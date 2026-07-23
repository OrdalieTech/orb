package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

const alreadyPromptingMessage = "Agent is already processing a prompt. Use Steer() or FollowUp() to queue messages, or wait for completion."

type AgentOption func(*agentOptions)

type PrepareNextTurnWithoutContextFunc func(context.Context) (*AgentLoopTurnUpdate, error)

type agentOptions struct {
	initialState               *AgentState
	convertToLLM               ConvertToLLMFunc
	transformContext           TransformContextFunc
	streamFn                   StreamFn
	getAPIKey                  GetAPIKeyFunc
	getRequestAuth             GetRequestAuthFunc
	getModelHeaders            GetModelHeadersFunc
	beforeToolCall             BeforeToolCallFunc
	afterToolCall              AfterToolCallFunc
	prepareNextTurn            PrepareNextTurnWithoutContextFunc
	prepareNextTurnWithContext PrepareNextTurnFunc
	shouldStopAfterTurn        ShouldStopAfterTurnFunc
	getSteeringMessages        GetQueuedMessagesFunc
	getFollowUpMessages        GetQueuedMessagesFunc
	steeringMode               QueueMode
	followUpMode               QueueMode
	streamOptions              ai.SimpleStreamOptions
	toolExecution              ToolExecutionMode
	now                        func() int64
}

func WithInitialState(state AgentState) AgentOption {
	return func(options *agentOptions) {
		copy := copyAgentState(state)
		options.initialState = &copy
	}
}

func WithConvertToLLM(convert ConvertToLLMFunc) AgentOption {
	return func(options *agentOptions) { options.convertToLLM = convert }
}

func WithTransformContext(transform TransformContextFunc) AgentOption {
	return func(options *agentOptions) { options.transformContext = transform }
}

func WithAPIKeyResolver(resolve GetAPIKeyFunc) AgentOption {
	return func(options *agentOptions) { options.getAPIKey = resolve }
}

func WithRequestAuthResolver(resolve GetRequestAuthFunc) AgentOption {
	return func(options *agentOptions) { options.getRequestAuth = resolve }
}

func WithModelHeadersResolver(resolve GetModelHeadersFunc) AgentOption {
	return func(options *agentOptions) { options.getModelHeaders = resolve }
}

func WithBeforeToolCall(hook BeforeToolCallFunc) AgentOption {
	return func(options *agentOptions) { options.beforeToolCall = hook }
}

func WithAfterToolCall(hook AfterToolCallFunc) AgentOption {
	return func(options *agentOptions) { options.afterToolCall = hook }
}

func WithPrepareNextTurn(hook PrepareNextTurnWithoutContextFunc) AgentOption {
	return func(options *agentOptions) { options.prepareNextTurn = hook }
}

func WithPrepareNextTurnContext(hook PrepareNextTurnFunc) AgentOption {
	return func(options *agentOptions) { options.prepareNextTurnWithContext = hook }
}

func WithShouldStopAfterTurn(hook ShouldStopAfterTurnFunc) AgentOption {
	return func(options *agentOptions) { options.shouldStopAfterTurn = hook }
}

func WithGetSteeringMessages(getter GetQueuedMessagesFunc) AgentOption {
	return func(options *agentOptions) { options.getSteeringMessages = getter }
}

func WithGetFollowUpMessages(getter GetQueuedMessagesFunc) AgentOption {
	return func(options *agentOptions) { options.getFollowUpMessages = getter }
}

func WithSteeringMode(mode QueueMode) AgentOption {
	return func(options *agentOptions) { options.steeringMode = mode }
}

func WithFollowUpMode(mode QueueMode) AgentOption {
	return func(options *agentOptions) { options.followUpMode = mode }
}

func WithSimpleStreamOptions(streamOptions ai.SimpleStreamOptions) AgentOption {
	return func(options *agentOptions) { options.streamOptions = streamOptions }
}

func WithToolExecution(mode ToolExecutionMode) AgentOption {
	return func(options *agentOptions) { options.toolExecution = mode }
}

// WithClock supplies JavaScript Date.now-compatible milliseconds. It is useful
// for deterministic fixture runs and applications with an injected clock.
func WithClock(now func() int64) AgentOption {
	return func(options *agentOptions) { options.now = now }
}

type activeRun struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

type listenerEntry struct {
	id   uint64
	sink EventSink
}

// Agent is the stateful wrapper around RunLoop and RunLoopContinue.
type Agent struct {
	mu sync.Mutex

	state AgentState

	convertToLLM               ConvertToLLMFunc
	transformContext           TransformContextFunc
	streamFn                   StreamFn
	getAPIKey                  GetAPIKeyFunc
	getRequestAuth             GetRequestAuthFunc
	getModelHeaders            GetModelHeadersFunc
	beforeToolCall             BeforeToolCallFunc
	afterToolCall              AfterToolCallFunc
	prepareNextTurn            PrepareNextTurnWithoutContextFunc
	prepareNextTurnWithContext PrepareNextTurnFunc
	shouldStopAfterTurn        ShouldStopAfterTurnFunc
	getSteeringMessages        GetQueuedMessagesFunc
	getFollowUpMessages        GetQueuedMessagesFunc
	streamOptions              ai.SimpleStreamOptions
	toolExecution              ToolExecutionMode
	now                        func() int64

	steeringMode QueueMode
	followUpMode QueueMode
	steering     AgentMessages
	followUps    AgentMessages

	active         *activeRun
	listeners      []listenerEntry
	nextListenerID uint64
}

func NewAgent(stream StreamFn, option ...AgentOption) *Agent {
	options := agentOptions{
		convertToLLM:  defaultConvertToLLMFunc,
		streamFn:      stream,
		steeringMode:  QueueOneAtATime,
		followUpMode:  QueueOneAtATime,
		toolExecution: ToolExecutionParallel,
		now:           func() int64 { return time.Now().UnixMilli() },
	}
	options.streamOptions.Transport = pointerTo(ai.TransportAuto)
	for _, apply := range option {
		if apply != nil {
			apply(&options)
		}
	}
	if options.streamOptions.Transport == nil {
		options.streamOptions.Transport = pointerTo(ai.TransportAuto)
	}
	if options.now == nil {
		options.now = func() int64 { return time.Now().UnixMilli() }
	}
	if options.streamFn == nil {
		if streamFn, err := getDefaultStreamFn(); err == nil {
			options.streamFn = streamFn
		}
	}

	state := defaultAgentState()
	if options.initialState != nil {
		state = copyAgentState(*options.initialState)
	}
	if state.ThinkingLevel == "" {
		state.ThinkingLevel = ThinkingOff
	}
	if state.Model == nil {
		state.Model = defaultAgentModel()
	}
	if state.Tools == nil {
		state.Tools = []AgentTool{}
	}
	if state.Messages == nil {
		state.Messages = AgentMessages{}
	}
	state.IsStreaming = false
	state.StreamingMessage = nil
	state.PendingToolCalls = map[string]struct{}{}
	state.ErrorMessage = nil

	return &Agent{
		state:                      state,
		convertToLLM:               options.convertToLLM,
		transformContext:           options.transformContext,
		streamFn:                   options.streamFn,
		getAPIKey:                  options.getAPIKey,
		getRequestAuth:             options.getRequestAuth,
		getModelHeaders:            options.getModelHeaders,
		beforeToolCall:             options.beforeToolCall,
		afterToolCall:              options.afterToolCall,
		prepareNextTurn:            options.prepareNextTurn,
		prepareNextTurnWithContext: options.prepareNextTurnWithContext,
		shouldStopAfterTurn:        options.shouldStopAfterTurn,
		getSteeringMessages:        options.getSteeringMessages,
		getFollowUpMessages:        options.getFollowUpMessages,
		streamOptions:              options.streamOptions,
		toolExecution:              options.toolExecution,
		now:                        options.now,
		steeringMode:               normalizeQueueMode(options.steeringMode),
		followUpMode:               normalizeQueueMode(options.followUpMode),
	}
}

// Prompt starts a run from a string, one AgentMessage, or AgentMessages. String
// prompts accept optional images and are normalized to upstream content blocks.
func (agent *Agent) Prompt(ctx context.Context, input any, images ...*ai.ImageContent) error {
	agent.mu.Lock()
	busy := agent.active != nil
	missingStreamFn := agent.streamFn == nil
	agent.mu.Unlock()
	if busy {
		return upstreamError(alreadyPromptingMessage)
	}
	if missingStreamFn {
		return upstreamError(missingDefaultStreamFnMessage)
	}
	messages, err := agent.normalizePromptInput(input, images)
	if err != nil {
		return err
	}
	return agent.runPromptMessages(ctx, messages, false)
}

func (agent *Agent) Continue(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	agent.mu.Lock()
	if agent.active != nil {
		agent.mu.Unlock()
		return upstreamError("Agent is already processing. Wait for completion before continuing.")
	}
	if agent.streamFn == nil {
		agent.mu.Unlock()
		return upstreamError(missingDefaultStreamFnMessage)
	}
	if len(agent.state.Messages) == 0 {
		agent.mu.Unlock()
		return upstreamError("No messages to continue from")
	}
	last := agent.state.Messages[len(agent.state.Messages)-1]
	if agentMessageRole(last) == "assistant" {
		steering := agent.drainQueueLocked(&agent.steering, agent.steeringMode)
		if len(steering) > 0 {
			active := agent.beginRunLocked(ctx)
			agent.mu.Unlock()
			return agent.runPromptMessagesReserved(active, steering, true)
		}
		followUps := agent.drainQueueLocked(&agent.followUps, agent.followUpMode)
		if len(followUps) > 0 {
			active := agent.beginRunLocked(ctx)
			agent.mu.Unlock()
			return agent.runPromptMessagesReserved(active, followUps, false)
		}
		agent.mu.Unlock()
		return upstreamError("Cannot continue from message role: assistant")
	}
	active := agent.beginRunLocked(ctx)
	agent.mu.Unlock()
	return agent.runContinuationReserved(active)
}

func (agent *Agent) Steer(message AgentMessage) {
	agent.mu.Lock()
	agent.steering = append(agent.steering, message)
	agent.mu.Unlock()
}

func (agent *Agent) FollowUp(message AgentMessage) {
	agent.mu.Lock()
	agent.followUps = append(agent.followUps, message)
	agent.mu.Unlock()
}

func (agent *Agent) ClearSteeringQueue() {
	agent.mu.Lock()
	agent.steering = nil
	agent.mu.Unlock()
}

func (agent *Agent) ClearFollowUpQueue() {
	agent.mu.Lock()
	agent.followUps = nil
	agent.mu.Unlock()
}

func (agent *Agent) ClearAllQueues() {
	agent.mu.Lock()
	agent.steering = nil
	agent.followUps = nil
	agent.mu.Unlock()
}

func (agent *Agent) HasQueuedMessages() bool {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return len(agent.steering) > 0 || len(agent.followUps) > 0
}

func (agent *Agent) SetSteeringMode(mode QueueMode) {
	agent.mu.Lock()
	agent.steeringMode = normalizeQueueMode(mode)
	agent.mu.Unlock()
}

func (agent *Agent) SteeringMode() QueueMode {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.steeringMode
}

func (agent *Agent) SetFollowUpMode(mode QueueMode) {
	agent.mu.Lock()
	agent.followUpMode = normalizeQueueMode(mode)
	agent.mu.Unlock()
}

func (agent *Agent) FollowUpMode() QueueMode {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.followUpMode
}

func (agent *Agent) Abort() {
	agent.mu.Lock()
	active := agent.active
	agent.mu.Unlock()
	if active != nil {
		active.cancel()
	}
}

func (agent *Agent) WaitForIdle(ctx context.Context) error {
	agent.mu.Lock()
	active := agent.active
	agent.mu.Unlock()
	if active == nil {
		return nil
	}
	select {
	case <-active.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (agent *Agent) IsIdle() bool {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.active == nil
}

func (agent *Agent) SetTransport(transport ai.Transport) {
	agent.mu.Lock()
	agent.streamOptions.Transport = pointerTo(transport)
	agent.mu.Unlock()
}

// SetStreamSessionID sets the per-request session id providers use for
// affinity and prompt-cache keys (upstream Agent's constructor sessionId).
func (agent *Agent) SetStreamSessionID(sessionID string) {
	agent.mu.Lock()
	agent.streamOptions.SessionID = pointerTo(sessionID)
	agent.mu.Unlock()
}

// Signal returns the active run context, matching upstream's abort signal
// exposure to extensions. It is nil while the agent is idle.
func (agent *Agent) Signal() context.Context {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.active == nil {
		return nil
	}
	return agent.active.ctx
}

// Subscribe adds a listener and returns its unsubscribe func. Listeners are
// awaited in subscription order for each event; overlapping upstream events may
// invoke the same listener concurrently.
func (agent *Agent) Subscribe(listener EventSink) func() {
	if listener == nil {
		return func() {}
	}
	agent.mu.Lock()
	agent.nextListenerID++
	id := agent.nextListenerID
	agent.listeners = append(agent.listeners, listenerEntry{id: id, sink: listener})
	agent.mu.Unlock()
	return func() {
		agent.mu.Lock()
		for index := range agent.listeners {
			if agent.listeners[index].id == id {
				agent.listeners = append(agent.listeners[:index], agent.listeners[index+1:]...)
				break
			}
		}
		agent.mu.Unlock()
	}
}

func (agent *Agent) Reset() {
	agent.mu.Lock()
	agent.state.Messages = AgentMessages{}
	agent.state.IsStreaming = false
	agent.state.StreamingMessage = nil
	agent.state.PendingToolCalls = map[string]struct{}{}
	agent.state.ErrorMessage = nil
	agent.steering = nil
	agent.followUps = nil
	agent.mu.Unlock()
}

func (agent *Agent) State() AgentState {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return copyAgentState(agent.state)
}

func (agent *Agent) DisplayState() AgentDisplayState {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	result := AgentDisplayState{
		ThinkingLevel: agent.state.ThinkingLevel,
	}
	if agent.state.Model != nil {
		result.HasModel = true
		result.ModelID = agent.state.Model.ID
		result.Provider = agent.state.Model.Provider
		result.ContextWindow = agent.state.Model.ContextWindow
		result.Reasoning = agent.state.Model.Reasoning
	}
	return result
}

func (agent *Agent) SetSystemPrompt(prompt string) {
	agent.mu.Lock()
	agent.state.SystemPrompt = prompt
	agent.mu.Unlock()
}

func (agent *Agent) SetModel(model *ai.Model) {
	agent.mu.Lock()
	agent.state.Model = cloneModel(model)
	if agent.state.Model == nil {
		agent.state.Model = defaultAgentModel()
	}
	agent.mu.Unlock()
}

func (agent *Agent) SetThinkingLevel(level ThinkingLevel) {
	agent.mu.Lock()
	agent.state.ThinkingLevel = level
	agent.mu.Unlock()
}

func (agent *Agent) SetTools(tools []AgentTool) {
	agent.mu.Lock()
	agent.state.Tools = cloneAgentTools(tools)
	agent.mu.Unlock()
}

func (agent *Agent) SetMessages(messages AgentMessages) {
	agent.mu.Lock()
	agent.state.Messages = cloneAgentMessages(messages)
	agent.mu.Unlock()
}

func (agent *Agent) AppendMessage(message AgentMessage) {
	agent.mu.Lock()
	agent.state.Messages = append(agent.state.Messages, cloneAgentMessage(message))
	agent.mu.Unlock()
}

func (agent *Agent) SetTransformContext(transform TransformContextFunc) {
	agent.mu.Lock()
	agent.transformContext = transform
	agent.mu.Unlock()
}

func (agent *Agent) SetStreamFn(stream StreamFn) {
	agent.mu.Lock()
	agent.streamFn = stream
	agent.mu.Unlock()
}

func (agent *Agent) StreamFn() StreamFn {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return agent.streamFn
}

func (agent *Agent) SetRequestResolvers(apiKey GetAPIKeyFunc, auth GetRequestAuthFunc, headers GetModelHeadersFunc) {
	agent.mu.Lock()
	if apiKey != nil {
		agent.getAPIKey = apiKey
	}
	if auth != nil {
		agent.getRequestAuth = auth
	}
	if headers != nil {
		agent.getModelHeaders = headers
	}
	agent.mu.Unlock()
}

func (agent *Agent) SetToolCallHooks(before BeforeToolCallFunc, after AfterToolCallFunc) {
	agent.mu.Lock()
	agent.beforeToolCall = before
	agent.afterToolCall = after
	agent.mu.Unlock()
}

func (agent *Agent) SetProviderHooks(payload ai.PayloadHook, headers ai.HeadersHook, response ai.ResponseHook) {
	agent.mu.Lock()
	agent.streamOptions.OnPayload = payload
	agent.streamOptions.TransformHeaders = headers
	agent.streamOptions.OnResponse = response
	agent.mu.Unlock()
}

func (agent *Agent) ReplaceLastMessage(replacement AgentMessage) bool {
	if replacement == nil {
		return false
	}
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if len(agent.state.Messages) == 0 {
		return false
	}
	agent.state.Messages[len(agent.state.Messages)-1] = cloneAgentMessage(replacement)
	return true
}

func (agent *Agent) normalizePromptInput(input any, images []*ai.ImageContent) (AgentMessages, error) {
	switch value := input.(type) {
	case string:
		blocks := ai.UserContentBlocks{&ai.TextContent{Text: value}}
		for _, image := range images {
			if image != nil {
				copy := *image
				blocks = append(blocks, &copy)
			}
		}
		return AgentMessages{&ai.UserMessage{
			Content:   ai.NewUserContent(blocks...),
			Timestamp: agent.clockNow(),
		}}, nil
	case AgentMessages:
		return append(AgentMessages(nil), value...), nil
	case []any:
		return append(AgentMessages(nil), value...), nil
	case nil:
		return nil, errors.New("agent: prompt is nil")
	default:
		return AgentMessages{value}, nil
	}
}

func (agent *Agent) runPromptMessages(ctx context.Context, messages AgentMessages, skipInitialSteeringPoll bool) error {
	return agent.runWithLifecycle(ctx, func(runContext context.Context) error {
		loopContext := agent.contextSnapshot()
		config := agent.loopConfig(skipInitialSteeringPoll)
		_, err := RunLoop(runContext, messages, loopContext, config, agent.processEvent, agent.StreamFn())
		return err
	})
}

func (agent *Agent) runPromptMessagesReserved(active *activeRun, messages AgentMessages, skipInitialSteeringPoll bool) error {
	return agent.runReserved(active, func(runContext context.Context) error {
		loopContext := agent.contextSnapshot()
		config := agent.loopConfig(skipInitialSteeringPoll)
		_, err := RunLoop(runContext, messages, loopContext, config, agent.processEvent, agent.StreamFn())
		return err
	})
}

func (agent *Agent) runContinuationReserved(active *activeRun) error {
	return agent.runReserved(active, func(runContext context.Context) error {
		loopContext := agent.contextSnapshot()
		config := agent.loopConfig(false)
		_, err := RunLoopContinue(runContext, &loopContext, config, agent.processEvent, agent.StreamFn())
		return err
	})
}

func (agent *Agent) runWithLifecycle(ctx context.Context, execute func(context.Context) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	agent.mu.Lock()
	if agent.active != nil {
		agent.mu.Unlock()
		return upstreamError(alreadyPromptingMessage)
	}
	active := agent.beginRunLocked(ctx)
	agent.mu.Unlock()
	return agent.runReserved(active, execute)
}

func (agent *Agent) beginRunLocked(ctx context.Context) *activeRun {
	runContext, cancel := context.WithCancel(ctx)
	active := &activeRun{ctx: runContext, cancel: cancel, done: make(chan struct{})}
	agent.active = active
	agent.state.IsStreaming = true
	agent.state.StreamingMessage = nil
	agent.state.ErrorMessage = nil
	return active
}

func (agent *Agent) runReserved(active *activeRun, execute func(context.Context) error) error {
	err := execute(active.ctx)
	if err != nil {
		if failureErr := agent.handleRunFailure(err, active.ctx.Err() != nil); failureErr != nil {
			err = failureErr
		} else {
			err = nil
		}
	}

	agent.mu.Lock()
	agent.state.IsStreaming = false
	agent.state.StreamingMessage = nil
	agent.state.PendingToolCalls = map[string]struct{}{}
	if agent.active == active {
		agent.active = nil
	}
	close(active.done)
	agent.mu.Unlock()
	active.cancel()
	return err
}

func (agent *Agent) handleRunFailure(runErr error, aborted bool) error {
	model := agent.currentModel()
	messageText := runErr.Error()
	stopReason := ai.StopReasonError
	if aborted {
		stopReason = ai.StopReasonAborted
	}
	failure := &ai.AssistantMessage{
		Content:      ai.AssistantContent{&ai.TextContent{Text: ""}},
		API:          model.API,
		Provider:     model.Provider,
		Model:        model.ID,
		Usage:        ai.Usage{Cost: ai.Cost{}},
		StopReason:   stopReason,
		ErrorMessage: &messageText,
		Timestamp:    agent.clockNow(),
	}
	ai.SetAssistantMessageErrorBeforeTimestamp(failure, true)
	ctx := agent.activeContext()
	for _, event := range []AgentEvent{
		MessageStartEvent{Message: failure},
		MessageEndEvent{Message: failure},
		TurnEndEvent{Message: failure, ToolResults: []*ai.ToolResultMessage{}},
		AgentEndEvent{Messages: AgentMessages{failure}},
	} {
		if err := agent.processEvent(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (agent *Agent) processEvent(ctx context.Context, event AgentEvent) error {
	agent.mu.Lock()
	switch value := event.(type) {
	case MessageStartEvent:
		agent.state.StreamingMessage = cloneAgentMessage(value.Message)
	case MessageUpdateEvent:
		agent.state.StreamingMessage = cloneAgentMessage(value.Message)
	case MessageEndEvent:
		agent.state.StreamingMessage = nil
		agent.state.Messages = append(agent.state.Messages, cloneAgentMessage(value.Message))
	case ToolExecutionStartEvent:
		pending := copyPendingToolCalls(agent.state.PendingToolCalls)
		pending[value.ToolCallID] = struct{}{}
		agent.state.PendingToolCalls = pending
	case ToolExecutionEndEvent:
		pending := copyPendingToolCalls(agent.state.PendingToolCalls)
		delete(pending, value.ToolCallID)
		agent.state.PendingToolCalls = pending
	case TurnEndEvent:
		if assistant, ok := value.Message.(*ai.AssistantMessage); ok && assistant.ErrorMessage != nil {
			message := *assistant.ErrorMessage
			agent.state.ErrorMessage = &message
		}
	case AgentEndEvent:
		agent.state.StreamingMessage = nil
	}
	agent.mu.Unlock()

	var lastID uint64
	for {
		agent.mu.Lock()
		var next listenerEntry
		for _, candidate := range agent.listeners {
			if candidate.id > lastID && (next.id == 0 || candidate.id < next.id) {
				next = candidate
			}
		}
		agent.mu.Unlock()
		if next.id == 0 {
			return nil
		}
		lastID = next.id
		if err := next.sink(ctx, event); err != nil {
			return err
		}
	}
}

func (agent *Agent) contextSnapshot() AgentContext {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	return AgentContext{
		SystemPrompt: agent.state.SystemPrompt,
		Messages:     append(AgentMessages(nil), agent.state.Messages...),
		Tools:        cloneAgentTools(agent.state.Tools),
	}
}

func (agent *Agent) loopConfig(skipInitialSteeringPoll bool) AgentLoopConfig {
	agent.mu.Lock()
	model := cloneModel(agent.state.Model)
	thinking := agent.state.ThinkingLevel
	config := AgentLoopConfig{
		SimpleStreamOptions: agent.streamOptions,
		Model:               model,
		ConvertToLLM:        agent.convertToLLM,
		TransformContext:    agent.transformContext,
		GetAPIKey:           agent.getAPIKey,
		GetRequestAuth:      agent.getRequestAuth,
		GetModelHeaders:     agent.getModelHeaders,
		ToolExecution:       agent.toolExecution,
		BeforeToolCall:      agent.beforeToolCall,
		AfterToolCall:       agent.afterToolCall,
		ShouldStopAfterTurn: agent.shouldStopAfterTurn,
		Now:                 agent.now,
	}
	prepareWithoutContext := agent.prepareNextTurn
	prepareWithContext := agent.prepareNextTurnWithContext
	externalSteering := agent.getSteeringMessages
	externalFollowUps := agent.getFollowUpMessages
	agent.mu.Unlock()

	if thinking != ThinkingOff {
		reasoning := ai.ThinkingLevel(thinking)
		config.Reasoning = &reasoning
	}
	if prepareWithContext != nil {
		config.PrepareNextTurn = prepareWithContext
	} else if prepareWithoutContext != nil {
		config.PrepareNextTurn = func(ctx context.Context, _ PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
			return prepareWithoutContext(ctx)
		}
	}
	initialPoll := true
	config.GetSteeringMessages = func(ctx context.Context) (AgentMessages, error) {
		agent.mu.Lock()
		if initialPoll {
			initialPoll = false
			if skipInitialSteeringPoll {
				agent.mu.Unlock()
				return AgentMessages{}, nil
			}
		}
		messages := agent.drainQueueLocked(&agent.steering, agent.steeringMode)
		agent.mu.Unlock()
		if externalSteering != nil {
			external, err := externalSteering(ctx)
			if err != nil {
				return nil, err
			}
			messages = append(messages, external...)
		}
		return messages, nil
	}
	config.GetFollowUpMessages = func(ctx context.Context) (AgentMessages, error) {
		agent.mu.Lock()
		messages := agent.drainQueueLocked(&agent.followUps, agent.followUpMode)
		agent.mu.Unlock()
		if externalFollowUps != nil {
			external, err := externalFollowUps(ctx)
			if err != nil {
				return nil, err
			}
			messages = append(messages, external...)
		}
		return messages, nil
	}
	return config
}

func (agent *Agent) drainQueueLocked(queue *AgentMessages, mode QueueMode) AgentMessages {
	if len(*queue) == 0 {
		return AgentMessages{}
	}
	if mode == QueueAll {
		drained := append(AgentMessages(nil), (*queue)...)
		*queue = nil
		return drained
	}
	first := (*queue)[0]
	*queue = append(AgentMessages(nil), (*queue)[1:]...)
	return AgentMessages{first}
}

func (agent *Agent) clockNow() int64 {
	agent.mu.Lock()
	now := agent.now
	agent.mu.Unlock()
	if now == nil {
		return time.Now().UnixMilli()
	}
	return now()
}

func (agent *Agent) currentModel() *ai.Model {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	model := cloneModel(agent.state.Model)
	if model == nil {
		return defaultAgentModel()
	}
	return model
}

func (agent *Agent) activeContext() context.Context {
	agent.mu.Lock()
	defer agent.mu.Unlock()
	if agent.active == nil {
		return context.Background()
	}
	return agent.active.ctx
}

func defaultAgentState() AgentState {
	return AgentState{
		Model:            defaultAgentModel(),
		ThinkingLevel:    ThinkingOff,
		Tools:            []AgentTool{},
		Messages:         AgentMessages{},
		PendingToolCalls: map[string]struct{}{},
	}
}

func defaultAgentModel() *ai.Model {
	return &ai.Model{
		ID:       "unknown",
		Name:     "unknown",
		API:      ai.API("unknown"),
		Provider: ai.ProviderID("unknown"),
		Input:    ai.InputModalities{},
		Cost:     ai.ModelCost{},
	}
}

func defaultConvertToLLMFunc(_ context.Context, messages AgentMessages) (ai.MessageList, error) {
	return defaultConvertToLLM(messages), nil
}

func copyAgentState(source AgentState) AgentState {
	copy := source
	copy.Model = cloneModel(source.Model)
	copy.Tools = cloneAgentTools(source.Tools)
	copy.Messages = cloneAgentMessages(source.Messages)
	copy.StreamingMessage = cloneAgentMessage(source.StreamingMessage)
	copy.PendingToolCalls = copyPendingToolCalls(source.PendingToolCalls)
	if source.ErrorMessage != nil {
		message := *source.ErrorMessage
		copy.ErrorMessage = &message
	}
	return copy
}

func copyPendingToolCalls(source map[string]struct{}) map[string]struct{} {
	copy := make(map[string]struct{}, len(source))
	for id := range source {
		copy[id] = struct{}{}
	}
	return copy
}

func cloneModel(model *ai.Model) *ai.Model {
	if model == nil {
		return nil
	}
	copy := *model
	copy.Input = append(ai.InputModalities(nil), model.Input...)
	if model.ThinkingLevelMap != nil {
		levelMap := make(map[ai.ModelThinkingLevel]*string, len(*model.ThinkingLevelMap))
		for level, value := range *model.ThinkingLevelMap {
			levelMap[level] = cloneStringPointer(value)
		}
		copy.ThinkingLevelMap = &levelMap
	}
	if model.Cost.Tiers != nil {
		tiers := append([]ai.ModelCostTier(nil), (*model.Cost.Tiers)...)
		copy.Cost.Tiers = &tiers
	}
	if model.Headers != nil {
		headers := make(map[string]string, len(*model.Headers))
		for key, value := range *model.Headers {
			headers[key] = value
		}
		copy.Headers = &headers
	}
	if model.Compat != nil {
		copy.Compat = append([]byte(nil), model.Compat...)
	}
	return &copy
}

func cloneAgentMessages(messages AgentMessages) AgentMessages {
	if messages == nil {
		return nil
	}
	copy := make(AgentMessages, len(messages))
	for index, message := range messages {
		copy[index] = cloneAgentMessage(message)
	}
	return copy
}

func cloneAgentTools(tools []AgentTool) []AgentTool {
	if tools == nil {
		return nil
	}
	result := make([]AgentTool, len(tools))
	copy(result, tools)
	return result
}

func normalizeQueueMode(mode QueueMode) QueueMode {
	if mode == QueueAll {
		return QueueAll
	}
	return QueueOneAtATime
}

func pointerTo[T any](value T) *T { return &value }
