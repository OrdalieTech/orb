package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	modetheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/codingagent/session/exporthtml"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

type ModelCycleResult struct {
	Model         ai.Model              `json:"model"`
	ThinkingLevel ai.ModelThinkingLevel `json:"thinkingLevel"`
	IsScoped      bool                  `json:"isScoped"`
}

type SessionTokenTotals struct {
	Input      int64 `json:"input"`
	Output     int64 `json:"output"`
	CacheRead  int64 `json:"cacheRead"`
	CacheWrite int64 `json:"cacheWrite"`
	Total      int64 `json:"total"`
}

type SessionStats struct {
	SessionFile       string                `json:"sessionFile,omitempty"`
	SessionID         string                `json:"sessionId"`
	UserMessages      int                   `json:"userMessages"`
	AssistantMessages int                   `json:"assistantMessages"`
	ToolCalls         int                   `json:"toolCalls"`
	ToolResults       int                   `json:"toolResults"`
	TotalMessages     int                   `json:"totalMessages"`
	Tokens            SessionTokenTotals    `json:"tokens"`
	Cost              float64               `json:"cost"`
	ContextUsage      *harness.ContextUsage `json:"contextUsage,omitempty"`
}

type FooterSnapshot struct {
	Display               agent.AgentDisplayState
	Tokens                SessionTokenTotals
	Cost                  float64
	ContextUsage          *harness.ContextUsage
	LatestCacheHitRate    float64
	HasLatestCacheHitRate bool
	AutoCompactEnabled    bool
}

type UsageCostBreakdownEntry struct {
	Key    string  `json:"key"`
	Cost   float64 `json:"cost"`
	Tokens int64   `json:"tokens"`
}

func addSessionUsage(tokens *SessionTokenTotals, cost *float64, usage *ai.Usage) {
	if usage == nil {
		return
	}
	tokens.Input += usage.Input
	tokens.Output += usage.Output
	tokens.CacheRead += usage.CacheRead
	tokens.CacheWrite += usage.CacheWrite
	*cost += usage.Cost.Total
}

// GetUsageCostBreakdown groups model-attributed usage and auxiliary tool/summary usage.
func GetUsageCostBreakdown(entries []sessionstore.SessionEntry) []UsageCostBreakdownEntry {
	result := make([]UsageCostBreakdownEntry, 0)
	byKey := make(map[string]int)
	for _, entry := range entries {
		key := ""
		var usage *ai.Usage
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			key, usage = "Tools/summaries", entry.Usage
		} else if entry.Type == "message" {
			message, err := ai.UnmarshalMessage(entry.Message)
			if err != nil {
				continue
			}
			switch typed := message.(type) {
			case *ai.AssistantMessage:
				model := typed.Model
				if typed.ResponseModel != nil {
					model = *typed.ResponseModel
				}
				key, usage = string(typed.Provider)+"/"+model, &typed.Usage
			case *ai.ToolResultMessage:
				key, usage = "Tools/summaries", typed.Usage
			}
		}
		if key == "" || usage == nil {
			continue
		}
		index, ok := byKey[key]
		if !ok {
			index = len(result)
			byKey[key] = index
			result = append(result, UsageCostBreakdownEntry{Key: key})
		}
		result[index].Cost += usage.Cost.Total
		result[index].Tokens += usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
	}
	result = slices.DeleteFunc(result, func(entry UsageCostBreakdownEntry) bool { return entry.Cost == 0 && entry.Tokens == 0 })
	sort.SliceStable(result, func(left, right int) bool { return result[left].Cost > result[right].Cost })
	return result
}

func (runtime *SessionRuntime) Manager() *sessionstore.SessionManager {
	if runtime == nil {
		return nil
	}
	return runtime.manager
}

func (runtime *SessionRuntime) PromptPreflight(ctx context.Context) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	state := runtime.agent.State()
	if state.Model == nil || (IsUnknownModel(state.Model) && runtime.getRequestAuth == nil && runtime.getAPIKey == nil) {
		return noModelSelectedError()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	hasAuth, err := runtime.hasProviderAuth(ctx, state.Model.Provider)
	if err != nil {
		return err
	}
	if !hasAuth {
		return errors.New(formatNoAPIKeyFoundMessage(state.Model.Provider))
	}
	for index := len(state.Messages) - 1; index >= 0; index-- {
		if assistant := asAssistant(state.Messages[index]); assistant != nil {
			_, _ = runtime.checkCompaction(ctx, assistant, false)
			break
		}
	}
	return nil
}

func (runtime *SessionRuntime) hasProviderAuth(ctx context.Context, provider ai.ProviderID) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if runtime.getRequestAuth != nil {
		resolved, err := runtime.getRequestAuth(ctx, provider)
		return resolved != nil, err
	}
	if runtime.getAPIKey != nil {
		key, err := runtime.getAPIKey(ctx, provider)
		return key != nil && *key != "", err
	}
	return true, nil
}

func (runtime *SessionRuntime) AvailableModels() []ai.Model {
	if runtime == nil || runtime.availableModels == nil {
		return []ai.Model{}
	}
	models := runtime.availableModels()
	return append(make([]ai.Model, 0, len(models)), models...)
}

func (runtime *SessionRuntime) IsCompacting() bool {
	if runtime == nil {
		return false
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.compactionCancel != nil || runtime.branchCancel != nil
}

func (runtime *SessionRuntime) PendingMessageCount() int {
	if runtime == nil {
		return 0
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return len(runtime.steering) + len(runtime.followUps)
}

func (runtime *SessionRuntime) DequeueMessages() []string {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	messages := append([]string(nil), runtime.steering...)
	messages = append(messages, runtime.followUps...)
	runtime.steering, runtime.followUps = nil, nil
	runtime.mu.Unlock()
	runtime.agent.ClearSteeringQueue()
	runtime.agent.ClearFollowUpQueue()
	runtime.emitQueueUpdate()
	return messages
}

// PendingMessages returns the queued steering and follow-up texts in queue
// order as copied slices — the pull-based counterpart of QueueUpdateEvent, so
// the TUI can render queue contents without racing delivery removal.
func (runtime *SessionRuntime) PendingMessages() QueueUpdateEvent {
	if runtime == nil {
		return QueueUpdateEvent{Steering: []string{}, FollowUp: []string{}}
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return QueueUpdateEvent{
		Steering: append([]string{}, runtime.steering...),
		FollowUp: append([]string{}, runtime.followUps...),
	}
}

// InteractiveSettings is an immutable snapshot of the documented UI settings
// interactive mode reads (upstream docs/settings.md UI keys); the mutable
// SettingsManager itself is never exposed.
type InteractiveSettings struct {
	QuietStartup           bool
	DoubleEscapeAction     string
	ClearOnShrink          bool
	HideThinkingBlock      bool
	ShowCacheMissNotices   bool
	ShowImages             bool
	ImageWidthCells        int
	ShowHardwareCursor     bool
	EditorPaddingX         int
	AutocompleteMaxVisible int
	SteeringMode           agent.QueueMode
	FollowUpMode           agent.QueueMode
}

func (runtime *SessionRuntime) InteractiveSettings() InteractiveSettings {
	if runtime == nil {
		return InteractiveSettings{}
	}
	return InteractiveSettings{
		QuietStartup:           runtime.settings.GetQuietStartup(),
		DoubleEscapeAction:     runtime.settings.GetDoubleEscapeAction(),
		ClearOnShrink:          runtime.settings.GetClearOnShrink(),
		HideThinkingBlock:      runtime.settings.GetHideThinkingBlock(),
		ShowCacheMissNotices:   runtime.settings.GetShowCacheMissNotices(),
		ShowImages:             runtime.settings.GetShowImages(),
		ImageWidthCells:        runtime.settings.GetImageWidthCells(),
		ShowHardwareCursor:     runtime.settings.GetShowHardwareCursor(),
		EditorPaddingX:         runtime.settings.GetEditorPaddingX(),
		AutocompleteMaxVisible: runtime.settings.GetAutocompleteMaxVisible(),
		SteeringMode:           agent.QueueMode(runtime.settings.GetSteeringMode()),
		FollowUpMode:           agent.QueueMode(runtime.settings.GetFollowUpMode()),
	}
}

// InteractiveModeSettings is the immutable startup/runtime configuration the
// TUI consumes in addition to the frequently read InteractiveSettings values.
type InteractiveModeSettings struct {
	InteractiveSettings
	AgentDir             string
	ProjectTrusted       bool
	GlobalThemePaths     []string
	ProjectThemePaths    []string
	ThemeSetting         string
	ImageAutoResize      bool
	BlockImages          bool
	EnableSkillCommands  bool
	Transport            ai.Transport
	HTTPIdleTimeoutMS    int64
	OutputPad            int
	ExternalEditor       string
	TreeFilterMode       string
	DefaultProjectTrust  string
	ShowTerminalProgress bool
}

func (runtime *SessionRuntime) InteractiveModeSettings() InteractiveModeSettings {
	if runtime == nil {
		return InteractiveModeSettings{}
	}
	httpIdleTimeout, err := runtime.settings.GetHTTPIdleTimeoutMS()
	if err != nil {
		httpIdleTimeout = 300000
	}
	return InteractiveModeSettings{
		InteractiveSettings:  runtime.InteractiveSettings(),
		AgentDir:             runtime.settings.AgentDir(),
		ProjectTrusted:       runtime.settings.IsProjectTrusted(),
		GlobalThemePaths:     append([]string(nil), runtime.settings.GetGlobalThemePaths()...),
		ProjectThemePaths:    append([]string(nil), runtime.settings.GetProjectThemePaths()...),
		ThemeSetting:         runtime.settings.GetThemeSetting(),
		ImageAutoResize:      runtime.settings.GetImageAutoResize(),
		BlockImages:          runtime.settings.GetBlockImages(),
		EnableSkillCommands:  runtime.settings.GetEnableSkillCommands(),
		Transport:            runtime.settings.GetTransport(),
		HTTPIdleTimeoutMS:    httpIdleTimeout,
		OutputPad:            runtime.settings.GetOutputPad(),
		ExternalEditor:       runtime.settings.GetExternalEditor(),
		TreeFilterMode:       runtime.settings.GetTreeFilterMode(),
		DefaultProjectTrust:  runtime.settings.GetDefaultProjectTrust(),
		ShowTerminalProgress: runtime.settings.GetShowTerminalProgress(),
	}
}

func (runtime *SessionRuntime) SetTheme(name string) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	runtime.settings.SetTheme(name)
	return nil
}

func (runtime *SessionRuntime) SetHideThinkingBlock(hidden bool) {
	if runtime != nil {
		runtime.settings.SetHideThinkingBlock(hidden)
	}
}

func (runtime *SessionRuntime) SetShowCacheMissNotices(show bool) {
	if runtime != nil {
		runtime.settings.SetShowCacheMissNotices(show)
	}
}

func (runtime *SessionRuntime) SetShowImages(show bool) {
	if runtime != nil {
		runtime.settings.SetShowImages(show)
	}
}

func (runtime *SessionRuntime) SetImageWidthCells(width int) {
	if runtime != nil {
		runtime.settings.SetImageWidthCells(width)
	}
}

func (runtime *SessionRuntime) SetImageAutoResize(enabled bool) {
	if runtime != nil {
		runtime.settings.SetImageAutoResize(enabled)
	}
}

func (runtime *SessionRuntime) SetBlockImages(blocked bool) {
	if runtime != nil {
		runtime.settings.SetBlockImages(blocked)
	}
}

func (runtime *SessionRuntime) SetEnableSkillCommands(enabled bool) {
	if runtime != nil {
		runtime.settings.SetEnableSkillCommands(enabled)
	}
}

func (runtime *SessionRuntime) SetTransport(transport ai.Transport) {
	if runtime != nil {
		runtime.settings.SetTransport(transport)
		runtime.agent.SetTransport(transport)
	}
}

func (runtime *SessionRuntime) SetHTTPIdleTimeoutMS(timeoutMS int64) {
	if runtime != nil {
		runtime.settings.SetHTTPIdleTimeoutMS(timeoutMS)
	}
}

func (runtime *SessionRuntime) SetQuietStartup(enabled bool) {
	if runtime != nil {
		runtime.settings.SetQuietStartup(enabled)
	}
}

func (runtime *SessionRuntime) SetDefaultProjectTrust(value string) {
	if runtime != nil {
		runtime.settings.SetDefaultProjectTrust(value)
	}
}

func (runtime *SessionRuntime) SetDoubleEscapeAction(action string) {
	if runtime != nil {
		runtime.settings.SetDoubleEscapeAction(action)
	}
}

func (runtime *SessionRuntime) SetTreeFilterMode(value string) {
	if runtime != nil {
		runtime.settings.SetTreeFilterMode(value)
	}
}

func (runtime *SessionRuntime) SetShowHardwareCursor(enabled bool) {
	if runtime != nil {
		runtime.settings.SetShowHardwareCursor(enabled)
	}
}

func (runtime *SessionRuntime) SetEditorPaddingX(padding int) {
	if runtime != nil {
		runtime.settings.SetEditorPaddingX(padding)
	}
}

func (runtime *SessionRuntime) SetOutputPad(padding int) {
	if runtime != nil {
		runtime.settings.SetOutputPad(padding)
	}
}

func (runtime *SessionRuntime) SetAutocompleteMaxVisible(visible int) {
	if runtime != nil {
		runtime.settings.SetAutocompleteMaxVisible(visible)
	}
}

func (runtime *SessionRuntime) SetClearOnShrink(enabled bool) {
	if runtime != nil {
		runtime.settings.SetClearOnShrink(enabled)
	}
}

func (runtime *SessionRuntime) SetShowTerminalProgress(enabled bool) {
	if runtime != nil {
		runtime.settings.SetShowTerminalProgress(enabled)
	}
}

func (runtime *SessionRuntime) SetEnabledModels(models []string) {
	if runtime != nil {
		runtime.settings.SetEnabledModels(append([]string(nil), models...))
	}
}

func (runtime *SessionRuntime) SteeringMode() agent.QueueMode {
	if runtime == nil {
		return agent.QueueOneAtATime
	}
	return runtime.agent.SteeringMode()
}

func (runtime *SessionRuntime) FollowUpMode() agent.QueueMode {
	if runtime == nil {
		return agent.QueueOneAtATime
	}
	return runtime.agent.FollowUpMode()
}

func (runtime *SessionRuntime) SetSteeringMode(mode agent.QueueMode) {
	runtime.agent.SetSteeringMode(mode)
	runtime.settings.SetSteeringMode(string(mode))
}

func (runtime *SessionRuntime) SetFollowUpMode(mode agent.QueueMode) {
	runtime.agent.SetFollowUpMode(mode)
	runtime.settings.SetFollowUpMode(string(mode))
}

func (runtime *SessionRuntime) SetModel(ctx context.Context, model ai.Model) error {
	return runtime.setModel(ctx, model, nil, true, extensions.ModelSelectSet)
}

func (runtime *SessionRuntime) setModel(
	ctx context.Context,
	model ai.Model,
	explicitThinking *ai.ModelThinkingLevel,
	checkAuth bool,
	source extensions.ModelSelectSource,
) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	if checkAuth {
		if ctx == nil {
			ctx = context.Background()
		}
		hasAuth, err := runtime.hasProviderAuth(ctx, model.Provider)
		if err != nil {
			return err
		}
		if !hasAuth {
			return errors.New("No API key for " + string(model.Provider) + "/" + model.ID) //nolint:staticcheck // Upstream RPC error text.
		}
	}
	state := runtime.agent.State()
	previous := state.Model
	thinkingLevel := runtime.thinkingLevelForModelSwitch(state, explicitThinking)
	runtime.agent.SetModel(&model)
	if _, err := runtime.manager.AppendModelChange(string(model.Provider), model.ID); err != nil {
		return err
	}
	runtime.settings.SetDefaultModelAndProvider(string(model.Provider), model.ID)
	if err := runtime.SetThinkingLevel(thinkingLevel); err != nil {
		return err
	}
	if state := runtime.extensionState; state != nil && state.runner.HasHandlers(extensions.EventModelSelect) && !sameModel(previous, &model) {
		state.runner.Emit(ctx, extensions.ModelSelectEvent{Model: &model, PreviousModel: previous, Source: source})
	}
	return nil
}

func (runtime *SessionRuntime) CycleModel(ctx context.Context) (*ModelCycleResult, error) {
	return runtime.cycleModel(ctx, 1)
}

// CycleModelBackward is upstream cycleModel("backward"): same scope selection,
// auth filtering, and thinking-level handling as CycleModel, stepping in reverse.
func (runtime *SessionRuntime) CycleModelBackward(ctx context.Context) (*ModelCycleResult, error) {
	return runtime.cycleModel(ctx, -1)
}

// step is +1 (forward) or -1 (backward); wraparound matches upstream
// (currentIndex + step + len) % len with an absent current model treated as index 0.
func (runtime *SessionRuntime) cycleModel(ctx context.Context, step int) (*ModelCycleResult, error) {
	if runtime == nil {
		return nil, errors.New("codingagent: nil session runtime")
	}
	if len(runtime.scopedModels) > 0 {
		models := make([]ScopedModel, 0, len(runtime.scopedModels))
		for _, scoped := range runtime.scopedModels {
			hasAuth, err := runtime.hasProviderAuth(ctx, scoped.Model.Provider)
			if err != nil {
				return nil, err
			}
			if !hasAuth {
				continue
			}
			models = append(models, scoped)
		}
		if len(models) <= 1 {
			return nil, nil
		}
		state := runtime.agent.State()
		index := slices.IndexFunc(models, func(scoped ScopedModel) bool {
			return state.Model != nil && scoped.Model.Provider == state.Model.Provider && scoped.Model.ID == state.Model.ID
		})
		if index < 0 {
			index = 0
		}
		next := models[(index+step+len(models))%len(models)]
		if err := runtime.setModel(ctx, next.Model, next.ThinkingLevel, false, extensions.ModelSelectCycle); err != nil {
			return nil, err
		}
		return &ModelCycleResult{
			Model: next.Model, ThinkingLevel: runtime.agent.State().ThinkingLevel, IsScoped: true,
		}, nil
	}
	models := runtime.AvailableModels()
	if len(models) <= 1 {
		return nil, nil
	}
	state := runtime.agent.State()
	index := slices.IndexFunc(models, func(model ai.Model) bool {
		return state.Model != nil && model.Provider == state.Model.Provider && model.ID == state.Model.ID
	})
	if index < 0 {
		index = 0
	}
	next := models[(index+step+len(models))%len(models)]
	if err := runtime.setModel(ctx, next, nil, true, extensions.ModelSelectCycle); err != nil {
		return nil, err
	}
	return &ModelCycleResult{Model: next, ThinkingLevel: runtime.agent.State().ThinkingLevel}, nil
}

func (runtime *SessionRuntime) ScopedModels() []ScopedModel {
	if runtime == nil {
		return nil
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return append([]ScopedModel(nil), runtime.scopedModels...)
}

func (runtime *SessionRuntime) SetScopedModels(models []ScopedModel) {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	runtime.scopedModels = append([]ScopedModel(nil), models...)
	runtime.mu.Unlock()
}

func (runtime *SessionRuntime) thinkingLevelForModelSwitch(
	state agent.AgentState,
	explicit *ai.ModelThinkingLevel,
) ai.ModelThinkingLevel {
	if explicit != nil {
		return *explicit
	}
	if state.Model != nil && state.Model.Reasoning {
		return state.ThinkingLevel
	}
	level := runtime.settings.GetDefaultThinkingLevel()
	if level == "" {
		level = ai.ModelThinkingMedium
	}
	return level
}

func (runtime *SessionRuntime) SetThinkingLevel(level ai.ModelThinkingLevel) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	state := runtime.agent.State()
	effective := ai.ClampThinkingLevel(state.Model, level)
	if effective == state.ThinkingLevel {
		return nil
	}
	runtime.agent.SetThinkingLevel(effective)
	if _, err := runtime.manager.AppendThinkingLevelChange(string(effective)); err != nil {
		return err
	}
	if effective != ai.ModelThinkingOff || state.Model != nil && state.Model.Reasoning {
		runtime.settings.SetDefaultThinkingLevel(effective)
	}
	runtime.emit(ThinkingLevelChangedEvent{Level: effective})
	if extensionState := runtime.extensionState; extensionState != nil && extensionState.runner.HasHandlers(extensions.EventThinkingLevelSelect) {
		extensionState.runner.Emit(context.Background(), extensions.ThinkingLevelSelectEvent{Level: effective, PreviousLevel: state.ThinkingLevel})
	}
	return nil
}

func (runtime *SessionRuntime) CycleThinkingLevel() (*ai.ModelThinkingLevel, error) {
	state := runtime.agent.State()
	levels := ai.SupportedThinkingLevels(state.Model)
	if state.Model == nil || !state.Model.Reasoning {
		return nil, nil
	}
	index := slices.Index(levels, state.ThinkingLevel)
	next := levels[(index+1)%len(levels)]
	if err := runtime.SetThinkingLevel(next); err != nil {
		return nil, err
	}
	return &next, nil
}

func (runtime *SessionRuntime) AvailableThinkingLevels() []ai.ModelThinkingLevel {
	if runtime == nil {
		return nil
	}
	return append([]ai.ModelThinkingLevel(nil), ai.SupportedThinkingLevels(runtime.agent.State().Model)...)
}

// providerLoginHelp mirrors upstream getProviderLoginHelp (auth-guidance.ts:6-12).
func providerLoginHelp() string {
	providersDoc, modelsDoc := authGuidanceDocPaths()
	return "Use /login to log into a provider via OAuth or API key. See:\n  " + providersDoc + "\n  " + modelsDoc
}

//nolint:staticcheck // User-visible auth guidance matches upstream.
func noModelSelectedError() error {
	return errors.New("No model selected.\n\n" + providerLoginHelp() + "\n\nThen use /model to select a model.")
}

// FormatNoModelsAvailableMessage exposes upstream
// formatNoModelsAvailableMessage (auth-guidance.ts:14-16) to CLI callers.
func FormatNoModelsAvailableMessage() string {
	return formatNoModelsAvailableMessage()
}

//nolint:staticcheck // User-visible auth guidance matches upstream.
func formatNoAPIKeyFoundMessage(provider ai.ProviderID) string {
	display := string(provider)
	if display == "unknown" {
		display = "the selected model"
	}
	return "No API key found for " + display + ".\n\n" + providerLoginHelp()
}

// authGuidanceDocPaths mirrors upstream getDocsPath (auth-guidance.ts:6-12)
// when a package layout is configured or the docs ship next to the binary, and
// falls back to the hosted docs for standalone binaries where
// <dir-of-binary>/docs does not exist.
func authGuidanceDocPaths() (providersDoc, modelsDoc string) {
	docsDir := filepath.Join(resolvePromptPackageDir(""), "docs")
	providersDoc = filepath.Join(docsDir, "providers.md")
	if _, err := os.Stat(providersDoc); err != nil && os.Getenv("PI_PACKAGE_DIR") == "" {
		return "https://github.com/OrdalieTech/pigo/blob/main/docs/providers.md",
			"https://github.com/OrdalieTech/pigo/blob/main/docs/models.md"
	}
	return providersDoc, filepath.Join(docsDir, "models.md")
}

// AuthGuidanceDocPaths exposes the auth-guidance doc pointers to the CLI and
// TUI so every login/model hint resolves docs the same way (upstream
// getDocsPath consumers in auth-guidance.ts and interactive-mode.ts).
func AuthGuidanceDocPaths() (providersDoc, modelsDoc string) {
	return authGuidanceDocPaths()
}

func (runtime *SessionRuntime) SetAutoCompactionEnabled(enabled bool) {
	runtime.settings.SetCompactionEnabled(enabled)
	runtime.mu.Lock()
	runtime.autoCompaction = runtime.settings.GetCompactionSettings().Enabled
	runtime.mu.Unlock()
}

func (runtime *SessionRuntime) AutoCompactionEnabled() bool { return runtime.autoCompactionEnabled() }

func (runtime *SessionRuntime) autoCompactionEnabled() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.autoCompaction
}

func (runtime *SessionRuntime) SetAutoRetryEnabled(enabled bool) {
	runtime.settings.SetRetryEnabled(enabled)
	runtime.mu.Lock()
	runtime.autoRetry = runtime.settings.GetRetrySettings().Enabled
	runtime.mu.Unlock()
}

func (runtime *SessionRuntime) AutoRetryEnabled() bool { return runtime.autoRetryEnabled() }

func (runtime *SessionRuntime) autoRetryEnabled() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return runtime.autoRetry
}

func (runtime *SessionRuntime) AbortRetry() {
	runtime.mu.Lock()
	cancel := runtime.retryCancel
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (runtime *SessionRuntime) ExecuteBash(ctx context.Context, command string, excludeFromContext *bool) (tools.BashResult, error) {
	if runtime == nil {
		return tools.BashResult{}, errors.New("codingagent: nil session runtime")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bashContext, cancel := context.WithCancel(ctx)
	runtime.mu.Lock()
	runtime.bashCancel = cancel
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		runtime.bashCancel = nil
		runtime.mu.Unlock()
	}()
	shellPath, err := runtime.settings.GetShellPath()
	if err != nil {
		return tools.BashResult{}, err
	}
	result, err := tools.ExecuteBash(
		bashContext, command, runtime.manager.GetCWD(), runtime.settings.GetShellCommandPrefix(), shellPath,
	)
	if err != nil {
		return result, err
	}
	return result, runtime.recordBash(command, result, excludeFromContext)
}

func (runtime *SessionRuntime) AbortBash() {
	runtime.mu.Lock()
	cancel := runtime.bashCancel
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (runtime *SessionRuntime) recordBash(command string, result tools.BashResult, exclude *bool) error {
	var fullOutputPath *string
	if result.FullOutputPath != "" {
		path := result.FullOutputPath
		fullOutputPath = &path
	}
	var excludeFromContext *bool
	if exclude != nil {
		value := *exclude
		excludeFromContext = &value
	}
	message := harness.BashExecutionMessage{
		Role: "bashExecution", Command: command, Output: result.Output, ExitCode: result.ExitCode,
		Cancelled: result.Cancelled, Truncated: result.Truncated, FullOutputPath: fullOutputPath,
		ExcludeFromContext: excludeFromContext, Timestamp: runtime.clock(),
	}
	if runtime.agent.State().IsStreaming {
		runtime.mu.Lock()
		runtime.pendingBash = append(runtime.pendingBash, message)
		runtime.mu.Unlock()
		return nil
	}
	return runtime.appendBash(message)
}

func (runtime *SessionRuntime) flushPendingBash() error {
	runtime.mu.Lock()
	pending := append([]harness.BashExecutionMessage(nil), runtime.pendingBash...)
	runtime.mu.Unlock()
	for _, message := range pending {
		if err := runtime.appendBash(message); err != nil {
			return err
		}
	}
	runtime.mu.Lock()
	runtime.pendingBash = runtime.pendingBash[len(pending):]
	runtime.mu.Unlock()
	return nil
}

func (runtime *SessionRuntime) appendBash(message harness.BashExecutionMessage) error {
	state := runtime.agent.State()
	state.Messages = append(state.Messages, message)
	runtime.agent.SetMessages(state.Messages)
	_, err := runtime.manager.AppendMessage(message)
	return err
}

func (runtime *SessionRuntime) GetUserMessagesForForking() []struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
} {
	result := make([]struct {
		EntryID string `json:"entryId"`
		Text    string `json:"text"`
	}, 0)
	for _, entry := range runtime.manager.GetEntries() {
		if entry.Type != "message" {
			continue
		}
		role, text := jsonwire.MessageRoleAndText(entry.Message)
		if role == "user" && text != "" {
			result = append(result, struct {
				EntryID string `json:"entryId"`
				Text    string `json:"text"`
			}{entry.ID, text})
		}
	}
	return result
}

func (runtime *SessionRuntime) GetSessionStats() SessionStats {
	stats := SessionStats{SessionFile: runtime.manager.GetSessionFile(), SessionID: runtime.manager.GetSessionID()}
	for _, entry := range runtime.manager.GetEntries() {
		if entry.Type == "compaction" || entry.Type == "branch_summary" {
			addSessionUsage(&stats.Tokens, &stats.Cost, entry.Usage)
		}
		if entry.Type != "message" {
			continue
		}
		stats.TotalMessages++
		var role struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(entry.Message, &role) != nil {
			continue
		}
		switch role.Role {
		case "user":
			stats.UserMessages++
		case "toolResult":
			stats.ToolResults++
			message, err := ai.UnmarshalMessage(entry.Message)
			if err == nil {
				if toolResult, ok := message.(*ai.ToolResultMessage); ok {
					addSessionUsage(&stats.Tokens, &stats.Cost, toolResult.Usage)
				}
			}
		case "assistant":
			message, err := ai.UnmarshalMessage(entry.Message)
			if err != nil {
				continue
			}
			assistant, ok := message.(*ai.AssistantMessage)
			if !ok {
				continue
			}
			stats.AssistantMessages++
			for _, content := range assistant.Content {
				if _, ok := content.(*ai.ToolCall); ok {
					stats.ToolCalls++
				}
			}
			addSessionUsage(&stats.Tokens, &stats.Cost, &assistant.Usage)
		}
	}
	stats.Tokens.Total = stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
	stats.ContextUsage = runtime.GetContextUsage()
	return stats
}

func (runtime *SessionRuntime) FooterSnapshot() FooterSnapshot {
	if runtime == nil {
		return FooterSnapshot{}
	}
	display := runtime.agent.DisplayState()
	aggregate, revision := runtime.manager.AggregateStats()

	runtime.footerMu.Lock()
	contextUsage := runtime.footerContextUsage
	if runtime.footerRevision != revision || runtime.footerContextWindow != display.ContextWindow {
		contextUsage = runtime.GetContextUsage()
		runtime.footerRevision = revision
		runtime.footerContextWindow = display.ContextWindow
		runtime.footerContextUsage = contextUsage
	}
	runtime.footerMu.Unlock()

	tokens := SessionTokenTotals{
		Input:      aggregate.InputTokens,
		Output:     aggregate.OutputTokens,
		CacheRead:  aggregate.CacheReadTokens,
		CacheWrite: aggregate.CacheWriteTokens,
	}
	tokens.Total = tokens.Input + tokens.Output + tokens.CacheRead + tokens.CacheWrite
	return FooterSnapshot{
		Display: display, Tokens: tokens, Cost: aggregate.Cost, ContextUsage: contextUsage,
		LatestCacheHitRate:    aggregate.LatestCacheHitRate,
		HasLatestCacheHitRate: aggregate.HasLatestCacheHitRate,
		AutoCompactEnabled:    runtime.autoCompactionEnabled(),
	}
}

func (runtime *SessionRuntime) GetLastAssistantText() *string {
	messages := runtime.agent.State().Messages
	for index := len(messages) - 1; index >= 0; index-- {
		assistant := asAssistant(messages[index])
		if assistant == nil || assistant.StopReason == ai.StopReasonAborted && len(assistant.Content) == 0 {
			continue
		}
		var text strings.Builder
		for _, content := range assistant.Content {
			if block, ok := content.(*ai.TextContent); ok {
				text.WriteString(block.Text)
			}
		}
		value := strings.TrimFunc(text.String(), isECMAScriptTrimSpace)
		if value == "" {
			return nil
		}
		return &value
	}
	return nil
}

func isECMAScriptTrimSpace(character rune) bool {
	return character == '\ufeff' || character == '\u00a0' || character == '\u1680' ||
		character >= '\u2000' && character <= '\u200a' || character == '\u2028' || character == '\u2029' ||
		character == '\u202f' || character == '\u205f' || character == '\u3000' ||
		character == '\t' || character == '\n' || character == '\v' || character == '\f' || character == '\r' || character == ' '
}

func (runtime *SessionRuntime) SetSessionName(name string) error {
	if _, err := runtime.manager.AppendSessionInfo(name); err != nil {
		return err
	}
	runtime.emit(SessionInfoChangedEvent{Name: runtime.manager.GetSessionName()})
	return nil
}

func (runtime *SessionRuntime) ExportHTML(outputPath string) (string, error) {
	state := runtime.agent.State()
	systemPrompt := state.SystemPrompt
	themeName := runtime.settings.GetTheme()
	var exportTheme *modetheme.Theme
	configured := themeName == "dark" || themeName == "light"
	if themeName != "" && !configured {
		exportTheme = modetheme.GetTheme(themeName)
		if exportTheme == nil && runtime.resourceLoader != nil {
			for _, candidate := range runtime.resourceLoader.GetThemes().Themes {
				if candidate.Name == themeName {
					exportTheme = candidate
					break
				}
			}
		}
		configured = exportTheme != nil
	}
	if !configured {
		if current := modetheme.Current(); current != nil {
			themeName = current.Name
			exportTheme = current
		} else {
			themeName = ""
		}
	}
	// Custom extension tools are pre-rendered through their TUI renderers
	// (upstream exportToHtml's createToolHtmlRenderer).
	uiTheme := extensions.NewNoopUI().Theme()
	if runner := runtime.ExtensionRunner(); runner != nil {
		uiTheme = runner.UI().Theme()
	}
	renderer := exporthtml.NewToolHTMLRenderer(exporthtml.ToolHTMLRendererDeps{
		GetToolDefinition: runtime.GetToolDefinition,
		Theme:             uiTheme,
		CWD:               runtime.manager.GetCWD(),
	})
	return exporthtml.ExportSession(runtime.manager, exporthtml.Options{
		OutputPath:   outputPath,
		SystemPrompt: &systemPrompt,
		Tools:        exportToolList(state.Tools),
		ToolRenderer: renderer,
		ThemeName:    themeName,
		Theme:        exportTheme,
	})
}

// exportToolList mirrors upstream exportToHtml's tools mapping: name,
// description, and parameters for each active tool.
func exportToolList(tools []agent.AgentTool) json.RawMessage {
	if tools == nil {
		return nil
	}
	type exportTool struct {
		Name        string        `json:"name"`
		Description string        `json:"description"`
		Parameters  ai.JSONSchema `json:"parameters"`
	}
	list := make([]exportTool, 0, len(tools))
	for _, tool := range tools {
		spec := tool.Spec()
		list = append(list, exportTool{Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters})
	}
	encoded, err := json.Marshal(list)
	if err != nil {
		return nil
	}
	return encoded
}

// ExportJSONL writes the current root-to-leaf branch as a standalone upstream
// session file, re-chaining parentId values into a linear sequence.
func (runtime *SessionRuntime) ExportJSONL(outputPath string) (string, error) {
	if runtime == nil || runtime.manager == nil {
		return "", errors.New("codingagent: nil session runtime")
	}
	if outputPath == "" {
		timestamp := time.UnixMilli(runtime.clock()).UTC().Format("2006-01-02T15:04:05.000Z")
		outputPath = "session-" + strings.NewReplacer(":", "-", ".", "-").Replace(timestamp) + ".jsonl"
	}
	normalized, err := config.NormalizePath(outputPath)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.Abs(filepath.Clean(normalized))
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "", err
	}
	version := sessionstore.CurrentVersion
	header := sessionstore.SessionHeader{
		Type: "session", Version: &version, ID: runtime.manager.GetSessionID(),
		Timestamp: time.UnixMilli(runtime.clock()).UTC().Format("2006-01-02T15:04:05.000Z"), CWD: runtime.manager.GetCWD(),
	}
	encodedHeader, err := header.MarshalJSON()
	if err != nil {
		return "", err
	}
	var output bytes.Buffer
	output.Write(encodedHeader)
	output.WriteByte('\n')
	var previous *string
	for _, entry := range runtime.manager.GetBranch() {
		encoded, marshalErr := entry.MarshalJSONWithParent(previous)
		if marshalErr != nil {
			return "", marshalErr
		}
		output.Write(encoded)
		output.WriteByte('\n')
		id := entry.ID
		previous = &id
	}
	if err := os.WriteFile(resolved, output.Bytes(), 0o666); err != nil {
		return "", fmt.Errorf("write JSONL export: %w", err)
	}
	return resolved, nil
}
