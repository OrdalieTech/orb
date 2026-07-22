package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	ConfigDirName = ".pi"
	EnvAgentDir   = "PI_CODING_AGENT_DIR"
	EnvSessionDir = "PI_CODING_AGENT_SESSION_DIR"
)

type Settings map[string]any

type CompactionSettings struct {
	Enabled          bool
	ReserveTokens    int64
	KeepRecentTokens int64
}

type BranchSummarySettings struct {
	ReserveTokens int64
	SkipPrompt    bool
}

type RetrySettings struct {
	Enabled     bool
	MaxRetries  int
	BaseDelayMS int64
}

type ProviderRetrySettings struct {
	TimeoutMS       *int64
	MaxRetries      *int
	MaxRetryDelayMS int64
}

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
	agentDir       string
	projectTrusted *bool
}

type Option func(*managerOptions)

func WithAgentDir(path string) Option {
	return func(options *managerOptions) { options.agentDir = path }
}

// WithProjectTrusted gates project settings: untrusted managers load and merge
// only global settings (upstream SettingsManager projectTrusted option).
func WithProjectTrusted(trusted bool) Option {
	return func(options *managerOptions) { options.projectTrusted = &trusted }
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

	globalLoadError  bool
	projectLoadError bool
	projectTrusted   bool
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

	projectTrusted := true
	if settingsOptions.projectTrusted != nil {
		projectTrusted = *settingsOptions.projectTrusted
	}
	manager := &SettingsManager{
		globalPath:     filepath.Join(resolvedAgentDir, "settings.json"),
		projectPath:    filepath.Join(resolvedCWD, ConfigDirName, "settings.json"),
		global:         Settings{},
		project:        Settings{},
		projectTrusted: projectTrusted,
	}
	manager.loadInitial()
	return manager, nil
}

func (manager *SettingsManager) loadInitial() {
	global, err := loadSettingsFile(manager.globalPath)
	if err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: GlobalSettings, Err: err})
		manager.globalLoadError = true
	} else {
		manager.global = global
	}

	manager.project = Settings{}
	if manager.projectTrusted {
		project, err := loadSettingsFile(manager.projectPath)
		if err != nil {
			manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
			manager.projectLoadError = true
		} else {
			manager.project = project
		}
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
		manager.globalLoadError = true
	} else {
		manager.global = global
		manager.globalLoadError = false
	}
	manager.reloadProjectLocked()
	manager.effective = mergeSettings(manager.global, manager.project)
}

func (manager *SettingsManager) reloadProjectLocked() {
	if !manager.projectTrusted {
		manager.project = Settings{}
		manager.projectLoadError = false
		return
	}
	if project, err := loadSettingsFile(manager.projectPath); err != nil {
		manager.errors = append(manager.errors, SettingsError{Scope: ProjectSettings, Err: err})
		manager.projectLoadError = true
	} else {
		manager.project = project
		manager.projectLoadError = false
	}
}

func (manager *SettingsManager) IsProjectTrusted() bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.projectTrusted
}

// SetProjectTrusted matches upstream: revoking trust drops project settings,
// granting it reloads them from disk.
func (manager *SettingsManager) SetProjectTrusted(trusted bool) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.projectTrusted == trusted {
		return
	}
	manager.projectTrusted = trusted
	manager.reloadProjectLocked()
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

// GetHTTPProxy returns the settings-configured proxy for pi-managed HTTP
// clients (upstream http-dispatcher's httpProxy key).
func (manager *SettingsManager) GetHTTPProxy() string {
	return strings.TrimSpace(manager.stringValue("httpProxy"))
}

// HTTPIdleTimeoutChoice pairs an /settings label with its timeout value,
// mirroring upstream http-dispatcher HTTP_IDLE_TIMEOUT_CHOICES.
type HTTPIdleTimeoutChoice struct {
	Label     string
	TimeoutMS int64
}

// HTTPIdleTimeoutChoices lists the /settings selector values in upstream order.
var HTTPIdleTimeoutChoices = []HTTPIdleTimeoutChoice{
	{Label: "30 sec", TimeoutMS: 30_000},
	{Label: "1 min", TimeoutMS: 60_000},
	{Label: "2 min", TimeoutMS: 120_000},
	{Label: "5 min", TimeoutMS: 300_000},
	{Label: "disabled", TimeoutMS: 0},
}

// FormatHTTPIdleTimeoutMS renders a timeout as its choice label, falling back
// to upstream's "<seconds> sec" (JS number formatting) for custom values.
func FormatHTTPIdleTimeoutMS(timeoutMS int64) string {
	for _, choice := range HTTPIdleTimeoutChoices {
		if choice.TimeoutMS == timeoutMS {
			return choice.Label
		}
	}
	return strconv.FormatFloat(float64(timeoutMS)/1000, 'f', -1, 64) + " sec"
}

// ApplyHTTPProxySettings exports the configured proxy as HTTP_PROXY and
// HTTPS_PROXY unless the environment already sets them, matching upstream's
// applyHttpProxySettings; Go's default transport then honors them.
func ApplyHTTPProxySettings(proxy string) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return
	}
	if os.Getenv("HTTP_PROXY") == "" {
		_ = os.Setenv("HTTP_PROXY", proxy)
	}
	if os.Getenv("HTTPS_PROXY") == "" {
		_ = os.Setenv("HTTPS_PROXY", proxy)
	}
}

func (manager *SettingsManager) GetEnabledModels() []string {
	value, exists := manager.value("enabledModels")
	if !exists {
		return nil
	}
	raw, ok := value.([]any)
	if !ok {
		return nil
	}
	models := make([]string, 0, len(raw))
	for _, item := range raw {
		if model, ok := item.(string); ok {
			models = append(models, model)
		}
	}
	return models
}

func (manager *SettingsManager) AgentDir() string { return filepath.Dir(manager.globalPath) }
func (manager *SettingsManager) CWD() string      { return filepath.Dir(filepath.Dir(manager.projectPath)) }

func settingsStringSlice(settings Settings, key string) []string {
	value, exists := settings[key]
	if !exists {
		return nil
	}
	switch values := value.(type) {
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	case []string:
		return append([]string(nil), values...)
	default:
		return nil
	}
}

func (manager *SettingsManager) GetGlobalSkillPaths() []string {
	return settingsStringSlice(manager.GetGlobalSettings(), "skills")
}

func (manager *SettingsManager) GetProjectSkillPaths() []string {
	return settingsStringSlice(manager.GetProjectSettings(), "skills")
}

func (manager *SettingsManager) GetGlobalPromptTemplatePaths() []string {
	return settingsStringSlice(manager.GetGlobalSettings(), "prompts")
}

func (manager *SettingsManager) GetProjectPromptTemplatePaths() []string {
	return settingsStringSlice(manager.GetProjectSettings(), "prompts")
}

func (manager *SettingsManager) GetEnableSkillCommands() bool {
	value, exists := manager.value("enableSkillCommands")
	if enabled, ok := value.(bool); exists && ok {
		return enabled
	}
	return true
}

func (manager *SettingsManager) GetGoExtensions() map[string]bool {
	manager.mu.RLock()
	var configured map[string]any
	switch value := manager.effective["goExtensions"].(type) {
	case map[string]any:
		configured = value
	case Settings:
		configured = value
	}
	if len(configured) == 0 {
		manager.mu.RUnlock()
		return nil
	}
	result := make(map[string]bool, len(configured))
	for name, value := range configured {
		if enabled, ok := value.(bool); ok {
			result[name] = enabled
		}
	}
	manager.mu.RUnlock()
	return result
}

// GetPlugins returns the effective bundled-plugin gates. Missing entries are
// intentionally false so first-party plugins stay dormant by default.
func (manager *SettingsManager) GetPlugins() map[string]bool {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	configured := nestedObject(manager.effective, "plugins")
	result := make(map[string]bool, len(configured))
	for name, value := range configured {
		switch typed := value.(type) {
		case bool:
			result[name] = typed
		case map[string]any:
			enabled, configured := typed["enabled"].(bool)
			result[name] = !configured || enabled
		case Settings:
			enabled, configured := typed["enabled"].(bool)
			result[name] = !configured || enabled
		}
	}
	return result
}

// GetPluginSettings returns a copy of one structured plugin configuration.
func (manager *SettingsManager) GetPluginSettings(name string) map[string]any {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	configured := nestedObject(nestedObject(manager.effective, "plugins"), name)
	if configured == nil {
		return nil
	}
	return cloneMap(configured)
}

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

func (manager *SettingsManager) GetBlockImages() bool {
	return boolDefault(manager.objectValue("images"), "blockImages", false)
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

func (manager *SettingsManager) GetThemeSetting() string { return manager.stringValue("theme") }

func (manager *SettingsManager) GetTheme() string {
	value := manager.GetThemeSetting()
	if strings.Contains(value, "/") {
		return ""
	}
	return value
}

func (manager *SettingsManager) SetTheme(value string) {
	manager.setGlobalValues(settingMember("theme", value))
}

func (manager *SettingsManager) GetThemePaths() []string {
	return settingsStringSlice(manager.GetSettings(), "themes")
}

func (manager *SettingsManager) GetExternalEditor() string {
	if configured := strings.TrimSpace(manager.stringValue("externalEditor")); configured != "" {
		return configured
	}
	if editor := os.Getenv("VISUAL"); editor != "" {
		return editor
	}
	if editor := os.Getenv("EDITOR"); editor != "" {
		return editor
	}
	if runtime.GOOS == "windows" {
		return "notepad"
	}
	return "nano"
}

func (manager *SettingsManager) GetTreeFilterMode() string {
	value := manager.stringValue("treeFilterMode")
	switch value {
	case "default", "no-tools", "user-only", "labeled-only", "all":
		return value
	default:
		return "default"
	}
}

func (manager *SettingsManager) GetOutputPad() int {
	if int64Default(manager.GetSettings(), "outputPad", 1) == 0 {
		return 0
	}
	return 1
}

func (manager *SettingsManager) GetShowTerminalProgress() bool {
	return boolDefault(manager.objectValue("terminal"), "showTerminalProgress", false)
}

func (manager *SettingsManager) GetMarkdownCodeBlockIndent() string {
	value, _ := manager.objectValue("markdown")["codeBlockIndent"].(string)
	if value == "" {
		return "  "
	}
	return value
}

func (manager *SettingsManager) GetCompactionSettings() CompactionSettings {
	object := manager.objectValue("compaction")
	return CompactionSettings{
		Enabled:          boolDefault(object, "enabled", true),
		ReserveTokens:    int64Default(object, "reserveTokens", 16384),
		KeepRecentTokens: int64Default(object, "keepRecentTokens", 20000),
	}
}

func (manager *SettingsManager) GetBranchSummarySettings() BranchSummarySettings {
	object := manager.objectValue("branchSummary")
	return BranchSummarySettings{
		ReserveTokens: int64Default(object, "reserveTokens", 16384),
		SkipPrompt:    boolDefault(object, "skipPrompt", false),
	}
}

func (manager *SettingsManager) GetRetrySettings() RetrySettings {
	object := manager.objectValue("retry")
	return RetrySettings{
		Enabled:     boolDefault(object, "enabled", true),
		MaxRetries:  int(int64Default(object, "maxRetries", 3)),
		BaseDelayMS: int64Default(object, "baseDelayMs", 2000),
	}
}

func (manager *SettingsManager) GetProviderRetrySettings() ProviderRetrySettings {
	retry := manager.objectValue("retry")
	provider := nestedObject(retry, "provider")
	return ProviderRetrySettings{
		TimeoutMS:       optionalInt64(provider, "timeoutMs"),
		MaxRetries:      optionalInt(provider, "maxRetries"),
		MaxRetryDelayMS: int64Default(provider, "maxRetryDelayMs", 60000),
	}
}

// GetHTTPIdleTimeoutMS returns the provider idle timeout. Zero is preserved so
// SDK callers can translate upstream's disabled-timeout sentinel.
func (manager *SettingsManager) GetHTTPIdleTimeoutMS() (int64, error) {
	value, err := manager.timeoutSetting("httpIdleTimeoutMs")
	if err != nil {
		return 0, err
	}
	if value == nil {
		return 300000, nil
	}
	return *value, nil
}

// GetWebSocketConnectTimeoutMS returns the optional WebSocket handshake timeout.
func (manager *SettingsManager) GetWebSocketConnectTimeoutMS() (*int64, error) {
	return manager.timeoutSetting("websocketConnectTimeoutMs")
}

// GetThinkingBudgets returns custom token budgets for each thinking level.
func (manager *SettingsManager) GetThinkingBudgets() *ai.ThinkingBudgets {
	object := manager.objectValue("thinkingBudgets")
	if object == nil {
		return nil
	}
	return &ai.ThinkingBudgets{
		Minimal: optionalInt(object, "minimal"),
		Low:     optionalInt(object, "low"),
		Medium:  optionalInt(object, "medium"),
		High:    optionalInt(object, "high"),
	}
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

// GetWarningAnthropicExtraUsage reads warnings.anthropicExtraUsage, the gate
// for the Anthropic subscription-auth warning (upstream settings-manager
// WarningSettings.anthropicExtraUsage; default true).
func (manager *SettingsManager) GetWarningAnthropicExtraUsage() bool {
	return boolDefault(manager.objectValue("warnings"), "anthropicExtraUsage", true)
}

func (manager *SettingsManager) stringValue(key string) string {
	value, _ := manager.value(key)
	result, _ := value.(string)
	return result
}

func (manager *SettingsManager) objectValue(key string) map[string]any {
	value, _ := manager.value(key)
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case Settings:
		return typed
	default:
		return nil
	}
}

func nestedObject(object map[string]any, key string) map[string]any {
	if object == nil {
		return nil
	}
	switch typed := object[key].(type) {
	case map[string]any:
		return typed
	case Settings:
		return typed
	default:
		return nil
	}
}

func boolDefault(object map[string]any, key string, fallback bool) bool {
	if object != nil {
		if value, ok := object[key].(bool); ok {
			return value
		}
	}
	return fallback
}

func int64Default(object map[string]any, key string, fallback int64) int64 {
	if value := optionalInt64(object, key); value != nil {
		return *value
	}
	return fallback
}

func optionalInt(object map[string]any, key string) *int {
	value := optionalInt64(object, key)
	if value == nil {
		return nil
	}
	converted := int(*value)
	return &converted
}

func optionalInt64(object map[string]any, key string) *int64 {
	if object == nil {
		return nil
	}
	switch value := object[key].(type) {
	case json.Number:
		converted, err := value.Int64()
		if err == nil {
			return &converted
		}
		floatValue, err := value.Float64()
		if err == nil {
			converted = int64(floatValue)
			return &converted
		}
	case float64:
		converted := int64(value)
		return &converted
	case int:
		converted := int64(value)
		return &converted
	case int64:
		converted := value
		return &converted
	}
	return nil
}

func (manager *SettingsManager) timeoutSetting(key string) (*int64, error) {
	manager.mu.RLock()
	value, exists := manager.effective[key]
	manager.mu.RUnlock()
	if !exists {
		return nil, nil
	}
	var number float64
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return nil, invalidTimeoutSetting(key, value)
		}
		number = parsed
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	case string:
		trimmed := strings.TrimSpace(typed)
		if strings.EqualFold(trimmed, "disabled") {
			zero := int64(0)
			return &zero, nil
		}
		parsed, err := strconv.ParseFloat(trimmed, 64)
		if err != nil || trimmed == "" {
			return nil, invalidTimeoutSetting(key, value)
		}
		number = parsed
	default:
		return nil, invalidTimeoutSetting(key, value)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return nil, invalidTimeoutSetting(key, value)
	}
	converted := int64(math.Floor(number))
	return &converted, nil
}

func invalidTimeoutSetting(key string, value any) error {
	formatted := fmt.Sprint(value)
	if value == nil {
		formatted = "null"
	}
	return fmt.Errorf("Invalid %s setting: %s", key, formatted) //nolint:staticcheck // Upstream text.
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
