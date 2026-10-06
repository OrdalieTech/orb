package config

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/jsonwire"
	"github.com/OrdalieTech/orb/internal/skilllocations"
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
	"mcp.json",
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

func decodeTrust(contents []byte, path string) (trustFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(contents, []byte{0xef, 0xbb, 0xbf})))
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

// encodeTrust matches upstream's JSON.stringify(sorted, null, 2) + "\n".
func encodeTrust(data trustFile) ([]byte, error) {
	keys := slices.Collect(maps.Keys(data))
	sort.Slice(keys, func(left, right int) bool { return lessUTF16(keys[left], keys[right]) })
	var output bytes.Buffer
	if len(keys) == 0 {
		output.WriteString("{}")
	} else {
		output.WriteString("{\n")
		for index, key := range keys {
			encodedKey, err := jsonwire.MarshalString(key)
			if err != nil {
				return nil, err
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
	return output.Bytes(), nil
}

// lessUTF16 orders keys the way JS Array.prototype.sort() compares strings: by
// UTF-16 code units, which diverges from Go byte order for astral characters.
func lessUTF16(left, right string) bool {
	leftUnits := utf16.Encode([]rune(left))
	rightUnits := utf16.Encode([]rune(right))
	for index := 0; index < len(leftUnits) && index < len(rightUnits); index++ {
		if leftUnits[index] != rightUnits[index] {
			return leftUnits[index] < rightUnits[index]
		}
	}
	return len(leftUnits) < len(rightUnits)
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
// resources gated by project trust, including compatible Agent Skills roots.
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
	userSkillDirs := skilllocations.User(homeDir)
	if homeDir != "" {
		userSkillDirs = append(userSkillDirs, filepath.Join(homeDir, ".agents", "skills"))
	}
	userSkillSet := make(map[string]struct{}, len(userSkillDirs))
	for _, dir := range userSkillDirs {
		userSkillSet[canonicalizeTrustPath(dir)] = struct{}{}
	}
	currentDir := normalizeTrustCwd(cwd)

	configDir := filepath.Join(currentDir, ConfigDirName)
	for _, entry := range trustRequiringProjectConfigResources {
		if _, err := os.Stat(filepath.Join(configDir, entry)); err == nil {
			return true
		}
	}

	for {
		projectSkillDirs := append([]string{filepath.Join(currentDir, ".agents", "skills")}, skilllocations.Project(currentDir)...)
		for _, dir := range projectSkillDirs {
			if _, userRoot := userSkillSet[canonicalizeTrustPath(dir)]; userRoot {
				continue
			}
			if _, err := os.Stat(dir); err == nil {
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
	document host.Document
	name     string // the trust file path in error text
}

func NewProjectTrustStoreWithDocument(document host.Document) (*ProjectTrustStore, error) {
	if document == nil {
		return nil, errors.New("trust document is required")
	}
	return &ProjectTrustStore{document: document, name: "database"}, nil
}

func NewProjectTrustStore(agentDir string) *ProjectTrustStore {
	resolved, err := resolvePath(agentDir)
	if err != nil {
		resolved = agentDir
	}
	path := filepath.Join(resolved, "trust.json")
	return &ProjectTrustStore{document: fileDocument(path, 0o644), name: path}
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
	contents, err := store.document.Read(context.Background())
	if err != nil {
		return nil, fmt.Errorf("Failed to read trust store %s: %s", store.name, err) //nolint:staticcheck // Upstream error text is observable.
	}
	data, err := store.decode(contents)
	if err != nil {
		// pi rewrites trust.json in place under its lock: a read that caught
		// that write halfway is read again under the lock.
		if store.document.Update(context.Background(), func(current []byte) ([]byte, error) {
			contents = current
			return current, nil
		}) != nil {
			return nil, err
		}
		if data, err = store.decode(contents); err != nil {
			return nil, err
		}
	}
	return findNearestTrustEntry(data, cwd), nil
}

func (store *ProjectTrustStore) Set(cwd string, decision *bool) error {
	return store.SetMany([]ProjectTrustUpdate{{Path: cwd, Decision: decision}})
}

func (store *ProjectTrustStore) SetMany(decisions []ProjectTrustUpdate) error {
	return store.document.Update(context.Background(), func(contents []byte) ([]byte, error) {
		data, err := store.decode(contents)
		if err != nil {
			return nil, err
		}
		applyTrustUpdates(data, decisions)
		return encodeTrust(data)
	})
}

// decode treats a missing store as empty, like upstream's existsSync check.
func (store *ProjectTrustStore) decode(contents []byte) (trustFile, error) {
	if len(contents) == 0 {
		return trustFile{}, nil
	}
	return decodeTrust(contents, store.name)
}

func applyTrustUpdates(data trustFile, decisions []ProjectTrustUpdate) {
	for _, update := range decisions {
		key := normalizeTrustCwd(update.Path)
		if update.Decision == nil {
			delete(data, key)
		} else {
			data[key] = boolPtr(*update.Decision)
		}
	}
}

// FormatProjectTrustPrompt is the prompt shown when asking for project trust
// (upstream project-trust.ts formatProjectTrustPrompt).
func FormatProjectTrustPrompt(cwd string) string {
	return strings.Join([]string{
		"Trust project folder?",
		cwd,
		"",
		fmt.Sprintf("This allows orb to load %s settings and resources, install missing project packages, and execute project extensions.", ConfigDirName),
	}, "\n")
}
