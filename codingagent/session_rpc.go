package codingagent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/codingagent/session/exporthtml"
	"github.com/OrdalieTech/pi-go/codingagent/tools"
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
	if state.Model == nil {
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
		return errors.New("No API key found for " + string(state.Model.Provider) + ".\n\nUse /login to log into a provider via OAuth or API key. See:\n  docs/providers.md\n  docs/models.md") //nolint:staticcheck // Upstream RPC text.
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
	return runtime.setModel(ctx, model, nil, true)
}

func (runtime *SessionRuntime) setModel(
	ctx context.Context,
	model ai.Model,
	explicitThinking *ai.ModelThinkingLevel,
	checkAuth bool,
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
	thinkingLevel := runtime.thinkingLevelForModelSwitch(state, explicitThinking)
	runtime.agent.SetModel(&model)
	if _, err := runtime.manager.AppendModelChange(string(model.Provider), model.ID); err != nil {
		return err
	}
	runtime.settings.SetDefaultModelAndProvider(string(model.Provider), model.ID)
	return runtime.SetThinkingLevel(thinkingLevel)
}

func (runtime *SessionRuntime) CycleModel(ctx context.Context) (*ModelCycleResult, error) {
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
		next := models[(index+1)%len(models)]
		if err := runtime.setModel(ctx, next.Model, next.ThinkingLevel, false); err != nil {
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
	next := models[(index+1)%len(models)]
	if err := runtime.SetModel(ctx, next); err != nil {
		return nil, err
	}
	return &ModelCycleResult{Model: next, ThinkingLevel: runtime.agent.State().ThinkingLevel}, nil
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
	effective := clampThinkingLevel(state.Model, level)
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
	return nil
}

func (runtime *SessionRuntime) CycleThinkingLevel() (*ai.ModelThinkingLevel, error) {
	state := runtime.agent.State()
	levels := supportedThinkingLevels(state.Model)
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

func supportedThinkingLevels(model *ai.Model) []ai.ModelThinkingLevel {
	all := []ai.ModelThinkingLevel{
		ai.ModelThinkingOff, ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium,
		ai.ModelThinkingHigh, ai.ModelThinkingXHigh, ai.ModelThinkingMax,
	}
	if model == nil {
		return all
	}
	if !model.Reasoning {
		return []ai.ModelThinkingLevel{ai.ModelThinkingOff}
	}
	result := make([]ai.ModelThinkingLevel, 0, len(all))
	for _, level := range all {
		present := false
		var value *string
		if model.ThinkingLevelMap != nil {
			value, present = (*model.ThinkingLevelMap)[level]
		}
		if present && value == nil {
			continue
		}
		if (level == ai.ModelThinkingXHigh || level == ai.ModelThinkingMax) && !present {
			continue
		}
		result = append(result, level)
	}
	return result
}

//nolint:staticcheck // User-visible auth guidance matches upstream.
func noModelSelectedError() error {
	return errors.New("No model selected.\n\nUse /login to log into a provider via OAuth or API key. See:\n  docs/providers.md\n  docs/models.md\n\nThen use /model to select a model.")
}

func clampThinkingLevel(model *ai.Model, requested ai.ModelThinkingLevel) ai.ModelThinkingLevel {
	levels := supportedThinkingLevels(model)
	if slices.Contains(levels, requested) {
		return requested
	}
	all := []ai.ModelThinkingLevel{
		ai.ModelThinkingOff, ai.ModelThinkingMinimal, ai.ModelThinkingLow, ai.ModelThinkingMedium,
		ai.ModelThinkingHigh, ai.ModelThinkingXHigh, ai.ModelThinkingMax,
	}
	index := slices.Index(all, requested)
	if index < 0 {
		return levels[0]
	}
	for candidate := index; candidate < len(all); candidate++ {
		if slices.Contains(levels, all[candidate]) {
			return all[candidate]
		}
	}
	for candidate := index - 1; candidate >= 0; candidate-- {
		if slices.Contains(levels, all[candidate]) {
			return all[candidate]
		}
	}
	return levels[0]
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
		role, text := messageRoleAndText(entry.Message)
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
			stats.Tokens.Input += assistant.Usage.Input
			stats.Tokens.Output += assistant.Usage.Output
			stats.Tokens.CacheRead += assistant.Usage.CacheRead
			stats.Tokens.CacheWrite += assistant.Usage.CacheWrite
			stats.Cost += assistant.Usage.Cost.Total
		}
	}
	stats.Tokens.Total = stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
	stats.ContextUsage = runtime.GetContextUsage()
	return stats
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
	return exporthtml.ExportSession(runtime.manager, exporthtml.Options{OutputPath: outputPath, SystemPrompt: &systemPrompt})
}

func messageRoleAndText(raw json.RawMessage) (string, string) {
	var message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return "", ""
	}
	var plain string
	if json.Unmarshal(message.Content, &plain) == nil {
		return message.Role, plain
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(message.Content, &blocks) != nil {
		return message.Role, ""
	}
	var text strings.Builder
	for _, block := range blocks {
		if block.Type == "text" {
			text.WriteString(block.Text)
		}
	}
	return message.Role, text.String()
}
