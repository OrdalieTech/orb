package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

var (
	errNoModel    = errors.New("agent: loop requires a model")
	errNoStreamFn = errors.New("agent: loop requires a stream function")
)

// RunLoop starts a loop with prompt messages and returns only messages created
// by this invocation. The caller-owned context is copied before it is changed.
func RunLoop(
	ctx context.Context,
	prompts AgentMessages,
	loopContext AgentContext,
	config AgentLoopConfig,
	sink EventSink,
) (AgentMessages, error) {
	current := copyAgentContext(loopContext)
	current.Messages = append(current.Messages, prompts...)
	newMessages := append(AgentMessages(nil), prompts...)
	emitter := newEventEmitter(sink)

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

	if err := runLoop(ctx, &current, &newMessages, config, emitter); err != nil {
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
) (AgentMessages, error) {
	if loopContext == nil || len(loopContext.Messages) == 0 {
		return nil, upstreamError("Cannot continue: no messages in context")
	}
	if agentMessageRole(loopContext.Messages[len(loopContext.Messages)-1]) == "assistant" {
		return nil, upstreamError("Cannot continue from message role: assistant")
	}

	newMessages := AgentMessages{}
	emitter := newEventEmitter(sink)
	if err := emitter.emit(ctx, AgentStartEvent{}); err != nil {
		return nil, err
	}
	if err := emitter.emit(ctx, TurnStartEvent{}); err != nil {
		return nil, err
	}
	if err := runLoop(ctx, loopContext, &newMessages, config, emitter); err != nil {
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
) error {
	firstTurn := true
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
			message, err := streamAssistantResponse(ctx, currentContext, config, emitter)
			if err != nil {
				return err
			}
			*newMessages = append(*newMessages, message)

			if message.StopReason == ai.StopReasonError || message.StopReason == ai.StopReasonAborted {
				if err := emitter.emit(ctx, TurnEndEvent{Message: message, ToolResults: []*ai.ToolResultMessage{}}); err != nil {
					return err
				}
				return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
			}

			toolCalls := assistantToolCalls(message)
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

			if err := emitter.emit(ctx, TurnEndEvent{Message: message, ToolResults: toolResults}); err != nil {
				return err
			}
			nextContext := ShouldStopAfterTurnContext{
				Message: message, ToolResults: toolResults, Context: currentContext, NewMessages: *newMessages,
			}
			if config.PrepareNextTurn != nil {
				update, updateErr := config.PrepareNextTurn(ctx, nextContext)
				if updateErr != nil {
					return updateErr
				}
				if update != nil {
					if update.Context != nil {
						currentContext = update.Context
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
				}
			}
			if config.ShouldStopAfterTurn != nil {
				stop, stopErr := config.ShouldStopAfterTurn(ctx, ShouldStopAfterTurnContext{
					Message: message, ToolResults: toolResults, Context: currentContext, NewMessages: *newMessages,
				})
				if stopErr != nil {
					return stopErr
				}
				if stop {
					return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
				}
			}

			pendingMessages, err = queuedMessages(ctx, config.GetSteeringMessages)
			if err != nil {
				return err
			}
		}

		pendingMessages, err = queuedMessages(ctx, config.GetFollowUpMessages)
		if err != nil {
			return err
		}
		if len(pendingMessages) == 0 {
			break
		}
	}

	return emitter.emit(ctx, AgentEndEvent{Messages: *newMessages})
}

func streamAssistantResponse(
	ctx context.Context,
	loopContext *AgentContext,
	config AgentLoopConfig,
	emitter *eventEmitter,
) (*ai.AssistantMessage, error) {
	if config.Model == nil {
		return nil, errNoModel
	}
	if config.StreamFn == nil {
		return nil, errNoStreamFn
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

	llmContext := ai.Context{SystemPrompt: &loopContext.SystemPrompt, Messages: llmMessages}
	if loopContext.Tools != nil {
		tools := make([]ai.Tool, 0, len(loopContext.Tools))
		for _, tool := range loopContext.Tools {
			spec := tool.Spec()
			tools = append(tools, ai.Tool{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters})
		}
		llmContext.Tools = &tools
	}

	requestModel := cloneModel(config.Model)
	options := config.SimpleStreamOptions
	if config.GetAPIKey != nil {
		key, keyErr := config.GetAPIKey(ctx, requestModel.Provider)
		if keyErr != nil {
			return nil, keyErr
		}
		if key != nil && *key != "" {
			options.APIKey = key
		}
	}
	if config.GetModelHeaders != nil {
		headers, headerErr := config.GetModelHeaders(ctx, requestModel, options.APIKey)
		if headerErr != nil {
			return nil, headerErr
		}
		requestModel.Headers = mergeRequestHeaders(requestModel.Headers, headers)
	}
	stream, err := config.StreamFn(ctx, requestModel, llmContext, &options)
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
			return finishAssistantResponse(ctx, loopContext, value.Message, addedPartial, emitter)
		case *ai.DoneEvent:
			return finishAssistantResponse(ctx, loopContext, value.Message, addedPartial, emitter)
		case ai.ErrorEvent:
			return finishAssistantResponse(ctx, loopContext, value.Error, addedPartial, emitter)
		case *ai.ErrorEvent:
			return finishAssistantResponse(ctx, loopContext, value.Error, addedPartial, emitter)
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
) (*ai.AssistantMessage, error) {
	if message == nil {
		return nil, ai.ErrStreamIncomplete
	}
	if addedPartial {
		loopContext.Messages[len(loopContext.Messages)-1] = message
	} else {
		loopContext.Messages = append(loopContext.Messages, message)
		if err := emitter.emit(ctx, MessageStartEvent{Message: shallowAssistantCopy(message)}); err != nil {
			return nil, err
		}
	}
	if err := emitter.emit(ctx, MessageEndEvent{Message: message}); err != nil {
		return nil, err
	}
	return message, nil
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

	var wait sync.WaitGroup
	for index := range entries {
		if entries[index].prepared == nil {
			continue
		}
		wait.Add(1)
		go func(entry *toolEntry) {
			defer wait.Done()
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
	args := any(toolCall.Arguments)
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
	return &preparedToolCall{toolCall: toolCall, tool: tool, args: args}, nil
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
	if prepared.releaseExecution != nil {
		defer prepared.releaseExecution()
	}
	var updateWait sync.WaitGroup
	var updateMu sync.Mutex
	acceptingUpdates := true
	var updateErr error
	onUpdate := func(partial AgentToolResult) {
		updateMu.Lock()
		if !acceptingUpdates {
			updateMu.Unlock()
			return
		}
		event := NewToolExecutionUpdateEvent(prepared.toolCall, cloneAgentToolResult(partial))
		updateWait.Add(1)
		updateMu.Unlock()
		go func() {
			defer updateWait.Done()
			if err := emitter.emit(executionContext, event); err != nil {
				updateMu.Lock()
				if updateErr == nil {
					updateErr = err
				}
				updateMu.Unlock()
			}
		}()
	}

	var result AgentToolResult
	executeErr := prepared.executionError
	if executeErr == nil {
		result, executeErr = prepared.tool.Execute(executionContext, prepared.toolCall.ID, prepared.args, onUpdate)
	}
	updateMu.Lock()
	acceptingUpdates = false
	updateMu.Unlock()
	updateWait.Wait()
	updateMu.Lock()
	emitErr := updateErr
	updateMu.Unlock()
	if emitErr != nil {
		return executedToolCall{}, emitErr
	}
	if executeErr != nil {
		return executedToolCall{result: createErrorToolResult(executeErr.Error()), isError: true}, nil
	}
	return executedToolCall{result: result}, nil
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
			if after.Content != nil {
				result.Content = after.Content
			}
			if after.Details != nil {
				result.Details = after.Details
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
}

func newEventEmitter(sink EventSink) *eventEmitter {
	return &eventEmitter{sink: sink}
}

func (emitter *eventEmitter) emit(ctx context.Context, event AgentEvent) error {
	if emitter.sink == nil {
		return nil
	}
	return emitter.sink(ctx, event)
}
