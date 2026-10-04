package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

var errNoModel = errors.New("agent: loop requires a model")

// maxParallelToolCalls bounds concurrently executing tool calls within one
// assistant message. Result order is unaffected.
const maxParallelToolCalls = 16

const (
	maxDroppedToolCallRetries = 3
	droppedToolCallNudge      = "Your previous turn indicated a tool call but none was included. Do not narrate a plan or restate intent; issue the actual tool call now to continue the task."
)

// RunLoop starts a loop with prompt messages and returns only messages created
// by this invocation. The caller-owned context is copied before it is changed.
func RunLoop(
	ctx context.Context,
	prompts AgentMessages,
	loopContext AgentContext,
	config AgentLoopConfig,
	sink EventSink,
	streamFn StreamFn,
) (AgentMessages, error) {
	current := copyAgentContext(loopContext)
	prompts = declarePromptAndToolChanges(current, prompts, loopNow(config))
	current.Messages = append(current.Messages, prompts...)
	newMessages := append(AgentMessages(nil), prompts...)
	emitter := newEventEmitter(sink)
	defer emitter.close()

	if err := emitter.emit(ctx, AgentStartEvent{}); err != nil {
		return nil, err
	}
	if err := emitter.emit(ctx, TurnStartEvent{}); err != nil {
		return nil, err
	}
	for _, prompt := range prompts {
		if err := emitter.emit(ctx, MessageStartEvent{Message: prompt}); err != nil {
			return nil, err
		}
		if err := emitter.emit(ctx, MessageEndEvent{Message: prompt}); err != nil {
			return nil, err
		}
	}
	if streamFn == nil {
		return nil, upstreamError(missingDefaultStreamFnMessage)
	}

	if err := runLoop(ctx, &current, &newMessages, config, emitter, streamFn); err != nil {
		return nil, err
	}
	return newMessages, nil
}

// RunLoopContinue resumes a non-assistant transcript without adding a prompt.
func RunLoopContinue(
	ctx context.Context,
	loopContext *AgentContext,
	config AgentLoopConfig,
	sink EventSink,
	streamFn StreamFn,
) (AgentMessages, error) {
	if loopContext == nil || len(loopContext.Messages) == 0 {
		return nil, upstreamError("Cannot continue: no messages in context")
	}
	if agentMessageRole(loopContext.Messages[len(loopContext.Messages)-1]) == "assistant" {
		return nil, upstreamError("Cannot continue from message role: assistant")
	}

	newMessages := AgentMessages{}
	emitter := newEventEmitter(sink)
	defer emitter.close()
	if err := emitter.emit(ctx, AgentStartEvent{}); err != nil {
		return nil, err
	}
	if err := emitter.emit(ctx, TurnStartEvent{}); err != nil {
		return nil, err
	}
	if streamFn == nil {
		return nil, upstreamError(missingDefaultStreamFnMessage)
	}
	if err := runLoop(ctx, loopContext, &newMessages, config, emitter, streamFn); err != nil {
		return nil, err
	}
	return newMessages, nil
}

func runLoop(
	ctx context.Context,
	currentContext *AgentContext,
	newMessages *AgentMessages,
	config AgentLoopConfig,
	emitter *eventEmitter,
	streamFn StreamFn,
) error {
	firstTurn := true
	explicitContinuation := false
	droppedToolCallRetries := 0
	recoveryScaffoldStart := -1
	pendingMessages, err := queuedMessages(ctx, config.GetSteeringMessages)
	if err != nil {
		return err
	}

	for {
		hasMoreToolCalls := true
		for hasMoreToolCalls || len(pendingMessages) > 0 {
			if !firstTurn {
				if err := emitter.emit(ctx, TurnStartEvent{}); err != nil {
					return err
				}
			} else {
				firstTurn = false
			}

			pendingMessages = declarePromptAndToolChanges(*currentContext, pendingMessages, loopNow(config))
			for _, message := range pendingMessages {
				if err := emitter.emit(ctx, MessageStartEvent{Message: message}); err != nil {
					return err
				}
				if err := emitter.emit(ctx, MessageEndEvent{Message: message}); err != nil {
					return err
				}
				currentContext.Messages = append(currentContext.Messages, message)
				*newMessages = append(*newMessages, message)
			}
			if config.PrepareRequest != nil {
				update, updateErr := config.PrepareRequest(ctx, PrepareRequestContext{
					Context: currentContext, Model: config.Model, ThinkingLevel: ThinkingLevel(requestedThinkingLevel(config)),
				})
				if updateErr != nil {
					return updateErr
				}
				var scaffold AgentMessages
				if recoveryScaffoldStart >= 0 && update != nil && update.Context != nil && update.Context != currentContext {
					// A projected context never holds the unrecorded recovery
					// scaffold; carry it onto the projection.
					scaffold = append(scaffold, currentContext.Messages[recoveryScaffoldStart:]...)
				}
				currentContext = applyTurnUpdate(&config, currentContext, update)
				if scaffold != nil {
					recoveryScaffoldStart = len(currentContext.Messages)
					currentContext.Messages = append(currentContext.Messages, scaffold...)
				}
			}
			mayRecoverDroppedToolCall := droppedToolCallRetries < maxDroppedToolCallRetries
			message, err := streamAssistantResponse(ctx, currentContext, config, emitter, streamFn, mayRecoverDroppedToolCall)
			if err != nil {
				return err
			}
			toolCalls := assistantToolCalls(message)
			if mayRecoverDroppedToolCall && isDroppedToolCallResponse(message, toolCalls) {
				if recoveryScaffoldStart < 0 {
					recoveryScaffoldStart = len(currentContext.Messages) - 1
				}
				currentContext.Messages = append(currentContext.Messages, &ai.UserMessage{
					Content:   ai.NewUserText(droppedToolCallNudge),
					Timestamp: loopNow(config),
				})
				droppedToolCallRetries++
				hasMoreToolCalls = true
				pendingMessages = nil
				if err := emitter.emit(withEphemeralAgentEvent(ctx), TurnEndEvent{
					Message: message, ToolResults: []*ai.ToolResultMessage{},
				}); err != nil {
					return err
				}
				continue
			}
			if recoveryScaffoldStart >= 0 {
				currentContext.Messages = append(currentContext.Messages[:recoveryScaffoldStart], message)
				recoveryScaffoldStart = -1
			}
			droppedToolCallRetries = 0
			*newMessages = append(*newMessages, message)

			if message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted {
				if config.FinishTurn != nil {
					if _, err := config.FinishTurn(ctx, TurnContext{
						Message: message, ToolResults: []*ai.ToolResultMessage{}, Context: currentContext, NewMessages: *newMessages,
					}); err != nil {
						return err
					}
				}
				if err := emitter.emit(ctx, TurnEndEvent{Message: message, ToolResults: []*ai.ToolResultMessage{}}); err != nil {
					return err
				}
				return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
			}

			toolResults := []*ai.ToolResultMessage{}
			hasMoreToolCalls = false
			if len(toolCalls) > 0 {
				var batch executedToolBatch
				if message.StopReason == ai.StopReasonLength {
					batch, err = failTruncatedToolCalls(ctx, toolCalls, config, emitter)
				} else {
					batch, err = executeToolCalls(ctx, currentContext, message, toolCalls, config, emitter)
				}
				if err != nil {
					return err
				}
				toolResults = append(toolResults, batch.messages...)
				hasMoreToolCalls = !batch.terminate
				for _, result := range toolResults {
					currentContext.Messages = append(currentContext.Messages, result)
					*newMessages = append(*newMessages, result)
				}
			}

			turn := TurnContext{Message: message, ToolResults: toolResults, Context: currentContext, NewMessages: *newMessages}
			var action TurnAction
			if config.FinishTurn != nil {
				if action, err = config.FinishTurn(ctx, turn); err != nil {
					return err
				}
			}
			if err := emitter.emit(ctx, TurnEndEvent{Message: message, ToolResults: toolResults}); err != nil {
				return err
			}
			if action == TurnEnd {
				return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
			}
			if config.PrepareNextTurn != nil {
				update, updateErr := config.PrepareNextTurn(ctx, turn)
				if updateErr != nil {
					return updateErr
				}
				currentContext = applyTurnUpdate(&config, currentContext, update)
			}
			explicitContinuation = action == TurnContinue

			pendingMessages, err = queuedMessages(ctx, config.GetSteeringMessages)
			if err != nil {
				return err
			}
			if hasMoreToolCalls || len(pendingMessages) > 0 {
				explicitContinuation = false
			}
		}

		pendingMessages, err = queuedMessages(ctx, config.GetFollowUpMessages)
		if err != nil {
			return err
		}
		if len(pendingMessages) > 0 {
			explicitContinuation = false
			continue
		}
		// No queued work answered the continuation decision, so make one
		// context-only request.
		if !explicitContinuation {
			break
		}
		explicitContinuation = false
	}

	return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
}

func streamAssistantResponse(
	ctx context.Context,
	loopContext *AgentContext,
	config AgentLoopConfig,
	emitter *eventEmitter,
	streamFn StreamFn,
	mayRecoverDroppedToolCall bool,
) (*ai.AssistantMessage, error) {
	if config.Model == nil {
		return nil, errNoModel
	}
	messages := loopContext.Messages
	var err error
	if config.TransformContext != nil {
		messages, err = config.TransformContext(ctx, messages)
		if err != nil {
			return nil, err
		}
	}
	var llmMessages ai.MessageList
	if config.ConvertToLLM != nil {
		llmMessages, err = config.ConvertToLLM(ctx, messages)
	} else {
		llmMessages = defaultConvertToLLM(messages)
	}
	if err != nil {
		return nil, err
	}

	llmContext := ai.Context{Messages: llmMessages}
	if current := ai.CurrentSystemMessage(llmMessages); current != nil {
		prompt := ai.SystemMessageText(current)
		tools := ai.CurrentTools(llmMessages)
		llmContext.SystemPrompt = &prompt
		llmContext.Tools = &tools
		llmContext.Messages = make(ai.MessageList, 0, len(llmMessages))
		for _, message := range llmMessages {
			if _, system := message.(*ai.SystemMessage); !system {
				llmContext.Messages = append(llmContext.Messages, message)
			}
		}
	} else if loopContext.Tools != nil {
		tools := make([]ai.Tool, 0, len(loopContext.Tools))
		for _, tool := range loopContext.Tools {
			spec := tool.Spec()
			tools = append(tools, ai.Tool{
				Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters,
				ConstrainedSampling: spec.ConstrainedSampling,
			})
		}
		llmContext.Tools = &tools
	}

	requestModel := cloneModel(config.Model)
	options := config.SimpleStreamOptions
	if config.GetRequestAuth != nil {
		resolved, authErr := config.GetRequestAuth(ctx, requestModel.Provider)
		if authErr != nil {
			return nil, authErr
		}
		if resolved != nil {
			if options.APIKey == nil {
				options.APIKey = resolved.APIKey
			}
			options.Env = mergeRequestEnv(resolved.Env, options.Env)
			options.Headers = mergeRequestAuthHeaders(resolved.Headers, options.Headers)
			if resolved.BaseURL != nil {
				requestModel.BaseURL = *resolved.BaseURL
			}
		}
	} else if config.GetAPIKey != nil {
		key, keyErr := config.GetAPIKey(ctx, requestModel.Provider)
		if keyErr != nil {
			return nil, keyErr
		}
		if key != nil && *key != "" {
			options.APIKey = key
		}
	}
	if config.GetModelHeaders != nil {
		headers, headerErr := config.GetModelHeaders(ctx, requestModel, options.APIKey, options.Env)
		if headerErr != nil {
			return nil, headerErr
		}
		requestModel.Headers = mergeRequestHeaders(requestModel.Headers, headers)
	}
	for _, message := range llmMessages {
		if system, ok := message.(*ai.SystemMessage); ok && ai.SystemMessageText(system) != "" {
			ctx = ai.WithTranscriptContext(ctx, ai.TranscriptContext{Messages: llmMessages})
			break
		}
	}
	stream, err := streamFn(ctx, requestModel, llmContext, &options)
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, ai.ErrStreamIncomplete
	}

	var partial *ai.AssistantMessage
	addedPartial := false
	for event, streamErr := range stream {
		if streamErr != nil {
			return nil, streamErr
		}
		switch value := event.(type) {
		case ai.StartEvent:
			partial = value.Partial
			if partial == nil {
				return nil, ai.ErrStreamIncomplete
			}
			loopContext.Messages = append(loopContext.Messages, partial)
			addedPartial = true
			if err := emitter.emit(ctx, MessageStartEvent{Message: shallowAssistantCopy(partial)}); err != nil {
				return nil, err
			}
		case *ai.StartEvent:
			partial = value.Partial
			if partial == nil {
				return nil, ai.ErrStreamIncomplete
			}
			loopContext.Messages = append(loopContext.Messages, partial)
			addedPartial = true
			if err := emitter.emit(ctx, MessageStartEvent{Message: shallowAssistantCopy(partial)}); err != nil {
				return nil, err
			}
		case ai.DoneEvent:
			return finishAssistantResponse(ctx, loopContext, value.Message, addedPartial, emitter, mayRecoverDroppedToolCall, requestedThinkingLevel(config))
		case *ai.DoneEvent:
			return finishAssistantResponse(ctx, loopContext, value.Message, addedPartial, emitter, mayRecoverDroppedToolCall, requestedThinkingLevel(config))
		case ai.ErrorEvent:
			return finishAssistantResponse(ctx, loopContext, value.Error, addedPartial, emitter, false, requestedThinkingLevel(config))
		case *ai.ErrorEvent:
			return finishAssistantResponse(ctx, loopContext, value.Error, addedPartial, emitter, false, requestedThinkingLevel(config))
		default:
			eventPartial, ok := assistantEventPartial(event)
			if ok && partial != nil && eventPartial != nil {
				partial = eventPartial
				loopContext.Messages[len(loopContext.Messages)-1] = partial
				if err := emitter.emit(ctx, MessageUpdateEvent{
					AssistantMessageEvent: event,
					Message:               shallowAssistantCopy(partial),
				}); err != nil {
					return nil, err
				}
			}
		}
	}
	return nil, ai.ErrStreamIncomplete
}

func mergeRequestEnv(resolved, overrides ai.ProviderEnv) ai.ProviderEnv {
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

func mergeRequestAuthHeaders(resolved, overrides ai.ProviderHeaders) ai.ProviderHeaders {
	if len(resolved) == 0 && len(overrides) == 0 {
		return nil
	}
	merged := make(ai.ProviderHeaders, len(resolved)+len(overrides))
	for name, value := range resolved {
		merged[name] = value
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

func mergeRequestHeaders(base, override *map[string]string) *map[string]string {
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

func finishAssistantResponse(
	ctx context.Context,
	loopContext *AgentContext,
	message *ai.AssistantMessage,
	addedPartial bool,
	emitter *eventEmitter,
	mayRecoverDroppedToolCall bool,
	thinkingLevel ai.ModelThinkingLevel,
) (*ai.AssistantMessage, error) {
	if message == nil {
		return nil, ai.ErrStreamIncomplete
	}
	message.ThinkingLevel = &thinkingLevel
	uniquifyToolCallIDs(message)
	eventContext := ctx
	if mayRecoverDroppedToolCall && isDroppedToolCallResponse(message, assistantToolCalls(message)) {
		eventContext = withEphemeralAgentEvent(ctx)
	}
	if addedPartial {
		loopContext.Messages[len(loopContext.Messages)-1] = message
	} else {
		loopContext.Messages = append(loopContext.Messages, message)
		if err := emitter.emit(eventContext, MessageStartEvent{Message: shallowAssistantCopy(message)}); err != nil {
			return nil, err
		}
	}
	if err := emitter.emit(eventContext, MessageEndEvent{Message: message}); err != nil {
		return nil, err
	}
	return message, nil
}

// applyTurnUpdate applies a prepare hook's update to the loop configuration
// and returns the context the next request uses.
func applyTurnUpdate(config *AgentLoopConfig, current *AgentContext, update *AgentLoopTurnUpdate) *AgentContext {
	if update == nil {
		return current
	}
	if update.Model != nil {
		config.Model = update.Model
	}
	if update.ThinkingLevel != nil {
		if *update.ThinkingLevel == ThinkingOff {
			config.Reasoning = nil
		} else {
			reasoning := ai.ThinkingLevel(*update.ThinkingLevel)
			config.Reasoning = &reasoning
		}
	}
	if update.Context != nil {
		return update.Context
	}
	return current
}

// requestedThinkingLevel is the level recorded on each assistant message,
// whichever stream function answered the request.
func requestedThinkingLevel(config AgentLoopConfig) ai.ModelThinkingLevel {
	if config.Reasoning == nil {
		return ai.ModelThinkingOff
	}
	return ai.ModelThinkingLevel(*config.Reasoning)
}

func isDroppedToolCallResponse(message *ai.AssistantMessage, toolCalls []*ai.ToolCall) bool {
	return message != nil && message.StopReason == ai.StopReasonToolUse && len(toolCalls) == 0
}

func uniquifyToolCallIDs(message *ai.AssistantMessage) {
	seen := make(map[string]struct{})
	for _, block := range message.Content {
		toolCall, ok := block.(*ai.ToolCall)
		if !ok {
			continue
		}
		pairingID, itemID := splitToolCallID(toolCall.ID)
		if pairingID == "" {
			continue
		}
		if _, exists := seen[pairingID]; !exists {
			seen[pairingID] = struct{}{}
			continue
		}
		for suffix := 2; ; suffix++ {
			candidate := fmt.Sprintf("%s_d%d", pairingID, suffix)
			if _, exists := seen[candidate]; exists {
				continue
			}
			seen[candidate] = struct{}{}
			toolCall.ID = candidate + itemID
			break
		}
	}
}

func splitToolCallID(id string) (string, string) {
	if separator := strings.IndexByte(id, '|'); separator >= 0 {
		return id[:separator], id[separator:]
	}
	return id, ""
}

func assistantEventPartial(event ai.AssistantMessageEvent) (*ai.AssistantMessage, bool) {
	switch value := event.(type) {
	case ai.TextStartEvent:
		return value.Partial, true
	case *ai.TextStartEvent:
		return value.Partial, true
	case ai.TextDeltaEvent:
		return value.Partial, true
	case *ai.TextDeltaEvent:
		return value.Partial, true
	case ai.TextEndEvent:
		return value.Partial, true
	case *ai.TextEndEvent:
		return value.Partial, true
	case ai.ThinkingStartEvent:
		return value.Partial, true
	case *ai.ThinkingStartEvent:
		return value.Partial, true
	case ai.ThinkingDeltaEvent:
		return value.Partial, true
	case *ai.ThinkingDeltaEvent:
		return value.Partial, true
	case ai.ThinkingEndEvent:
		return value.Partial, true
	case *ai.ThinkingEndEvent:
		return value.Partial, true
	case ai.ToolCallStartEvent:
		return value.Partial, true
	case *ai.ToolCallStartEvent:
		return value.Partial, true
	case ai.ToolCallDeltaEvent:
		return value.Partial, true
	case *ai.ToolCallDeltaEvent:
		return value.Partial, true
	case ai.ToolCallEndEvent:
		return value.Partial, true
	case *ai.ToolCallEndEvent:
		return value.Partial, true
	default:
		return nil, false
	}
}

type executedToolBatch struct {
	messages  []*ai.ToolResultMessage
	terminate bool
}

type preparedToolCall struct {
	toolCall         *ai.ToolCall
	tool             AgentTool
	args             any
	model            *ai.Model
	executionContext context.Context
	releaseExecution func()
	executionError   error
}

type finalizedToolCall struct {
	toolCall *ai.ToolCall
	result   AgentToolResult
	isError  bool
}

type toolEntry struct {
	finalized *finalizedToolCall
	prepared  *preparedToolCall
	err       error
}

func failTruncatedToolCalls(
	ctx context.Context,
	toolCalls []*ai.ToolCall,
	config AgentLoopConfig,
	emitter *eventEmitter,
) (executedToolBatch, error) {
	messages := make([]*ai.ToolResultMessage, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		if err := emitter.emit(ctx, NewToolExecutionStartEvent(toolCall)); err != nil {
			return executedToolBatch{}, err
		}
		finalized := finalizedToolCall{
			toolCall: toolCall,
			result: createErrorToolResult(fmt.Sprintf(
				`Tool call %q was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.`,
				toolCall.Name,
			)),
			isError: true,
		}
		if err := emitToolExecutionEnd(ctx, finalized, emitter); err != nil {
			return executedToolBatch{}, err
		}
		message, err := createToolResultMessage(finalized, config)
		if err != nil {
			return executedToolBatch{}, err
		}
		if err := emitToolResultMessage(ctx, message, emitter); err != nil {
			return executedToolBatch{}, err
		}
		messages = append(messages, message)
	}
	return executedToolBatch{messages: messages}, nil
}

func executeToolCalls(
	ctx context.Context,
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCalls []*ai.ToolCall,
	config AgentLoopConfig,
	emitter *eventEmitter,
) (executedToolBatch, error) {
	sequential := config.ToolExecution == ToolExecutionSequential
	if !sequential {
		for _, toolCall := range toolCalls {
			if tool := findAgentTool(currentContext.Tools, toolCall.Name); tool != nil && tool.Spec().ExecutionMode == ToolExecutionSequential {
				sequential = true
				break
			}
		}
	}
	if sequential {
		return executeToolCallsSequential(ctx, currentContext, assistantMessage, toolCalls, config, emitter)
	}
	return executeToolCallsParallel(ctx, currentContext, assistantMessage, toolCalls, config, emitter)
}

func executeToolCallsSequential(
	ctx context.Context,
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCalls []*ai.ToolCall,
	config AgentLoopConfig,
	emitter *eventEmitter,
) (executedToolBatch, error) {
	finalized := make([]finalizedToolCall, 0, len(toolCalls))
	messages := make([]*ai.ToolResultMessage, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		if err := emitter.emit(ctx, NewToolExecutionStartEvent(toolCall)); err != nil {
			return executedToolBatch{}, err
		}
		prepared, immediate := prepareToolCall(ctx, currentContext, assistantMessage, toolCall, config)
		var outcome finalizedToolCall
		if immediate != nil {
			outcome = *immediate
		} else {
			executed, err := executePreparedToolCall(ctx, prepared, emitter)
			if err != nil {
				return executedToolBatch{}, err
			}
			outcome = finalizeExecutedToolCall(ctx, currentContext, assistantMessage, prepared, executed, config)
		}
		if err := emitToolExecutionEnd(ctx, outcome, emitter); err != nil {
			return executedToolBatch{}, err
		}
		message, err := createToolResultMessage(outcome, config)
		if err != nil {
			return executedToolBatch{}, err
		}
		if err := emitToolResultMessage(ctx, message, emitter); err != nil {
			return executedToolBatch{}, err
		}
		finalized = append(finalized, outcome)
		messages = append(messages, message)
		if ctx.Err() != nil {
			break
		}
	}
	return executedToolBatch{messages: messages, terminate: shouldTerminate(finalized)}, nil
}

func executeToolCallsParallel(
	ctx context.Context,
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCalls []*ai.ToolCall,
	config AgentLoopConfig,
	emitter *eventEmitter,
) (executedToolBatch, error) {
	entries := make([]toolEntry, 0, len(toolCalls))
	for _, toolCall := range toolCalls {
		if err := emitter.emit(ctx, NewToolExecutionStartEvent(toolCall)); err != nil {
			return executedToolBatch{}, err
		}
		prepared, immediate := prepareToolCall(ctx, currentContext, assistantMessage, toolCall, config)
		if immediate != nil {
			if err := emitToolExecutionEnd(ctx, *immediate, emitter); err != nil {
				return executedToolBatch{}, err
			}
			entries = append(entries, toolEntry{finalized: immediate})
			if ctx.Err() != nil {
				break
			}
			continue
		}
		entries = append(entries, toolEntry{prepared: prepared})
		if ctx.Err() != nil {
			break
		}
	}

	for index := range entries {
		prepared := entries[index].prepared
		if prepared == nil {
			continue
		}
		preparer, ok := prepared.tool.(ParallelExecutionPreparer)
		if !ok {
			continue
		}
		executionContext, release, err := preparer.PrepareParallelExecution(ctx, prepared.args)
		if err != nil {
			prepared.executionError = err
			continue
		}
		prepared.executionContext = executionContext
		prepared.releaseExecution = release
	}

	// ponytail: upstream Promise.all's every prepared call at once
	// (agent-loop.ts:539); Go goroutines each own OS resources (a tool call can
	// fork subprocesses), so cap in-flight calls. Raise or drop the cap if a
	// model legitimately needs more than maxParallelToolCalls tools at once.
	slots := make(chan struct{}, maxParallelToolCalls)
	var wait sync.WaitGroup
	for index := range entries {
		if entries[index].prepared == nil {
			continue
		}
		wait.Add(1)
		go func(entry *toolEntry) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			executed, err := executePreparedToolCall(ctx, entry.prepared, emitter)
			if err != nil {
				entry.err = err
				return
			}
			outcome := finalizeExecutedToolCall(ctx, currentContext, assistantMessage, entry.prepared, executed, config)
			entry.finalized = &outcome
			entry.err = emitToolExecutionEnd(ctx, outcome, emitter)
		}(&entries[index])
	}
	wait.Wait()

	finalized := make([]finalizedToolCall, 0, len(entries))
	messages := make([]*ai.ToolResultMessage, 0, len(entries))
	for _, entry := range entries {
		if entry.err != nil {
			return executedToolBatch{}, entry.err
		}
		if entry.finalized == nil {
			return executedToolBatch{}, errors.New("agent: tool execution produced no outcome")
		}
		outcome := *entry.finalized
		message, err := createToolResultMessage(outcome, config)
		if err != nil {
			return executedToolBatch{}, err
		}
		if err := emitToolResultMessage(ctx, message, emitter); err != nil {
			return executedToolBatch{}, err
		}
		finalized = append(finalized, outcome)
		messages = append(messages, message)
	}
	return executedToolBatch{messages: messages, terminate: shouldTerminate(finalized)}, nil
}

func prepareToolCall(
	ctx context.Context,
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	toolCall *ai.ToolCall,
	config AgentLoopConfig,
) (*preparedToolCall, *finalizedToolCall) {
	tool := findAgentTool(currentContext.Tools, toolCall.Name)
	if tool == nil {
		outcome := finalizedToolCall{toolCall: toolCall, result: createErrorToolResult("Tool " + toolCall.Name + " not found"), isError: true}
		return nil, &outcome
	}

	spec := tool.Spec()
	args := ai.ToolCallArgumentsValue(toolCall)
	argumentJSON, err := ai.MarshalToolCallArguments(toolCall)
	if err == nil && spec.PrepareArguments != nil {
		originalArgs := args
		originalSnapshot := cloneJSONValue(originalArgs)
		args, err = spec.PrepareArguments(originalArgs)
		if err == nil && (!sameReference(originalArgs, args) || !reflect.DeepEqual(originalSnapshot, args)) {
			argumentJSON, err = ai.Marshal(args)
		}
	}
	if err == nil {
		args, err = jsonschema.ValidateToolArgumentsJSON(toolCall.Name, spec.Parameters, argumentJSON)
	}
	if err == nil && config.BeforeToolCall != nil {
		var before *BeforeToolCallResult
		before, err = config.BeforeToolCall(ctx, BeforeToolCallContext{
			AssistantMessage: assistantMessage, ToolCall: toolCall, Args: args, Context: currentContext,
		})
		if err == nil && ctx.Err() != nil {
			err = upstreamError("Operation aborted")
		}
		if err == nil && before != nil && before.Block {
			reason := before.Reason
			if reason == "" {
				reason = "Tool execution was blocked"
			}
			err = errors.New(reason)
		}
	}
	if err == nil && ctx.Err() != nil {
		err = upstreamError("Operation aborted")
	}
	if err != nil {
		outcome := finalizedToolCall{toolCall: toolCall, result: createErrorToolResult(err.Error()), isError: true}
		return nil, &outcome
	}
	return &preparedToolCall{toolCall: toolCall, tool: tool, args: args, model: cloneModel(config.Model)}, nil
}

func sameReference(left, right any) bool {
	leftValue := reflect.ValueOf(left)
	rightValue := reflect.ValueOf(right)
	if !leftValue.IsValid() || !rightValue.IsValid() || leftValue.Type() != rightValue.Type() {
		return false
	}
	switch leftValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice:
		return leftValue.Pointer() == rightValue.Pointer()
	default:
		return false
	}
}

type executedToolCall struct {
	result  AgentToolResult
	isError bool
}

func executePreparedToolCall(
	ctx context.Context,
	prepared *preparedToolCall,
	emitter *eventEmitter,
) (executedToolCall, error) {
	executionContext := ctx
	if prepared.executionContext != nil {
		executionContext = prepared.executionContext
	}
	executionContext = WithToolExecutionModel(executionContext, prepared.model)
	if prepared.releaseExecution != nil {
		defer prepared.releaseExecution()
	}
	var updateMu sync.Mutex
	acceptingUpdates := true
	updateState := &toolUpdateState{}
	// The lock keeps enqueue order well defined for tools that call onUpdate from
	// several goroutines; the queue, not the sink, is what the tool waits on.
	onUpdate := func(partial AgentToolResult) {
		updateMu.Lock()
		defer updateMu.Unlock()
		if !acceptingUpdates {
			return
		}
		event := NewToolExecutionUpdateEvent(prepared.toolCall, cloneAgentToolResult(partial))
		emitter.enqueueUpdate(executionContext, event, updateState)
	}

	var result AgentToolResult
	executeErr := prepared.executionError
	if executeErr == nil {
		result, executeErr = prepared.tool.Execute(executionContext, prepared.toolCall.ID, prepared.args, onUpdate)
	}
	updateMu.Lock()
	acceptingUpdates = false
	updateMu.Unlock()
	emitErr := emitter.flushUpdates(updateState)
	if emitErr != nil {
		return executedToolCall{}, emitErr
	}
	if executeErr != nil {
		return executedToolCall{result: createErrorToolResult(executeErr.Error()), isError: true}, nil
	}
	return executedToolCall{result: result, isError: result.IsError}, nil
}

func finalizeExecutedToolCall(
	ctx context.Context,
	currentContext *AgentContext,
	assistantMessage *ai.AssistantMessage,
	prepared *preparedToolCall,
	executed executedToolCall,
	config AgentLoopConfig,
) finalizedToolCall {
	result := executed.result
	isError := executed.isError
	if config.AfterToolCall != nil {
		after, err := config.AfterToolCall(ctx, AfterToolCallContext{
			AssistantMessage: assistantMessage,
			ToolCall:         prepared.toolCall,
			Args:             prepared.args,
			Result:           result,
			IsError:          isError,
			Context:          currentContext,
		})
		if err != nil {
			result = createErrorToolResult(err.Error())
			isError = true
		} else if after != nil {
			if after.StructuredContent != nil {
				result.StructuredContent = after.StructuredContent
			} else if after.Content != nil {
				result.StructuredContent = nil
			}
			if after.Content != nil {
				result.Content = after.Content
			}
			if after.DetailsSet || after.Details != nil {
				result.Details = after.Details
			}
			if after.Usage != nil {
				result.Usage = after.Usage
			}
			if after.Terminate != nil {
				result.Terminate = after.Terminate
			}
			if after.IsError != nil {
				isError = *after.IsError
			}
		}
	}
	return finalizedToolCall{toolCall: prepared.toolCall, result: result, isError: isError}
}

// AgentToolCallOutcome is how one tool call ended.
type AgentToolCallOutcome struct {
	ToolCall *ai.ToolCall
	Result   AgentToolResult
	IsError  bool
}

// RunToolCallOptions run one tool call outside the loop, as ExecuteTool does:
// the call goes through argument preparation, validation and the hooks, against
// Tools.
type RunToolCallOptions struct {
	Tools            []AgentTool
	AssistantMessage *ai.AssistantMessage
	Context          AgentContext
	Model            *ai.Model
	BeforeToolCall   BeforeToolCallFunc
	AfterToolCall    AfterToolCallFunc
	OnUpdate         AgentToolUpdateCallback
}

// RunToolCall runs one tool call. Failures (unknown tools, validation errors,
// blocked calls, tool errors) come back as an error outcome, never an error.
func RunToolCall(ctx context.Context, toolCall *ai.ToolCall, options RunToolCallOptions) AgentToolCallOutcome {
	currentContext := options.Context
	currentContext.Tools = options.Tools
	config := AgentLoopConfig{Model: options.Model, BeforeToolCall: options.BeforeToolCall, AfterToolCall: options.AfterToolCall}
	prepared, immediate := prepareToolCall(ctx, &currentContext, options.AssistantMessage, toolCall, config)
	if immediate != nil {
		return AgentToolCallOutcome{ToolCall: toolCall, Result: immediate.result, IsError: true}
	}
	onUpdate := options.OnUpdate
	if onUpdate == nil {
		onUpdate = func(AgentToolResult) {}
	}
	result, err := prepared.tool.Execute(WithToolExecutionModel(ctx, prepared.model), toolCall.ID, prepared.args, onUpdate)
	executed := executedToolCall{result: result, isError: result.IsError}
	if err != nil {
		executed = executedToolCall{result: createErrorToolResult(err.Error()), isError: true}
	}
	finalized := finalizeExecutedToolCall(ctx, &currentContext, options.AssistantMessage, prepared, executed, config)
	return AgentToolCallOutcome{ToolCall: toolCall, Result: finalized.result, IsError: finalized.isError}
}

func createErrorToolResult(message string) AgentToolResult {
	return AgentToolResult{
		Content: ai.ToolResultContent{&ai.TextContent{Text: message}},
		Details: map[string]any{},
	}
}

func emitToolExecutionEnd(ctx context.Context, finalized finalizedToolCall, emitter *eventEmitter) error {
	return emitter.emit(ctx, ToolExecutionEndEvent{
		ToolCallID: finalized.toolCall.ID,
		ToolName:   finalized.toolCall.Name,
		Result:     finalized.result,
		IsError:    finalized.isError,
	})
}

func createToolResultMessage(finalized finalizedToolCall, config AgentLoopConfig) (*ai.ToolResultMessage, error) {
	content := finalized.result.Content
	if content == nil {
		content = ai.ToolResultContent{}
	}
	var details json.RawMessage
	if finalized.result.Details != nil {
		encoded, err := ai.Marshal(finalized.result.Details)
		if err != nil {
			return nil, err
		}
		details = encoded
	}
	var addedToolNames *[]string
	if finalized.result.AddedToolNames != nil && len(*finalized.result.AddedToolNames) > 0 {
		names := append([]string(nil), (*finalized.result.AddedToolNames)...)
		addedToolNames = &names
	}
	return &ai.ToolResultMessage{
		ToolCallID:     finalized.toolCall.ID,
		ToolName:       finalized.toolCall.Name,
		Content:        content,
		Details:        details,
		Usage:          finalized.result.Usage,
		AddedToolNames: addedToolNames,
		IsError:        finalized.isError,
		Timestamp:      loopNow(config),
	}, nil
}

func emitToolResultMessage(ctx context.Context, message *ai.ToolResultMessage, emitter *eventEmitter) error {
	if err := emitter.emit(ctx, MessageStartEvent{Message: message}); err != nil {
		return err
	}
	return emitter.emit(ctx, MessageEndEvent{Message: message})
}

func shouldTerminate(finalized []finalizedToolCall) bool {
	if len(finalized) == 0 {
		return false
	}
	for _, outcome := range finalized {
		if outcome.result.Terminate == nil || !*outcome.result.Terminate {
			return false
		}
	}
	return true
}

func assistantToolCalls(message *ai.AssistantMessage) []*ai.ToolCall {
	toolCalls := make([]*ai.ToolCall, 0)
	for _, block := range message.Content {
		if toolCall, ok := block.(*ai.ToolCall); ok {
			toolCalls = append(toolCalls, toolCall)
		}
	}
	return toolCalls
}

func findAgentTool(tools []AgentTool, name string) AgentTool {
	for _, tool := range tools {
		if tool != nil && tool.Spec().Name == name {
			return tool
		}
	}
	return nil
}

func queuedMessages(ctx context.Context, getter GetQueuedMessagesFunc) (AgentMessages, error) {
	if getter == nil {
		return nil, nil
	}
	messages, err := getter(ctx)
	if messages == nil {
		messages = AgentMessages{}
	}
	return messages, err
}

func defaultConvertToLLM(messages AgentMessages) ai.MessageList {
	converted := make(ai.MessageList, 0, len(messages))
	for _, message := range messages {
		if standard, ok := message.(ai.Message); ok {
			converted = append(converted, standard)
		}
	}
	return converted
}

func declarePromptAndToolChanges(context AgentContext, pending AgentMessages, timestamp int64) AgentMessages {
	baseline := append(AgentMessages(nil), pending...)
	lastSystem := -1
	for index := len(baseline) - 1; index >= 0; index-- {
		if _, ok := baseline[index].(*ai.SystemMessage); ok {
			lastSystem = index
			break
		}
	}
	withoutPendingChanges := append(AgentMessages(nil), baseline...)
	if lastSystem >= 0 {
		if message, ok := withoutPendingChanges[lastSystem].(*ai.SystemMessage); ok {
			copy := *message
			copy.ToolsAdded = nil
			copy.ToolsRemoved = nil
			withoutPendingChanges[lastSystem] = &copy
		}
	}
	all := append(append(AgentMessages(nil), context.Messages...), withoutPendingChanges...)
	previous := ai.CurrentTools(agentMessagesToAI(all))
	current := make([]ai.Tool, 0, len(context.Tools))
	for _, tool := range context.Tools {
		spec := tool.Spec()
		current = append(current, ai.Tool{Name: spec.Name, Description: spec.Description, Parameters: spec.Parameters, ConstrainedSampling: spec.ConstrainedSampling})
	}
	added, removed := ai.ToolStateChanges(previous, current)
	if lastSystem >= 0 {
		message, _ := baseline[lastSystem].(*ai.SystemMessage)
		if message != nil && (len(added) > 0 || len(removed) > 0 || len(message.ToolsAdded) > 0 || len(message.ToolsRemoved) > 0) {
			baseline[lastSystem] = ai.WithToolStateChanges(message, added, removed)
		}
		return baseline
	}
	if len(added) == 0 && len(removed) == 0 {
		return baseline
	}
	update := ai.NewToolStateSystemMessage(timestamp, added, removed)
	insert := firstNonSystemIndex(baseline)
	baseline = append(baseline, nil)
	copy(baseline[insert+1:], baseline[insert:])
	baseline[insert] = update
	return baseline
}

func firstNonSystemIndex(messages AgentMessages) int {
	for index, message := range messages {
		if _, ok := message.(*ai.SystemMessage); !ok {
			return index
		}
	}
	return len(messages)
}

func agentMessagesToAI(messages AgentMessages) ai.MessageList {
	result := make(ai.MessageList, 0, len(messages))
	for _, message := range messages {
		if typed, ok := message.(ai.Message); ok {
			result = append(result, typed)
		}
	}
	return result
}

func shallowAssistantCopy(message *ai.AssistantMessage) *ai.AssistantMessage {
	if message == nil {
		return nil
	}
	copy := *message
	return &copy
}

func copyAgentContext(source AgentContext) AgentContext {
	return AgentContext{
		SystemPrompt: source.SystemPrompt,
		Messages:     append(AgentMessages(nil), source.Messages...),
		Tools:        cloneAgentTools(source.Tools),
	}
}

func agentMessageRole(message AgentMessage) string {
	switch message.(type) {
	case *ai.UserMessage:
		return "user"
	case *ai.AssistantMessage:
		return "assistant"
	case *ai.ToolResultMessage:
		return "toolResult"
	}
	data, err := ai.Marshal(message)
	if err != nil {
		return ""
	}
	var header struct {
		Role string `json:"role"`
	}
	_ = json.Unmarshal(data, &header)
	return header.Role
}

func loopNow(config AgentLoopConfig) int64 {
	if config.Now != nil {
		return config.Now()
	}
	return time.Now().UnixMilli()
}

// upstreamError retains public compatibility strings whose capitalization is
// fixed by the TypeScript API.
func upstreamError(message string) error { return errors.New(message) }

type eventEmitter struct {
	sink EventSink
	// The update queue and its drain goroutine start with the first tool
	// update: most runs have none.
	start   sync.Once
	updates chan queuedUpdate
	drained chan struct{}
}

// toolUpdateQueueDepth bounds pending tool_execution_update events. Producers
// block once it fills rather than dropping an update, so a sink that stalls
// longer than this many updates does eventually reach back into the tool.
const toolUpdateQueueDepth = 256

// queuedUpdate is either an event to deliver or, when flush is set, a barrier
// the producer waits on.
type queuedUpdate struct {
	ctx   context.Context
	event AgentEvent
	state *toolUpdateState
	flush chan struct{}
}

// toolUpdateState collects sink failures per tool call, matching upstream's
// per-call updateEvents array (agent-loop.ts:673).
type toolUpdateState struct {
	mu       sync.Mutex
	err      error
	enqueued bool
}

func (state *toolUpdateState) record(err error) {
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.err == nil {
		state.err = err
	}
}

func (state *toolUpdateState) failure() error {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.err
}

func newEventEmitter(sink EventSink) *eventEmitter {
	return &eventEmitter{sink: sink}
}

// startDrain runs one drain goroutine per run: upstream's emit call is
// synchronous but its subscriber runs on the event loop, so updates stay
// totally ordered and never overlap while a slow sink cannot block the tool
// (agent-loop.ts:679-692).
func (emitter *eventEmitter) startDrain() {
	emitter.updates = make(chan queuedUpdate, toolUpdateQueueDepth)
	emitter.drained = make(chan struct{})
	go func() {
		defer close(emitter.drained)
		for item := range emitter.updates {
			if item.flush != nil {
				close(item.flush)
				continue
			}
			if err := emitter.emit(item.ctx, item.event); err != nil {
				item.state.record(err)
			}
		}
	}()
}

// close stops the drain goroutine after every queued update has been delivered.
func (emitter *eventEmitter) close() {
	started := true
	emitter.start.Do(func() { started = false })
	if started {
		close(emitter.updates)
		<-emitter.drained
	}
}

func (emitter *eventEmitter) enqueueUpdate(ctx context.Context, event AgentEvent, state *toolUpdateState) {
	emitter.start.Do(emitter.startDrain)
	if emitter.updates == nil {
		panic("engine: tool update after its run ended")
	}
	state.mu.Lock()
	state.enqueued = true
	state.mu.Unlock()
	emitter.updates <- queuedUpdate{ctx: ctx, event: event, state: state}
}

// flushUpdates returns once every update this call enqueued has been delivered,
// so a tool's updates always precede its tool_execution_end.
func (emitter *eventEmitter) flushUpdates(state *toolUpdateState) error {
	state.mu.Lock()
	enqueued := state.enqueued
	state.mu.Unlock()
	if !enqueued {
		return state.failure()
	}
	barrier := make(chan struct{})
	emitter.updates <- queuedUpdate{flush: barrier}
	<-barrier
	return state.failure()
}

func (emitter *eventEmitter) emit(ctx context.Context, event AgentEvent) error {
	if emitter.sink == nil {
		return nil
	}
	return emitter.sink(ctx, event)
}
