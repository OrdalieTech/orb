package codingagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	aiapi "github.com/OrdalieTech/pi-go/ai/api"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

type SessionRuntimeConfig struct {
	Agent                  *agent.Agent
	SessionManager         *sessionstore.SessionManager
	Settings               *config.SettingsManager
	StreamFn               agent.StreamFn
	GetAPIKey              agent.GetAPIKeyFunc
	GetRequestAuth         agent.GetRequestAuthFunc
	GetModelHeaders        agent.GetModelHeadersFunc
	AvailableModels        func() []ai.Model
	ScopedModels           []ScopedModel
	Complete               harness.CompleteFunc
	Sleep                  func(context.Context, time.Duration) error
	Clock                  func() int64
	SlashResolver          *SlashResolver
	ExtensionRegistry      *extensions.Registry
	ExtensionMode          extensions.Mode
	ExtensionUI            extensions.UI
	ExtensionErrorHandler  func(extensions.ExtensionError)
	ModelRegistry          extensions.ModelRegistry
	RegisterProvider       func(extensions.Provider) error
	UnregisterProvider     func(string) error
	BaseTools              []agent.AgentTool
	InitialActiveToolNames []string
	AllowedToolNames       *[]string
	ExcludedToolNames      []string
	RebuildBaseTools       func() ([]agent.AgentTool, error)
	SystemPromptOptions    *SystemPromptOptions
	SessionStartEvent      *extensions.SessionStartEvent
	DeferExtensionStart    bool
}

type SessionRuntime struct {
	agent    *agent.Agent
	manager  *sessionstore.SessionManager
	settings *config.SettingsManager
	complete harness.CompleteFunc
	sleep    func(context.Context, time.Duration) error
	clock    func() int64

	mu                   sync.Mutex
	listeners            []sessionListener
	nextListenerID       uint64
	steering             []string
	followUps            []string
	lastAssistant        *ai.AssistantMessage
	retryAttempt         int
	overflowAttempted    bool
	retryCancel          context.CancelFunc
	compactionCancel     context.CancelFunc
	autoCompactionCancel context.CancelFunc
	branchCancel         context.CancelFunc
	bashCancel           context.CancelFunc
	pendingBash          []harness.BashExecutionMessage
	autoCompaction       bool
	autoRetry            bool
	availableModels      func() []ai.Model
	scopedModels         []ScopedModel
	getAPIKey            agent.GetAPIKeyFunc
	getRequestAuth       agent.GetRequestAuthFunc
	unsubscribeAgent     func()
	slashResolver        *SlashResolver
	baseSlashResolver    *SlashResolver
	activeRuns           int
	idleWait             chan struct{}
	extensionState       *extensionRuntimeState
	beginReload          func() error
	endReload            func()
	reloadPrepared       func() error
}

func (runtime *SessionRuntime) setReloadLifecycle(begin func() error, prepared func() error, end func()) {
	runtime.beginReload = begin
	runtime.reloadPrepared = prepared
	runtime.endReload = end
}

type sessionListener struct {
	id       uint64
	listener func(any)
}

type NavigateTreeOptions struct {
	Summarize           bool
	CustomInstructions  string
	ReplaceInstructions bool
	Label               string
}

type NavigateTreeResult struct {
	EditorText   string
	Cancelled    bool
	Aborted      bool
	SummaryEntry *sessionstore.SessionEntry
}

func NewSessionRuntime(runtimeConfig SessionRuntimeConfig) (*SessionRuntime, error) {
	if runtimeConfig.Agent == nil || runtimeConfig.SessionManager == nil || runtimeConfig.Settings == nil {
		return nil, errors.New("codingagent: session runtime requires agent, session manager, and settings")
	}
	streamFn := runtimeConfig.StreamFn
	if streamFn == nil {
		streamFn = aiapi.StreamSimple
	}
	complete := runtimeConfig.Complete
	if complete == nil {
		complete = func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			providerSettings := runtimeConfig.Settings.GetProviderRetrySettings()
			merged := ai.SimpleStreamOptions{}
			if options != nil {
				merged = *options
			}
			if merged.TimeoutMS == nil {
				merged.TimeoutMS = providerSettings.TimeoutMS
			}
			if merged.TimeoutMS == nil {
				httpIdleTimeout, err := runtimeConfig.Settings.GetHTTPIdleTimeoutMS()
				if err != nil {
					return nil, err
				}
				if httpIdleTimeout == 0 {
					httpIdleTimeout = 2147483647
				}
				merged.TimeoutMS = &httpIdleTimeout
			}
			if merged.WebSocketConnectTimeoutMS == nil {
				webSocketConnectTimeout, err := runtimeConfig.Settings.GetWebSocketConnectTimeoutMS()
				if err != nil {
					return nil, err
				}
				merged.WebSocketConnectTimeoutMS = webSocketConnectTimeout
			}
			if merged.MaxRetries == nil {
				merged.MaxRetries = providerSettings.MaxRetries
			}
			if merged.MaxRetryDelayMS == nil {
				maxDelay := providerSettings.MaxRetryDelayMS
				merged.MaxRetryDelayMS = &maxDelay
			}
			if merged.ThinkingBudgets == nil {
				merged.ThinkingBudgets = runtimeConfig.Settings.GetThinkingBudgets()
			}
			requestModel := model
			if runtimeConfig.GetRequestAuth != nil && model != nil {
				resolved, err := runtimeConfig.GetRequestAuth(ctx, model.Provider)
				if err != nil {
					return nil, err
				}
				if resolved != nil {
					if merged.APIKey == nil {
						merged.APIKey = resolved.APIKey
					}
					merged.Env = mergeSummaryEnv(resolved.Env, merged.Env)
					merged.Headers = mergeSummaryAuthHeaders(resolved.Headers, merged.Headers)
					if resolved.BaseURL != nil {
						copy := *model
						copy.BaseURL = *resolved.BaseURL
						requestModel = &copy
					}
				}
			} else if merged.APIKey == nil && runtimeConfig.GetAPIKey != nil && model != nil {
				key, err := runtimeConfig.GetAPIKey(ctx, model.Provider)
				if err != nil {
					return nil, err
				}
				merged.APIKey = key
			}
			if runtimeConfig.GetModelHeaders != nil && model != nil {
				headers, err := runtimeConfig.GetModelHeaders(ctx, requestModel, merged.APIKey, merged.Env)
				if err != nil {
					return nil, err
				}
				copy := *requestModel
				copy.Headers = mergeSummaryHeaders(requestModel.Headers, headers)
				requestModel = &copy
			}
			events, err := streamFn(ctx, requestModel, request, &merged)
			if err != nil {
				return nil, err
			}
			return ai.Collect(events)
		}
	}
	sleep := runtimeConfig.Sleep
	if sleep == nil {
		sleep = sleepContext
	}
	clock := runtimeConfig.Clock
	if clock == nil {
		clock = func() int64 { return time.Now().UnixMilli() }
	}
	runtime := &SessionRuntime{
		agent: runtimeConfig.Agent, manager: runtimeConfig.SessionManager,
		settings: runtimeConfig.Settings, complete: complete, sleep: sleep, clock: clock,
		listeners: []sessionListener{}, steering: []string{}, followUps: []string{},
		autoCompaction:  runtimeConfig.Settings.GetCompactionSettings().Enabled,
		autoRetry:       runtimeConfig.Settings.GetRetrySettings().Enabled,
		availableModels: runtimeConfig.AvailableModels, getAPIKey: runtimeConfig.GetAPIKey,
		getRequestAuth:    runtimeConfig.GetRequestAuth,
		scopedModels:      append([]ScopedModel(nil), runtimeConfig.ScopedModels...),
		slashResolver:     cloneSlashResolver(runtimeConfig.SlashResolver),
		baseSlashResolver: cloneSlashResolver(runtimeConfig.SlashResolver),
	}
	runtimeConfig.SlashResolver = runtime.slashResolver
	runtime.agent.SetSteeringMode(agent.QueueMode(runtime.settings.GetSteeringMode()))
	runtime.agent.SetFollowUpMode(agent.QueueMode(runtime.settings.GetFollowUpMode()))
	if runtimeConfig.ExtensionRegistry != nil {
		runtime.bindExtensions(runtimeConfig)
	}
	runtime.unsubscribeAgent = runtime.agent.Subscribe(runtime.handleAgentEvent)
	return runtime, nil
}

func cloneSlashResolver(resolver *SlashResolver) *SlashResolver {
	if resolver == nil {
		return nil
	}
	cloned := *resolver
	cloned.Skills = append([]Skill(nil), resolver.Skills...)
	cloned.PromptTemplates = append([]PromptTemplate(nil), resolver.PromptTemplates...)
	cloned.ExtensionCommands = append([]SlashCommandInfo(nil), resolver.ExtensionCommands...)
	return &cloned
}

func mergeSummaryHeaders(base, override *map[string]string) *map[string]string {
	if base == nil && override == nil {
		return nil
	}
	merged := make(map[string]string)
	if base != nil {
		for name, value := range *base {
			merged[name] = value
		}
	}
	if override != nil {
		for name, value := range *override {
			for existing := range merged {
				if strings.EqualFold(existing, name) {
					delete(merged, existing)
				}
			}
			merged[name] = value
		}
	}
	return &merged
}

func mergeSummaryEnv(resolved, overrides ai.ProviderEnv) ai.ProviderEnv {
	if len(resolved) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(ai.ProviderEnv, len(resolved)+len(overrides))
	for name, value := range resolved {
		merged[name] = value
	}
	for name, value := range overrides {
		merged[name] = value
	}
	return merged
}

func mergeSummaryAuthHeaders(resolved map[string]string, overrides ai.ProviderHeaders) ai.ProviderHeaders {
	if len(resolved) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(ai.ProviderHeaders, len(resolved)+len(overrides))
	for name, value := range resolved {
		copy := value
		merged[name] = &copy
	}
	for name, value := range overrides {
		for existing := range merged {
			if strings.EqualFold(existing, name) {
				delete(merged, existing)
			}
		}
		merged[name] = value
	}
	return merged
}

func (runtime *SessionRuntime) Dispose() {
	runtime.dispose(true)
}

func (runtime *SessionRuntime) disposeAfterExtensionShutdown() {
	runtime.dispose(false)
}

func (runtime *SessionRuntime) dispose(emitExtensionShutdown bool) {
	if runtime == nil {
		return
	}
	runtime.disposeExtensions(emitExtensionShutdown)
	runtime.Abort()
	runtime.AbortCompaction()
	runtime.AbortBranchSummary()
	runtime.AbortBash()
	runtime.disconnectFromAgent()
	runtime.mu.Lock()
	runtime.listeners = nil
	runtime.mu.Unlock()
	// Provider session caches are process-scoped; upstream treats teardown as best-effort.
	_ = ai.CleanupSessionResources(runtime.manager.GetSessionID())
}

func (runtime *SessionRuntime) Subscribe(listener func(any)) func() {
	if runtime == nil || listener == nil {
		return func() {}
	}
	runtime.mu.Lock()
	runtime.nextListenerID++
	id := runtime.nextListenerID
	runtime.listeners = append(runtime.listeners, sessionListener{id: id, listener: listener})
	runtime.mu.Unlock()
	return func() {
		runtime.mu.Lock()
		for index := range runtime.listeners {
			if runtime.listeners[index].id == id {
				runtime.listeners = append(runtime.listeners[:index], runtime.listeners[index+1:]...)
				break
			}
		}
		runtime.mu.Unlock()
	}
}

func (runtime *SessionRuntime) Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	if text, ok := input.(string); ok && runtime.extensionState != nil {
		return runtime.promptExtensionInput(ctx, text, images, extensions.InputInteractive, true, nil, true)
	}
	if err := runtime.PromptPreflight(ctx); err != nil {
		return err
	}
	return runtime.PromptAfterPreflight(ctx, input, images...)
}

func (runtime *SessionRuntime) PromptAfterPreflight(ctx context.Context, input any, images ...*ai.ImageContent) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	if text, ok := input.(string); ok && runtime.extensionState != nil {
		return runtime.promptExtensionInput(ctx, text, images, extensions.InputInteractive, true, nil, false)
	}
	if text, ok := input.(string); ok && runtime.slashResolver != nil {
		expanded, handled := runtime.slashResolver.ResolvePrompt(text)
		if handled {
			return nil
		}
		input = expanded
	}
	return runtime.runPolicies(ctx, func() error { return runtime.agent.Prompt(ctx, input, images...) })
}

func (runtime *SessionRuntime) Continue(ctx context.Context) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	return runtime.runPolicies(ctx, func() error { return runtime.agent.Continue(ctx) })
}

func (runtime *SessionRuntime) Steer(text string) error {
	return runtime.SteerImages(text, nil)
}

func (runtime *SessionRuntime) SteerImages(text string, images []*ai.ImageContent) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	if runtime.slashResolver != nil {
		var err error
		text, err = runtime.slashResolver.ExpandQueued(text)
		if err != nil {
			return err
		}
	}
	message := userMessageWithImagesAt(text, images, runtime.clock())
	runtime.mu.Lock()
	runtime.steering = append(runtime.steering, text)
	runtime.mu.Unlock()
	runtime.agent.Steer(message)
	runtime.emitQueueUpdate()
	return nil
}

func (runtime *SessionRuntime) FollowUp(text string) error {
	return runtime.FollowUpImages(text, nil)
}

func (runtime *SessionRuntime) FollowUpImages(text string, images []*ai.ImageContent) error {
	if runtime == nil {
		return errors.New("codingagent: nil session runtime")
	}
	if runtime.slashResolver != nil {
		var err error
		text, err = runtime.slashResolver.ExpandQueued(text)
		if err != nil {
			return err
		}
	}
	message := userMessageWithImagesAt(text, images, runtime.clock())
	runtime.mu.Lock()
	runtime.followUps = append(runtime.followUps, text)
	runtime.mu.Unlock()
	runtime.agent.FollowUp(message)
	runtime.emitQueueUpdate()
	return nil
}

func (runtime *SessionRuntime) Commands() []SlashCommandInfo {
	if runtime == nil {
		return []SlashCommandInfo{}
	}
	runtime.syncExtensionCommands()
	if runtime.slashResolver == nil {
		return []SlashCommandInfo{}
	}
	return runtime.slashResolver.Commands(runtime.settings.GetEnableSkillCommands())
}

func (runtime *SessionRuntime) State() agent.AgentState {
	if runtime == nil || runtime.agent == nil {
		return agent.AgentState{}
	}
	return runtime.agent.State()
}

func (runtime *SessionRuntime) Abort() {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	retryCancel := runtime.retryCancel
	runtime.retryCancel = nil
	runtime.mu.Unlock()
	if retryCancel != nil {
		retryCancel()
	}
	runtime.agent.Abort()
}

func (runtime *SessionRuntime) ClearQueue() QueueUpdateEvent {
	if runtime == nil {
		return QueueUpdateEvent{Steering: []string{}, FollowUp: []string{}}
	}
	runtime.mu.Lock()
	cleared := QueueUpdateEvent{
		Steering: append([]string{}, runtime.steering...),
		FollowUp: append([]string{}, runtime.followUps...),
	}
	runtime.steering = []string{}
	runtime.followUps = []string{}
	runtime.mu.Unlock()
	runtime.agent.ClearAllQueues()
	runtime.emitQueueUpdate()
	return cleared
}

func (runtime *SessionRuntime) AbortCompaction() {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	manualCancel := runtime.compactionCancel
	autoCancel := runtime.autoCompactionCancel
	runtime.mu.Unlock()
	if manualCancel != nil {
		manualCancel()
	}
	if autoCancel != nil {
		autoCancel()
	}
}

func (runtime *SessionRuntime) AbortBranchSummary() {
	if runtime == nil {
		return
	}
	runtime.mu.Lock()
	cancel := runtime.branchCancel
	runtime.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (runtime *SessionRuntime) WaitForIdle(ctx context.Context) error {
	if runtime == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.mu.Lock()
	wait := runtime.idleWait
	runtime.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return runtime.agent.WaitForIdle(ctx)
}

func (runtime *SessionRuntime) runPolicies(ctx context.Context, start func() error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !runtime.beginRun() {
		return errors.New("Agent is already processing. Specify streamingBehavior ('steer' or 'followUp') to queue the message.") //nolint:staticcheck // Upstream session error is observable.
	}
	defer func() {
		runtime.clearExtensionTurnState()
		flushErr := runtime.flushPendingBash()
		runtime.emitExtensionSettled(ctx)
		runtime.emit(AgentSettledEvent{})
		runtime.endRun()
		if err == nil && flushErr != nil {
			err = flushErr
		}
	}()
	if err := start(); err != nil {
		return err
	}
	for {
		message := runtime.takeLastAssistant()
		if message == nil {
			break
		}
		if runtime.isRetryable(message) {
			shouldContinue, err := runtime.prepareRetry(ctx, message)
			if err != nil {
				return err
			}
			if shouldContinue {
				if err := runtime.agent.Continue(ctx); err != nil {
					return err
				}
				continue
			}
		}
		if message.StopReason == ai.StopReasonError {
			runtime.mu.Lock()
			attempt := runtime.retryAttempt
			runtime.retryAttempt = 0
			runtime.mu.Unlock()
			if attempt > 0 {
				runtime.emit(AutoRetryEndEvent{Success: false, Attempt: attempt, FinalError: message.ErrorMessage})
			}
		}
		continueAfterCompaction, _ := runtime.checkCompaction(ctx, message, true)
		if continueAfterCompaction || runtime.agent.HasQueuedMessages() {
			if err := runtime.agent.Continue(ctx); err != nil {
				return err
			}
			continue
		}
		break
	}
	return nil
}

func (runtime *SessionRuntime) handleAgentEvent(ctx context.Context, event agent.AgentEvent) error {
	if start, ok := event.(agent.MessageStartEvent); ok {
		switch start.Message.(type) {
		case *ai.UserMessage, ai.UserMessage:
			runtime.mu.Lock()
			runtime.overflowAttempted = false
			runtime.mu.Unlock()
		}
		if text := userMessageText(start.Message); text != "" {
			changed := false
			runtime.mu.Lock()
			if index := indexOf(runtime.steering, text); index >= 0 {
				runtime.steering = append(runtime.steering[:index], runtime.steering[index+1:]...)
				changed = true
			} else if index := indexOf(runtime.followUps, text); index >= 0 {
				runtime.followUps = append(runtime.followUps[:index], runtime.followUps[index+1:]...)
				changed = true
			}
			runtime.mu.Unlock()
			if changed {
				runtime.emitQueueUpdate()
			}
		}
	}
	event = runtime.extensionLifecycleEvent(ctx, event)
	if ended, ok := event.(agent.AgentEndEvent); ok {
		runtime.emit(SessionAgentEndEvent{Messages: ended.Messages, WillRetry: runtime.willRetry(ended.Messages)})
	} else {
		runtime.emit(event)
	}
	ended, ok := event.(agent.MessageEndEvent)
	if !ok {
		return nil
	}
	if err := runtime.persistMessage(ended.Message); err != nil {
		return err
	}
	assistant := asAssistant(ended.Message)
	if assistant == nil {
		return nil
	}
	runtime.mu.Lock()
	runtime.lastAssistant = assistant
	if assistant.StopReason != ai.StopReasonError {
		runtime.overflowAttempted = false
	}
	attempt := runtime.retryAttempt
	if assistant.StopReason != ai.StopReasonError && attempt > 0 {
		runtime.retryAttempt = 0
	}
	runtime.mu.Unlock()
	if assistant.StopReason != ai.StopReasonError && attempt > 0 {
		runtime.emit(AutoRetryEndEvent{Success: true, Attempt: attempt})
	}
	return nil
}

func (runtime *SessionRuntime) persistMessage(message agent.AgentMessage) error {
	encoded, err := ai.Marshal(message)
	if err != nil {
		return err
	}
	var envelope struct {
		Role       json.RawMessage `json:"role"`
		CustomType json.RawMessage `json:"customType"`
		Content    json.RawMessage `json:"content"`
		Display    bool            `json:"display"`
		Details    json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return err
	}
	role, err := jsonwire.UnmarshalString(bytes.TrimSpace(envelope.Role))
	if err != nil {
		return err
	}
	switch role {
	case "user", "assistant", "toolResult":
		_, err = runtime.manager.AppendMessage(message)
		return err
	case "custom":
		customType, decodeErr := jsonwire.UnmarshalString(bytes.TrimSpace(envelope.CustomType))
		if decodeErr != nil {
			return decodeErr
		}
		if len(envelope.Content) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Content), []byte("null")) {
			envelope.Content = json.RawMessage("[]")
		}
		if len(envelope.Details) > 0 {
			_, err = runtime.manager.AppendCustomMessageEntry(customType, envelope.Content, envelope.Display, envelope.Details)
		} else {
			_, err = runtime.manager.AppendCustomMessageEntry(customType, envelope.Content, envelope.Display)
		}
		return err
	default:
		return nil
	}
}

func (runtime *SessionRuntime) prepareRetry(ctx context.Context, message *ai.AssistantMessage) (bool, error) {
	settings := runtime.settings.GetRetrySettings()
	if !runtime.autoRetryEnabled() {
		return false, nil
	}
	runtime.mu.Lock()
	runtime.retryAttempt++
	attempt := runtime.retryAttempt
	if attempt > settings.MaxRetries {
		runtime.retryAttempt--
		runtime.mu.Unlock()
		return false, nil
	}
	delay := settings.BaseDelayMS * int64(1<<(attempt-1))
	retryContext, cancel := context.WithCancel(ctx)
	runtime.retryCancel = cancel
	runtime.mu.Unlock()
	errorMessage := "Unknown error"
	if message.ErrorMessage != nil {
		errorMessage = *message.ErrorMessage
	}
	runtime.emit(AutoRetryStartEvent{Attempt: attempt, MaxAttempts: settings.MaxRetries, DelayMS: delay, ErrorMessage: errorMessage})
	runtime.dropLastAssistant()
	err := runtime.sleep(retryContext, time.Duration(delay)*time.Millisecond)
	cancel()
	runtime.mu.Lock()
	runtime.retryCancel = nil
	runtime.mu.Unlock()
	if err != nil {
		finalError := "Retry cancelled"
		runtime.mu.Lock()
		runtime.retryAttempt = 0
		runtime.mu.Unlock()
		runtime.emit(AutoRetryEndEvent{Success: false, Attempt: attempt, FinalError: &finalError})
		return false, nil
	}
	return true, nil
}

func (runtime *SessionRuntime) willRetry(messages agent.AgentMessages) bool {
	settings := runtime.settings.GetRetrySettings()
	runtime.mu.Lock()
	attempt := runtime.retryAttempt
	runtime.mu.Unlock()
	if !runtime.autoRetryEnabled() || attempt >= settings.MaxRetries {
		return false
	}
	for index := len(messages) - 1; index >= 0; index-- {
		if assistant := asAssistant(messages[index]); assistant != nil {
			return runtime.isRetryable(assistant)
		}
	}
	return false
}

func (runtime *SessionRuntime) isRetryable(message *ai.AssistantMessage) bool {
	state := runtime.agent.State()
	contextWindow := float64(0)
	if state.Model != nil {
		contextWindow = state.Model.ContextWindow
	}
	return !harness.IsContextOverflow(message, contextWindow) && harness.IsRetryableAssistantError(message)
}

func (runtime *SessionRuntime) checkCompaction(ctx context.Context, message *ai.AssistantMessage, skipAbortedCheck bool) (bool, error) {
	settings := runtime.settings.GetCompactionSettings()
	if !runtime.autoCompactionEnabled() || (skipAbortedCheck && message.StopReason == ai.StopReasonAborted) {
		return false, nil
	}
	state := runtime.agent.State()
	if state.Model == nil {
		return false, nil
	}
	latest := sessionstore.GetLatestCompactionEntry(runtime.manager.GetBranch())
	if latest != nil && message.Timestamp <= parseSessionTimestamp(latest.Timestamp) {
		return false, nil
	}
	sameModel := string(message.Provider) == string(state.Model.Provider) && message.Model == state.Model.ID
	if sameModel && harness.IsContextOverflow(message, state.Model.ContextWindow) {
		willRetry := message.StopReason != ai.StopReasonStop
		if willRetry {
			runtime.mu.Lock()
			alreadyAttempted := runtime.overflowAttempted
			if !alreadyAttempted {
				runtime.overflowAttempted = true
			}
			runtime.mu.Unlock()
			if alreadyAttempted {
				errorMessage := "Context overflow recovery failed after one compact-and-retry attempt. Try reducing context or switching to a larger-context model."
				runtime.emit(CompactionEndEvent{Reason: "overflow", ErrorMessage: &errorMessage})
				return false, nil
			}
			runtime.dropLastAssistant()
		}
		return runtime.runAutoCompaction(ctx, "overflow", willRetry)
	}
	direct := harness.CalculateContextTokens(message.Usage)
	contextTokens := direct
	if message.StopReason == ai.StopReasonError || direct == 0 {
		estimate := harness.EstimateContextTokens(state.Messages)
		if estimate.LastUsageIndex == nil {
			return false, nil
		}
		if latest != nil {
			usageMessage := asAssistant(state.Messages[*estimate.LastUsageIndex])
			if usageMessage != nil && usageMessage.Timestamp <= parseSessionTimestamp(latest.Timestamp) {
				return false, nil
			}
		}
		contextTokens = estimate.Tokens
	}
	if harness.ShouldCompact(contextTokens, state.Model.ContextWindow, harness.CompactionSettings{
		Enabled: runtime.autoCompactionEnabled(), ReserveTokens: settings.ReserveTokens, KeepRecentTokens: settings.KeepRecentTokens,
	}) {
		return runtime.runAutoCompaction(ctx, "threshold", false)
	}
	return false, nil
}

func (runtime *SessionRuntime) runAutoCompaction(ctx context.Context, reason string, willRetry bool) (bool, error) {
	settings := runtime.settings.GetCompactionSettings()
	branch := runtime.manager.GetBranch()
	preparation, err := harness.PrepareCompaction(projectSessionEntries(branch), harness.CompactionSettings{
		Enabled: runtime.autoCompactionEnabled(), ReserveTokens: settings.ReserveTokens, KeepRecentTokens: settings.KeepRecentTokens,
	})
	if err != nil || preparation == nil {
		return false, err
	}
	runtime.emit(CompactionStartEvent{Reason: reason})
	compactionContext, cancel := context.WithCancel(ctx)
	runtime.mu.Lock()
	runtime.autoCompactionCancel = cancel
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		runtime.autoCompactionCancel = nil
		runtime.mu.Unlock()
	}()
	result, fromExtension, extensionCancelled := runtime.beforeExtensionCompaction(
		compactionContext, preparation, branch, nil, extensions.CompactionReason(reason), willRetry,
	)
	var compactErr error
	if extensionCancelled {
		compactErr = context.Canceled
	} else if result == nil {
		result, compactErr = harness.Compact(compactionContext, preparation, runtime.agent.State().Model, runtime.complete, "", runtime.agent.State().ThinkingLevel)
	}
	wasCancelled := compactionContext.Err() != nil
	if compactErr == nil && wasCancelled {
		compactErr = context.Canceled
	}
	if compactErr != nil {
		if extensionCancelled || errors.Is(compactErr, context.Canceled) || wasCancelled {
			runtime.emit(CompactionEndEvent{Reason: reason, Aborted: true})
			return false, nil
		}
		message := "Auto-compaction failed: " + compactErr.Error()
		if reason == "overflow" {
			message = "Context overflow recovery failed: " + compactErr.Error()
		}
		runtime.emit(CompactionEndEvent{Reason: reason, ErrorMessage: &message})
		return false, nil
	}
	fields := sessionstore.OptionalEntryFields{Details: result.Details, HasDetails: true}
	if runtime.extensionState != nil {
		fields.FromHook = &fromExtension
	}
	entryID, err := runtime.manager.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, fields)
	if err != nil {
		message := "Auto-compaction failed: " + err.Error()
		runtime.emit(CompactionEndEvent{Reason: reason, ErrorMessage: &message})
		return false, err
	}
	runtime.syncAgentMessages()
	runtime.emitExtensionCompaction(compactionContext, entryID, fromExtension, extensions.CompactionReason(reason), willRetry)
	result.EstimatedTokensAfter = estimateAllTokens(runtime.agent.State().Messages)
	runtime.emit(CompactionEndEvent{Reason: reason, Result: result, WillRetry: willRetry})
	if willRetry {
		state := runtime.agent.State()
		if len(state.Messages) > 0 {
			lastAssistant := asAssistant(state.Messages[len(state.Messages)-1])
			if lastAssistant != nil && lastAssistant.StopReason == ai.StopReasonError {
				runtime.dropLastAssistant()
			}
		}
		return true, nil
	}
	return runtime.agent.HasQueuedMessages(), nil
}

//nolint:staticcheck // User-visible compaction errors match upstream capitalization.
func (runtime *SessionRuntime) Compact(ctx context.Context, customInstructions string) (*harness.CompactionResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	runtime.disconnectFromAgent()
	defer runtime.reconnectToAgent()
	runtime.Abort()
	if err := runtime.WaitForIdle(ctx); err != nil {
		return nil, err
	}
	compactionContext, cancel := context.WithCancel(ctx)
	runtime.mu.Lock()
	runtime.compactionCancel = cancel
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		runtime.compactionCancel = nil
		runtime.mu.Unlock()
	}()
	runtime.emit(CompactionStartEvent{Reason: "manual"})
	if runtime.agent.State().Model == nil {
		err := noModelSelectedError()
		message := "Compaction failed: " + err.Error()
		runtime.emit(CompactionEndEvent{Reason: "manual", ErrorMessage: &message})
		return nil, err
	}
	settings := runtime.settings.GetCompactionSettings()
	branch := runtime.manager.GetBranch()
	preparation, err := harness.PrepareCompaction(projectSessionEntries(branch), harness.CompactionSettings{
		Enabled: settings.Enabled, ReserveTokens: settings.ReserveTokens, KeepRecentTokens: settings.KeepRecentTokens,
	})
	if err != nil || preparation == nil {
		if err == nil {
			if len(branch) > 0 && branch[len(branch)-1].Type == "compaction" {
				err = errors.New("Already compacted")
			} else {
				err = errors.New("Nothing to compact (session too small)")
			}
		}
		message := "Compaction failed: " + err.Error()
		runtime.emit(CompactionEndEvent{Reason: "manual", ErrorMessage: &message})
		return nil, err
	}
	var customInstructionsValue *string
	if customInstructions != "" {
		customInstructionsValue = &customInstructions
	}
	result, fromExtension, extensionCancelled := runtime.beforeExtensionCompaction(
		compactionContext, preparation, branch, customInstructionsValue, extensions.CompactionManual, false,
	)
	if extensionCancelled {
		err = errors.New("Compaction cancelled")
	} else if result == nil {
		result, err = harness.Compact(compactionContext, preparation, runtime.agent.State().Model, runtime.complete, customInstructions, runtime.agent.State().ThinkingLevel)
	}
	if err == nil && compactionContext.Err() != nil {
		runtime.emit(CompactionEndEvent{Reason: "manual", Aborted: true})
		return nil, errors.New("Compaction cancelled")
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || compactionContext.Err() != nil || err.Error() == "Compaction cancelled" {
			runtime.emit(CompactionEndEvent{Reason: "manual", Aborted: true})
			return nil, err
		}
		message := "Compaction failed: " + err.Error()
		runtime.emit(CompactionEndEvent{Reason: "manual", ErrorMessage: &message})
		return nil, err
	}
	fields := sessionstore.OptionalEntryFields{Details: result.Details, HasDetails: true}
	if runtime.extensionState != nil {
		fields.FromHook = &fromExtension
	}
	entryID, err := runtime.manager.AppendCompaction(result.Summary, result.FirstKeptEntryID, result.TokensBefore, fields)
	if err != nil {
		message := "Compaction failed: " + err.Error()
		runtime.emit(CompactionEndEvent{Reason: "manual", ErrorMessage: &message})
		return nil, err
	}
	runtime.syncAgentMessages()
	runtime.emitExtensionCompaction(compactionContext, entryID, fromExtension, extensions.CompactionManual, false)
	result.EstimatedTokensAfter = estimateAllTokens(runtime.agent.State().Messages)
	runtime.emit(CompactionEndEvent{Reason: "manual", Result: result})
	return result, nil
}

func (runtime *SessionRuntime) beforeExtensionCompaction(
	ctx context.Context,
	preparation *harness.CompactionPreparation,
	branch []sessionstore.SessionEntry,
	customInstructions *string,
	reason extensions.CompactionReason,
	willRetry bool,
) (*harness.CompactionResult, bool, bool) {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventSessionBeforeCompact) {
		return nil, false, false
	}
	raw := state.runner.Emit(ctx, extensions.SessionBeforeCompactEvent{
		Preparation: *preparation, BranchEntries: branch, CustomInstructions: customInstructions,
		Reason: reason, WillRetry: willRetry, Signal: ctx,
	})
	var result *extensions.SessionBeforeCompactResult
	switch value := raw.(type) {
	case extensions.SessionBeforeCompactResult:
		result = &value
	case *extensions.SessionBeforeCompactResult:
		result = value
	}
	if result == nil {
		return nil, false, false
	}
	if result.Cancel {
		return nil, false, true
	}
	if result.Compaction == nil {
		return nil, false, false
	}
	copy := *result.Compaction
	return &copy, true, false
}

func (runtime *SessionRuntime) emitExtensionCompaction(
	ctx context.Context,
	entryID string,
	fromExtension bool,
	reason extensions.CompactionReason,
	willRetry bool,
) {
	state := runtime.extensionState
	if state == nil || state.runner == nil || !state.runner.HasHandlers(extensions.EventSessionCompact) {
		return
	}
	entry := runtime.manager.GetEntry(entryID)
	if entry == nil {
		return
	}
	state.runner.Emit(ctx, extensions.SessionCompactEvent{
		CompactionEntry: *entry, FromExtension: fromExtension, Reason: reason, WillRetry: willRetry,
	})
}

func (runtime *SessionRuntime) GetContextUsage() *harness.ContextUsage {
	state := runtime.agent.State()
	if state.Model == nil || state.Model.ContextWindow <= 0 {
		return nil
	}
	branch := runtime.manager.GetBranch()
	if latest := sessionstore.GetLatestCompactionEntry(branch); latest != nil {
		compactionIndex := -1
		for index := range branch {
			if branch[index].ID == latest.ID {
				compactionIndex = index
			}
		}
		hasUsage := false
		for index := len(branch) - 1; index > compactionIndex; index-- {
			if branch[index].Type != "message" {
				continue
			}
			message := decodeSessionMessage(branch[index].Message)
			if assistant := asAssistant(message); assistant != nil && assistant.StopReason != ai.StopReasonAborted && assistant.StopReason != ai.StopReasonError && harness.CalculateContextTokens(assistant.Usage) > 0 {
				hasUsage = true
				break
			}
		}
		if !hasUsage {
			return &harness.ContextUsage{ContextWindow: state.Model.ContextWindow}
		}
	}
	estimate := harness.EstimateContextTokens(state.Messages)
	tokens := estimate.Tokens
	percent := float64(tokens) / state.Model.ContextWindow * 100
	return &harness.ContextUsage{Tokens: &tokens, ContextWindow: state.Model.ContextWindow, Percent: &percent}
}

//nolint:staticcheck // SessionError text matches upstream capitalization.
func (runtime *SessionRuntime) NavigateTree(ctx context.Context, targetID string, options NavigateTreeOptions) (NavigateTreeResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	oldLeaf := runtime.manager.GetLeafID()
	if oldLeaf != nil && *oldLeaf == targetID {
		return NavigateTreeResult{}, nil
	}
	target := runtime.manager.GetEntry(targetID)
	if target == nil {
		return NavigateTreeResult{}, fmt.Errorf("Entry %s not found", targetID)
	}
	entries := runtime.manager.GetEntries()
	collected, err := harness.CollectEntriesForBranchSummary(projectSessionEntries(entries), oldLeaf, targetID)
	if err != nil {
		return NavigateTreeResult{}, err
	}
	branchContext, cancel := context.WithCancel(ctx)
	runtime.mu.Lock()
	runtime.branchCancel = cancel
	runtime.mu.Unlock()
	defer func() {
		cancel()
		runtime.mu.Lock()
		runtime.branchCancel = nil
		runtime.mu.Unlock()
	}()

	var summary string
	var details any = harness.BranchSummaryDetails{}
	fromExtension := false
	customInstructions := options.CustomInstructions
	replaceInstructions := options.ReplaceInstructions
	label := options.Label
	if state := runtime.extensionState; state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventSessionBeforeTree) {
		entriesByID := make(map[string]sessionstore.SessionEntry, len(entries))
		for _, entry := range entries {
			entriesByID[entry.ID] = entry
		}
		entriesToSummarize := make([]sessionstore.SessionEntry, 0, len(collected.Entries))
		for _, entry := range collected.Entries {
			entriesToSummarize = append(entriesToSummarize, entriesByID[entry.ID])
		}
		var customInstructionsValue *string
		if customInstructions != "" {
			customInstructionsValue = &customInstructions
		}
		var labelValue *string
		if label != "" {
			labelValue = &label
		}
		raw := state.runner.Emit(branchContext, extensions.SessionBeforeTreeEvent{
			Preparation: extensions.TreePreparation{
				TargetID: targetID, OldLeafID: oldLeaf, CommonAncestorID: collected.CommonAncestorID,
				EntriesToSummarize: entriesToSummarize, UserWantsSummary: options.Summarize,
				CustomInstructions: customInstructionsValue, ReplaceInstructions: replaceInstructions, Label: labelValue,
			},
			Signal: branchContext,
		})
		var hookResult *extensions.SessionBeforeTreeResult
		switch value := raw.(type) {
		case extensions.SessionBeforeTreeResult:
			hookResult = &value
		case *extensions.SessionBeforeTreeResult:
			hookResult = value
		}
		if hookResult != nil {
			if hookResult.Cancel {
				return NavigateTreeResult{Cancelled: true}, nil
			}
			if hookResult.Summary != nil && options.Summarize {
				summary = hookResult.Summary.Summary
				details = hookResult.Summary.Details
				fromExtension = true
			}
			if hookResult.CustomInstructions != nil {
				customInstructions = *hookResult.CustomInstructions
			}
			if hookResult.ReplaceInstructions != nil {
				replaceInstructions = *hookResult.ReplaceInstructions
			}
			if hookResult.Label != nil {
				label = *hookResult.Label
			}
		}
	}
	if options.Summarize && len(collected.Entries) > 0 && !fromExtension {
		settings := runtime.settings.GetBranchSummarySettings()
		result, summaryErr := harness.GenerateBranchSummary(branchContext, collected.Entries, harness.GenerateBranchSummaryOptions{
			Model: runtime.agent.State().Model, Complete: runtime.complete,
			CustomInstructions: customInstructions, ReplaceInstructions: replaceInstructions,
			ReserveTokens: &settings.ReserveTokens,
		})
		if summaryErr != nil {
			if errors.Is(summaryErr, context.Canceled) || branchContext.Err() != nil {
				return NavigateTreeResult{Cancelled: true, Aborted: true}, nil
			}
			return NavigateTreeResult{}, summaryErr
		}
		summary = result.Summary
		details = harness.BranchSummaryDetails{ReadFiles: result.ReadFiles, ModifiedFiles: result.ModifiedFiles}
	}

	newLeaf := &targetID
	editorText := ""
	switch target.Type {
	case "message":
		var envelope struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(target.Message, &envelope) == nil && envelope.Role == "user" {
			newLeaf = target.ParentID
			editorText = decodeUserContentText(envelope.Content)
		}
	case "custom_message":
		newLeaf = target.ParentID
		editorText = decodeUserContentText(target.Content)
	}
	var summaryEntry *sessionstore.SessionEntry
	if summary != "" {
		fields := sessionstore.OptionalEntryFields{Details: details, HasDetails: true}
		if runtime.extensionState != nil {
			fields.FromHook = &fromExtension
		}
		summaryID, branchErr := runtime.manager.BranchWithSummary(newLeaf, summary, fields)
		if branchErr != nil {
			return NavigateTreeResult{}, branchErr
		}
		summaryEntry = runtime.manager.GetEntry(summaryID)
		if label != "" {
			if _, labelErr := runtime.manager.AppendLabelChange(summaryID, &label); labelErr != nil {
				return NavigateTreeResult{}, labelErr
			}
		}
	} else if newLeaf == nil {
		runtime.manager.ResetLeaf()
	} else if err := runtime.manager.Branch(*newLeaf); err != nil {
		return NavigateTreeResult{}, err
	}
	if label != "" && summary == "" {
		if _, err := runtime.manager.AppendLabelChange(targetID, &label); err != nil {
			return NavigateTreeResult{}, err
		}
	}
	runtime.syncAgentMessages()
	if state := runtime.extensionState; state != nil && state.runner != nil && state.runner.HasHandlers(extensions.EventSessionTree) {
		var fromExtensionValue *bool
		if summary != "" {
			fromExtensionValue = &fromExtension
		}
		state.runner.Emit(branchContext, extensions.SessionTreeEvent{
			NewLeafID: runtime.manager.GetLeafID(), OldLeafID: oldLeaf, SummaryEntry: summaryEntry, FromExtension: fromExtensionValue,
		})
	}
	return NavigateTreeResult{EditorText: editorText, SummaryEntry: summaryEntry}, nil
}

func (runtime *SessionRuntime) emitQueueUpdate() {
	runtime.mu.Lock()
	event := QueueUpdateEvent{Steering: append([]string{}, runtime.steering...), FollowUp: append([]string{}, runtime.followUps...)}
	runtime.mu.Unlock()
	runtime.emit(event)
}

func (runtime *SessionRuntime) emit(event any) {
	runtime.mu.Lock()
	listeners := make([]func(any), 0, len(runtime.listeners))
	for _, entry := range runtime.listeners {
		listeners = append(listeners, entry.listener)
	}
	runtime.mu.Unlock()
	for _, listener := range listeners {
		listener(event)
	}
}

func (runtime *SessionRuntime) disconnectFromAgent() {
	runtime.mu.Lock()
	unsubscribe := runtime.unsubscribeAgent
	runtime.unsubscribeAgent = nil
	runtime.mu.Unlock()
	if unsubscribe != nil {
		unsubscribe()
	}
}

func (runtime *SessionRuntime) reconnectToAgent() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.unsubscribeAgent == nil {
		runtime.unsubscribeAgent = runtime.agent.Subscribe(runtime.handleAgentEvent)
	}
}

func (runtime *SessionRuntime) beginRun() bool {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.activeRuns != 0 {
		return false
	}
	runtime.idleWait = make(chan struct{})
	runtime.activeRuns = 1
	return true
}

func (runtime *SessionRuntime) endRun() {
	runtime.mu.Lock()
	if runtime.activeRuns > 0 {
		runtime.activeRuns--
	}
	if runtime.activeRuns == 0 && runtime.idleWait != nil {
		close(runtime.idleWait)
		runtime.idleWait = nil
	}
	runtime.mu.Unlock()
}

func (runtime *SessionRuntime) takeLastAssistant() *ai.AssistantMessage {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	message := runtime.lastAssistant
	runtime.lastAssistant = nil
	return message
}

func (runtime *SessionRuntime) dropLastAssistant() {
	state := runtime.agent.State()
	if len(state.Messages) > 0 && asAssistant(state.Messages[len(state.Messages)-1]) != nil {
		runtime.agent.SetMessages(state.Messages[:len(state.Messages)-1])
	}
}

func (runtime *SessionRuntime) syncAgentMessages() {
	context := runtime.manager.BuildSessionContext()
	messages := make(agent.AgentMessages, 0, len(context.Messages))
	for _, raw := range context.Messages {
		messages = append(messages, decodeSessionMessage(raw))
	}
	runtime.agent.SetMessages(messages)
}

func projectSessionEntries(entries []sessionstore.SessionEntry) []harness.SessionEntry {
	projected := make([]harness.SessionEntry, 0, len(entries))
	for _, entry := range entries {
		fromHook := entry.FromHook != nil && *entry.FromHook
		var content any
		if len(entry.Content) > 0 {
			_ = json.Unmarshal(entry.Content, &content)
		}
		var details any
		if len(entry.Details) > 0 {
			_ = json.Unmarshal(entry.Details, &details)
		}
		projected = append(projected, harness.SessionEntry{
			Type: entry.Type, ID: entry.ID, ParentID: entry.ParentID, Timestamp: entry.Timestamp,
			Message: decodeSessionMessage(entry.Message), Summary: entry.Summary,
			FirstKeptEntryID: entry.FirstKeptEntryID, TokensBefore: entry.TokensBefore,
			Details: details, FromHook: fromHook, FromID: entry.FromID,
			CustomType: entry.CustomType, Content: content, Display: entry.Display,
		})
	}
	return projected
}

func decodeSessionMessage(raw json.RawMessage) agent.AgentMessage {
	if len(raw) == 0 {
		return nil
	}
	if message, err := ai.UnmarshalMessage(raw); err == nil {
		return message
	}
	return append(json.RawMessage(nil), raw...)
}

func asAssistant(message agent.AgentMessage) *ai.AssistantMessage {
	switch typed := message.(type) {
	case *ai.AssistantMessage:
		copy := *typed
		return &copy
	case ai.AssistantMessage:
		copy := typed
		return &copy
	case json.RawMessage:
		var envelope struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(typed, &envelope) == nil && envelope.Role == "assistant" {
			var assistant ai.AssistantMessage
			if json.Unmarshal(typed, &assistant) == nil {
				return &assistant
			}
		}
	}
	return nil
}

func userMessage(text string) *ai.UserMessage {
	return userMessageWithImages(text, nil)
}

func userMessageWithImages(text string, images []*ai.ImageContent) *ai.UserMessage {
	return userMessageWithImagesAt(text, images, time.Now().UnixMilli())
}

func userMessageWithImagesAt(text string, images []*ai.ImageContent, timestamp int64) *ai.UserMessage {
	blocks := ai.UserContentBlocks{&ai.TextContent{Text: text}}
	for _, image := range images {
		if image != nil {
			copy := *image
			blocks = append(blocks, &copy)
		}
	}
	return &ai.UserMessage{Content: ai.NewUserContent(blocks...), Timestamp: timestamp}
}

func userMessageText(message agent.AgentMessage) string {
	user, ok := message.(*ai.UserMessage)
	if !ok {
		return ""
	}
	if user.Content.Text != nil {
		return *user.Content.Text
	}
	var result bytes.Buffer
	for _, block := range user.Content.Blocks {
		if text, ok := block.(*ai.TextContent); ok {
			result.WriteString(text.Text)
		}
	}
	return result.String()
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func decodeUserContentText(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return ""
	}
	if trimmed[0] == '"' {
		text, _ := jsonwire.UnmarshalString(trimmed)
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(trimmed, &blocks) != nil {
		return ""
	}
	var result bytes.Buffer
	for _, block := range blocks {
		if block.Type == "text" {
			result.WriteString(block.Text)
		}
	}
	return result.String()
}

func estimateAllTokens(messages agent.AgentMessages) int64 {
	var total int64
	for _, message := range messages {
		total += harness.EstimateTokens(message)
	}
	return total
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func parseSessionTimestamp(value string) int64 {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return 0
	}
	return parsed.UnixMilli()
}

func (runtime *SessionRuntime) String() string {
	return fmt.Sprintf("SessionRuntime(%s)", runtime.manager.GetSessionID())
}
