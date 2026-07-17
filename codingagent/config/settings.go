package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/OrdalieTech/pi-go/ai"
)

const (
	ConfigDirName = ".pi"
	EnvAgentDir   = "PI_CODING_AGENT_DIR"
	EnvSessionDir = "PI_CODING_AGENT_SESSION_DIR"
)

type Settings map[string]any

type SettingsScope string

const (
	GlobalSettings  SettingsScope = "global"
	ProjectSettings SettingsScope = "project"
)

type SettingsError struct {
	Scope SettingsScope
	Err   error
}

func (e SettingsError) Error() string { return fmt.Sprintf("%s settings: %v", e.Scope, e.Err) }
func (e SettingsError) Unwrap() error { return e.Err }

type managerOptions struct {
	agentDir string
}

type Option func(*managerOptions)

func WithAgentDir(path string) Option {
	return func(options *managerOptions) { options.agentDir = path }
}

// SettingsManager keeps the source documents untyped so unknown keys and
// invalid known values do not make an otherwise valid settings file unreadable.
type SettingsManager struct {
	mu sync.RWMutex

	globalPath  string
	projectPath string

	global    Settings
	project   Settings
	effective Settings
	errors    []SettingsError
}

func NewSettingsManager(cwd string, options ...Option) (*SettingsManager, error) {
	settingsOptions := managerOptions{}
	for _, option := range options {
		option(&settingsOptions)
	}

	agentDir := settingsOptions.agentDir
	if agentDir == "" {
		var err error
		agentDir, err = GetAgentDir()
		if err != nil {
			return nil, err
		}
	} else {
		var err error
		agentDir, err = NormalizePath(agentDir)
		if err != nil {
			return nil, err
		}
	}

	resolvedCWD, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	resolvedAgentDir, err := resolvePath(agentDir)
	if err != nil {
		return nil, err
	}

	manager := &SettingsManager{
		globalPath:  filepath.Join(resolvedAgentDir, "settings.json"),
		projectPath: filepath.Join(resolvedCWD, ConfigDirName, "settings.json"),
		global:      Settings{},
		project:     Settings{},
	}
	manager.loadInitial()
	return manager, nil
}

func (manager *SettingsManager) loadInitial() {
	global, err := loadSettingsFile(manager.globalPath)
	if err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
	} else {
		manager.global = global
	}

	project, err := loadSettingsFile(manager.projectPath)
	if err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
	} else {
		manager.project = project
	}
	manager.effective = mergeSettings(manager.global, manager.project)
}

func loadSettingsFile(path string) (Settings, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Settings{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return Settings{}, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("settings contain multiple JSON values")
		}
		return nil, err
	}
	settings, ok := decoded.(map[string]any)
	if !ok {
		return nil, errors.New("settings must be a JSON object")
	}
	migrateSettings(settings)
	return Settings(settings), nil
}

func migrateSettings(settings map[string]any) {
	if queueMode, exists := settings["queueMode"]; exists {
		if _, hasSteeringMode := settings["steeringMode"]; !hasSteeringMode {
			settings["steeringMode"] = queueMode
			delete(settings, "queueMode")
		}
	}

	if _, hasTransport := settings["transport"]; !hasTransport {
		if websockets, ok := settings["websockets"].(bool); ok {
			if websockets {
				settings["transport"] = "websocket"
			} else {
				settings["transport"] = "sse"
			}
			delete(settings, "websockets")
		}
	}

	if legacySkills, ok := settings["skills"].(map[string]any); ok {
		if enabled, exists := legacySkills["enableSkillCommands"]; exists {
			if _, alreadySet := settings["enableSkillCommands"]; !alreadySet {
				settings["enableSkillCommands"] = enabled
			}
		}
		if directories, ok := legacySkills["customDirectories"].([]any); ok && len(directories) > 0 {
			settings["skills"] = directories
		} else {
			delete(settings, "skills")
		}
	}

	if retry, ok := settings["retry"].(map[string]any); ok {
		delay, hasDelay := retry["maxDelayMs"]
		if _, numeric := delay.(json.Number); hasDelay && numeric {
			provider, providerIsObject := retry["provider"].(map[string]any)
			if !providerIsObject {
				provider = map[string]any{}
			}
			current, hasCurrent := provider["maxRetryDelayMs"]
			if !hasCurrent || current == nil {
				provider = cloneMap(provider)
				provider["maxRetryDelayMs"] = delay
				retry["provider"] = provider
			}
		}
		delete(retry, "maxDelayMs")
	}
}

// mergeSettings matches upstream's one-level object spread: nested objects
// merge once, while objects below that level are replaced.
func mergeSettings(base, overrides Settings) Settings {
	result := cloneMap(base)
	for key, override := range overrides {
		baseObject, baseOK := base[key].(map[string]any)
		overrideObject, overrideOK := override.(map[string]any)
		if baseOK && overrideOK {
			merged := cloneMap(baseObject)
			for nestedKey, value := range overrideObject {
				merged[nestedKey] = cloneValue(value)
			}
			result[key] = merged
			continue
		}
		result[key] = cloneValue(override)
	}
	return result
}

func cloneMap[T ~map[string]any](source T) Settings {
	cloned := make(Settings, len(source))
	for key, value := range source {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case Settings:
		return cloneMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	default:
		return value
	}
}

func (manager *SettingsManager) Reload() {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if global, err := loadSettingsFile(manager.globalPath); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
	} else {
		manager.global = global
	}
	if project, err := loadSettingsFile(manager.projectPath); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
	} else {
		manager.project = project
	}
	manager.effective = mergeSettings(manager.global, manager.project)
}

func (manager *SettingsManager) DrainErrors() []SettingsError {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	errors := append([]SettingsError(nil), manager.errors...)
	manager.errors = nil
	return errors
}

func (manager *SettingsManager) GetGlobalSettings() Settings {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneMap(manager.global)
}

func (manager *SettingsManager) GetProjectSettings() Settings {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneMap(manager.project)
}

func (manager *SettingsManager) GetSettings() Settings {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return cloneMap(manager.effective)
}

func (manager *SettingsManager) value(key string) (any, bool) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	value, exists := manager.effective[key]
	return cloneValue(value), exists
}

func (manager *SettingsManager) GetDefaultProvider() string {
	return manager.stringValue("defaultProvider")
}
func (manager *SettingsManager) GetDefaultModel() string { return manager.stringValue("defaultModel") }

func (manager *SettingsManager) GetDefaultThinkingLevel() ai.ModelThinkingLevel {
	return ai.ModelThinkingLevel(manager.stringValue("defaultThinkingLevel"))
}

func (manager *SettingsManager) GetTransport() ai.Transport {
	value := manager.stringValue("transport")
	if value == "" {
		return ai.TransportAuto
	}
	return ai.Transport(value)
}

func (manager *SettingsManager) GetSteeringMode() string {
	if value := manager.stringValue("steeringMode"); value != "" {
		return value
	}
	return "one-at-a-time"
}

func (manager *SettingsManager) GetFollowUpMode() string {
	if value := manager.stringValue("followUpMode"); value != "" {
		return value
	}
	return "one-at-a-time"
}

func (manager *SettingsManager) GetSessionDir() (string, error) {
	value := manager.stringValue("sessionDir")
	if value == "" {
		return "", nil
	}
	return NormalizePath(value)
}

func (manager *SettingsManager) GetShellPath() (string, error) {
	value := manager.stringValue("shellPath")
	if value == "" {
		return "", nil
	}
	return NormalizePath(value)
}

func (manager *SettingsManager) GetShellCommandPrefix() string {
	return manager.stringValue("shellCommandPrefix")
}

func (manager *SettingsManager) stringValue(key string) string {
	value, _ := manager.value(key)
	result, _ := value.(string)
	return result
}

func GetAgentDir() (string, error) {
	if configured := os.Getenv(EnvAgentDir); configured != "" {
		return NormalizePath(configured)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ConfigDirName, "agent"), nil
}

// ResolveSessionDir applies CLI, environment, then merged-settings precedence.
func ResolveSessionDir(cliValue string, manager *SettingsManager) (string, error) {
	if cliValue != "" {
		return NormalizePath(cliValue)
	}
	if environmentValue := os.Getenv(EnvSessionDir); environmentValue != "" {
		return NormalizePath(environmentValue)
	}
	if manager == nil {
		return "", nil
	}
	return manager.GetSessionDir()
}

func NormalizePath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") || (runtime.GOOS == "windows" && strings.HasPrefix(path, `~\`)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, path[2:]), nil
	}
	if strings.HasPrefix(path, "file://") {
		parsed, err := url.Parse(path)
		if err != nil {
			return "", err
		}
		if parsed.Host != "" && parsed.Host != "localhost" {
			return "", fmt.Errorf("file URL has unsupported host %q", parsed.Host)
		}
		decoded, err := url.PathUnescape(parsed.EscapedPath())
		if err != nil {
			return "", err
		}
		if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
			decoded = decoded[1:]
		}
		return filepath.FromSlash(decoded), nil
	}
	return path, nil
}

func resolvePath(path string) (string, error) {
	normalized, err := NormalizePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(normalized)
}
