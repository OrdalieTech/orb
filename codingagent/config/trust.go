package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

// Port of packages/coding-agent/src/core/trust-manager.ts.

type ProjectTrustStoreEntry struct {
	Path     string `json:"path"`
	Decision bool   `json:"decision"`
}

// ProjectTrustUpdate carries a decision; nil removes the stored entry.
type ProjectTrustUpdate struct {
	Path     string
	Decision *bool
}

type ProjectTrustOption struct {
	Label     string
	Trusted   bool
	Updates   []ProjectTrustUpdate
	SavedPath string
}

var trustRequiringProjectConfigResources = []string{
	"settings.json",
	"extensions",
	"skills",
	"prompts",
	"themes",
	"SYSTEM.md",
	"APPEND_SYSTEM.md",
}

func canonicalizeTrustPath(path string) string {
	if canonical, err := filepath.EvalSymlinks(path); err == nil {
		return canonical
	}
	return path
}

func normalizeTrustCwd(cwd string) string {
	resolved, err := resolvePath(cwd)
	if err != nil {
		resolved = cwd
	}
	return canonicalizeTrustPath(resolved)
}

func GetProjectTrustParentPath(cwd string) string {
	trustPath := normalizeTrustCwd(cwd)
	parentDir := filepath.Dir(trustPath)
	if parentDir == trustPath {
		return ""
	}
	return parentDir
}

func boolPtr(value bool) *bool { return &value }

func GetProjectTrustOptions(cwd string, includeSessionOnly bool) []ProjectTrustOption {
	trustPath := normalizeTrustCwd(cwd)
	options := []ProjectTrustOption{{
		Label:     "Trust",
		Trusted:   true,
		Updates:   []ProjectTrustUpdate{{Path: trustPath, Decision: boolPtr(true)}},
		SavedPath: trustPath,
	}}
	if parentPath := GetProjectTrustParentPath(cwd); parentPath != "" {
		options = append(options, ProjectTrustOption{
			Label:   fmt.Sprintf("Trust parent folder (%s)", parentPath),
			Trusted: true,
			Updates: []ProjectTrustUpdate{
				{Path: parentPath, Decision: boolPtr(true)},
				{Path: trustPath, Decision: nil},
			},
			SavedPath: parentPath,
		})
	}
	if includeSessionOnly {
		options = append(options, ProjectTrustOption{Label: "Trust (this session only)", Trusted: true, Updates: []ProjectTrustUpdate{}})
	}
	options = append(options, ProjectTrustOption{
		Label:     "Do not trust",
		Trusted:   false,
		Updates:   []ProjectTrustUpdate{{Path: trustPath, Decision: boolPtr(false)}},
		SavedPath: trustPath,
	})
	if includeSessionOnly {
		options = append(options, ProjectTrustOption{Label: "Do not trust (this session only)", Trusted: false, Updates: []ProjectTrustUpdate{}})
	}
	return options
}

// trustFile entries: nil means an explicit JSON null kept in the file.
type trustFile map[string]*bool

func readTrustFile(path string) (trustFile, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return trustFile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("Failed to read trust store %s: %s", path, err) //nolint:staticcheck // Upstream error text is observable.
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("Failed to read trust store %s: %s", path, err) //nolint:staticcheck // Upstream error text is observable.
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("Failed to read trust store %s: unexpected trailing content", path) //nolint:staticcheck // Upstream error text is observable.
	}
	object, ok := parsed.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Invalid trust store %s: expected an object", path) //nolint:staticcheck // Upstream error text is observable.
	}
	data := trustFile{}
	for key, value := range object {
		switch typed := value.(type) {
		case bool:
			data[key] = boolPtr(typed)
		case nil:
			data[key] = nil
		default:
			encodedKey, _ := jsonwire.MarshalString(key)
			return nil, fmt.Errorf("Invalid trust store %s: value for %s must be true, false, or null", path, encodedKey) //nolint:staticcheck // Upstream error text is observable.
		}
	}
	return data, nil
}

// writeTrustFile matches upstream's JSON.stringify(sorted, null, 2) + "\n".
func writeTrustFile(path string, data trustFile) error {
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output bytes.Buffer
	if len(keys) == 0 {
		output.WriteString("{}")
	} else {
		output.WriteString("{\n")
		for index, key := range keys {
			encodedKey, err := jsonwire.MarshalString(key)
			if err != nil {
				return err
			}
			output.WriteString("  ")
			output.Write(encodedKey)
			output.WriteString(": ")
			switch value := data[key]; {
			case value == nil:
				output.WriteString("null")
			case *value:
				output.WriteString("true")
			default:
				output.WriteString("false")
			}
			if index < len(keys)-1 {
				output.WriteString(",")
			}
			output.WriteString("\n")
		}
		output.WriteString("}")
	}
	output.WriteString("\n")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, output.Bytes(), 0o644)
}

func findNearestTrustEntry(data trustFile, cwd string) *ProjectTrustStoreEntry {
	currentDir := normalizeTrustCwd(cwd)
	for {
		if value, exists := data[currentDir]; exists && value != nil {
			return &ProjectTrustStoreEntry{Path: currentDir, Decision: *value}
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return nil
		}
		currentDir = parentDir
	}
}

// HasTrustRequiringProjectResources reports whether cwd has project-local
// resources gated by project trust: trust-requiring entries under cwd/.pi, or
// .agents/skills in cwd or an ancestor. The user-global ~/.agents/skills is
// always a trusted user resource and is ignored here, even when cwd is $HOME.
func HasTrustRequiringProjectResources(cwd string) bool {
	homeDir := ""
	if home := os.Getenv("HOME"); home != "" {
		homeDir = home
	} else if home, err := os.UserHomeDir(); err == nil {
		homeDir = home
	}
	if resolved, err := resolvePath(homeDir); err == nil {
		homeDir = resolved
	}
	homeDir = canonicalizeTrustPath(homeDir)
	userAgentsSkillsDir := filepath.Join(homeDir, ".agents", "skills")
	currentDir := normalizeTrustCwd(cwd)

	configDir := filepath.Join(currentDir, ConfigDirName)
	for _, entry := range trustRequiringProjectConfigResources {
		if _, err := os.Stat(filepath.Join(configDir, entry)); err == nil {
			return true
		}
	}

	for {
		agentsSkillsDir := filepath.Join(currentDir, ".agents", "skills")
		if agentsSkillsDir != userAgentsSkillsDir {
			if _, err := os.Stat(agentsSkillsDir); err == nil {
				return true
			}
		}
		parentDir := filepath.Dir(currentDir)
		if parentDir == currentDir {
			return false
		}
		currentDir = parentDir
	}
}

// ProjectTrustStore persists project trust decisions in <agentDir>/trust.json.
type ProjectTrustStore struct {
	trustPath string
}

func NewProjectTrustStore(agentDir string) *ProjectTrustStore {
	resolved, err := resolvePath(agentDir)
	if err != nil {
		resolved = agentDir
	}
	return &ProjectTrustStore{trustPath: filepath.Join(resolved, "trust.json")}
}

// Get returns the nearest stored decision for cwd, or nil when undecided.
func (store *ProjectTrustStore) Get(cwd string) (*bool, error) {
	entry, err := store.GetEntry(cwd)
	if err != nil || entry == nil {
		return nil, err
	}
	return boolPtr(entry.Decision), nil
}

func (store *ProjectTrustStore) GetEntry(cwd string) (*ProjectTrustStoreEntry, error) {
	var entry *ProjectTrustStoreEntry
	err := withSettingsLock(store.trustPath, func() error {
		data, err := readTrustFile(store.trustPath)
		if err != nil {
			return err
		}
		entry = findNearestTrustEntry(data, cwd)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entry, nil
}

func (store *ProjectTrustStore) Set(cwd string, decision *bool) error {
	return store.SetMany([]ProjectTrustUpdate{{Path: cwd, Decision: decision}})
}

func (store *ProjectTrustStore) SetMany(decisions []ProjectTrustUpdate) error {
	return withSettingsLock(store.trustPath, func() error {
		data, err := readTrustFile(store.trustPath)
		if err != nil {
			return err
		}
		for _, update := range decisions {
			key := normalizeTrustCwd(update.Path)
			if update.Decision == nil {
				delete(data, key)
			} else {
				data[key] = boolPtr(*update.Decision)
			}
		}
		return writeTrustFile(store.trustPath, data)
	})
}

// FormatProjectTrustPrompt is the prompt shown when asking for project trust
// (upstream project-trust.ts formatProjectTrustPrompt).
func FormatProjectTrustPrompt(cwd string) string {
	return strings.Join([]string{
		"Trust project folder?",
		cwd,
		"",
		fmt.Sprintf("This allows pigo to load %s settings and resources, install missing project packages, and execute project extensions.", ConfigDirName),
	}, "\n")
}
