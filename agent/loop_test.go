package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

type loopResponseQueue struct {
	mu       sync.Mutex
	messages []*ai.AssistantMessage
	contexts []ai.Context
}

func (queue *loopResponseQueue) stream(
	_ context.Context,
	_ *ai.Model,
	requestContext ai.Context,
	_ *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	queue.mu.Lock()
	defer queue.mu.Unlock()
	queue.contexts = append(queue.contexts, requestContext)
	if len(queue.messages) == 0 {
		return nil, errors.New("no scripted response")
	}
	message := queue.messages[0]
	queue.messages = queue.messages[1:]
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		yield(ai.DoneEvent{Reason: message.StopReason, Message: message}, nil)
	}, nil
}

func TestRunLoopParallelCompletionAndSourceOrder(t *testing.T) {
	firstCall := &ai.ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"value": "first"}}
	secondCall := &ai.ToolCall{ID: "call-2", Name: "echo", Arguments: map[string]any{"value": "second"}}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, firstCall, secondCall),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"}),
	}}
	secondFinished := make(chan struct{})
	var secondFinishedOnce sync.Once
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "echo",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, _ string, params any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			value := params.(map[string]any)["value"].(string)
			if value == "first" {
				<-secondFinished
			}
			return textToolResult("echo:" + value), nil
		},
	}

	var endOrder []string
	var resultOrder []string
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{
		SystemPrompt: "echo twice", Tools: []AgentTool{tool},
	}, AgentLoopConfig{
		Model:         loopModel(),
		StreamFn:      responses.stream,
		ToolExecution: ToolExecutionParallel,
		Now:           func() int64 { return 1234 },
	}, func(_ context.Context, event AgentEvent) error {
		switch value := event.(type) {
		case ToolExecutionEndEvent:
			endOrder = append(endOrder, value.ToolCallID)
			if value.ToolCallID == "call-2" {
				secondFinishedOnce.Do(func() { close(secondFinished) })
			}
		case MessageEndEvent:
			if result, ok := value.Message.(*ai.ToolResultMessage); ok {
				resultOrder = append(resultOrder, result.ToolCallID)
				if result.Timestamp != 1234 {
					t.Fatalf("tool result timestamp = %d", result.Timestamp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := joinStrings(endOrder), "call-2,call-1"; got != want {
		t.Fatalf("tool end order = %s, want %s", got, want)
	}
	if got, want := joinStrings(resultOrder), "call-1,call-2"; got != want {
		t.Fatalf("tool result order = %s, want %s", got, want)
	}
	if len(responses.contexts) != 2 {
		t.Fatalf("provider calls = %d", len(responses.contexts))
	}
	secondContext := responses.contexts[1]
	if got := len(secondContext.Messages); got != 4 {
		t.Fatalf("second request messages = %d, want 4", got)
	}
	for index, id := range []string{"call-1", "call-2"} {
		result, ok := secondContext.Messages[index+2].(*ai.ToolResultMessage)
		if !ok || result.ToolCallID != id {
			t.Fatalf("second request result %d = %#v", index, secondContext.Messages[index+2])
		}
	}
}

func TestRunLoopHooksMutateWithoutRevalidationAndIgnoreLateUpdates(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "mutate", Arguments: map[string]any{"value": "valid"}}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, call),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"}),
	}}
	var late AgentToolUpdateCallback
	var executed any
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "mutate",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, _ string, params any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			executed = params.(map[string]any)["value"]
			update(textToolResult("working"))
			late = update
			return textToolResult("complete"), nil
		},
	}
	updates := 0
	var final ToolExecutionEndEvent
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model:    loopModel(),
		StreamFn: responses.stream,
		BeforeToolCall: func(_ context.Context, hook BeforeToolCallContext) (*BeforeToolCallResult, error) {
			hook.Args.(map[string]any)["value"] = 17
			return nil, nil
		},
		AfterToolCall: func(_ context.Context, hook AfterToolCallContext) (*AfterToolCallResult, error) {
			if hook.Args.(map[string]any)["value"] != 17 {
				t.Fatalf("after hook args = %#v", hook.Args)
			}
			isError := true
			return &AfterToolCallResult{Content: ai.ToolResultContent{}, IsError: &isError}, nil
		},
	}, func(_ context.Context, event AgentEvent) error {
		switch value := event.(type) {
		case ToolExecutionUpdateEvent:
			updates++
		case ToolExecutionEndEvent:
			final = value
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executed != 17 {
		t.Fatalf("executed args = %#v", executed)
	}
	if updates != 1 {
		t.Fatalf("updates = %d, want 1", updates)
	}
	if !final.IsError || final.Result.Content == nil || len(final.Result.Content) != 0 {
		t.Fatalf("finalized result = %#v", final)
	}
	late(textToolResult("too late"))
	if updates != 1 {
		t.Fatalf("late update was emitted, updates = %d", updates)
	}
}

func TestRunLoopLengthStopFailsEveryToolWithoutExecuting(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "danger", Arguments: map[string]any{"value": "partial"}}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonLength, call),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "recovered"}),
	}}
	executions := 0
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "danger", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
			executions++
			return textToolResult("bad"), nil
		},
	}
	var result AgentToolResult
	var isError bool
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(), StreamFn: responses.stream,
	}, func(_ context.Context, event AgentEvent) error {
		if end, ok := event.(ToolExecutionEndEvent); ok {
			result, isError = end.Result, end.IsError
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("tool executions = %d", executions)
	}
	if !isError {
		t.Fatal("truncated tool result was not an error")
	}
	text := result.Content[0].(*ai.TextContent).Text
	want := `Tool call "danger" was not executed: the response hit the output token limit, so its arguments may be truncated. Re-issue the tool call with complete arguments.`
	if text != want {
		t.Fatalf("error text = %q", text)
	}
}

func TestRunLoopContinueValidation(t *testing.T) {
	config := AgentLoopConfig{Model: loopModel(), StreamFn: (&loopResponseQueue{}).stream}
	if _, err := RunLoopContinue(context.Background(), &AgentContext{}, config, nil); err == nil || err.Error() != "Cannot continue: no messages in context" {
		t.Fatalf("empty continuation error = %v", err)
	}
	if _, err := RunLoopContinue(context.Background(), &AgentContext{Messages: AgentMessages{loopAssistant(ai.StopReasonStop)}}, config, nil); err == nil || err.Error() != "Cannot continue from message role: assistant" {
		t.Fatalf("assistant continuation error = %v", err)
	}
}

func TestRunLoopContinueAppendsToCallerContextLikeUpstream(t *testing.T) {
	response := loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"})
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{response}}
	loopContext := &AgentContext{Messages: AgentMessages{loopUser("existing")}, Tools: []AgentTool{}}
	newMessages, err := RunLoopContinue(context.Background(), loopContext, AgentLoopConfig{
		Model: loopModel(), StreamFn: responses.stream,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(newMessages) != 1 || len(loopContext.Messages) != 2 || loopContext.Messages[1] != response {
		t.Fatalf("new = %#v, caller context = %#v", newMessages, loopContext.Messages)
	}
}

func TestRunLoopEmptyResolvedAPIKeyFallsBackToConfiguredKey(t *testing.T) {
	fallback := "configured-key"
	empty := ""
	response := loopAssistant(ai.StopReasonStop)
	var received *string
	stream := func(
		_ context.Context,
		_ *ai.Model,
		_ ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		received = options.APIKey
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: response}, nil)
		}, nil
	}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{}, AgentLoopConfig{
		SimpleStreamOptions: ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &fallback}},
		Model:               loopModel(),
		StreamFn:            stream,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			return &empty, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if received == nil || *received != fallback {
		t.Fatalf("received API key = %v", received)
	}
}

func TestRunLoopToolUpdateDoesNotBlockToolExecution(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "update", Arguments: map[string]any{}}),
		loopAssistant(ai.StopReasonStop),
	}}
	toolReturned := make(chan struct{})
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "update", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			update(textToolResult("working"))
			close(toolReturned)
			return textToolResult("done"), nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
			Model: loopModel(), StreamFn: responses.stream,
		}, func(_ context.Context, event AgentEvent) error {
			if _, ok := event.(ToolExecutionUpdateEvent); ok {
				<-toolReturned
			}
			return nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("tool update blocked tool execution")
	}
}

func TestRunLoopParallelEventSinksMayOverlap(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1", Name: "parallel", Arguments: map[string]any{"value": "first"}},
			&ai.ToolCall{ID: "call-2", Name: "parallel", Arguments: map[string]any{"value": "second"}},
		),
		loopAssistant(ai.StopReasonStop),
	}}
	allowSecond := make(chan struct{})
	secondEnded := make(chan struct{})
	var allowSecondOnce sync.Once
	var secondEndedOnce sync.Once
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "parallel",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, _ string, params any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			if params.(map[string]any)["value"] == "second" {
				<-allowSecond
			}
			return textToolResult("done"), nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
			Model: loopModel(), StreamFn: responses.stream,
		}, func(_ context.Context, event AgentEvent) error {
			end, ok := event.(ToolExecutionEndEvent)
			if !ok {
				return nil
			}
			if end.ToolCallID == "call-1" {
				allowSecondOnce.Do(func() { close(allowSecond) })
				<-secondEnded
			} else {
				secondEndedOnce.Do(func() { close(secondEnded) })
			}
			return nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel event sinks were globally serialized")
	}
}

func TestRunLoopIdentityPreparePreservesRawArgumentOrderInValidationError(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "ordered"}
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"z":1,"a":2}`)); err != nil {
		t.Fatal(err)
	}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, call),
		loopAssistant(ai.StopReasonStop),
	}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name: "ordered",
			Parameters: jsonschema.Schema(
				`{"type":"object","required":["missing"],"properties":{"z":{"type":"number"},"a":{"type":"number"}}}`,
			),
			PrepareArguments: func(args any) (any, error) { return args, nil },
		},
		Run: func(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
			t.Fatal("invalid tool arguments were executed")
			return AgentToolResult{}, nil
		},
	}
	var errorText string
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(), StreamFn: responses.stream,
	}, func(_ context.Context, event AgentEvent) error {
		if end, ok := event.(ToolExecutionEndEvent); ok {
			errorText = end.Result.Content[0].(*ai.TextContent).Text
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	zIndex := strings.Index(errorText, `"z": 1`)
	aIndex := strings.Index(errorText, `"a": 2`)
	if zIndex < 0 || aIndex < 0 || zIndex >= aIndex {
		t.Fatalf("validation diagnostic lost raw order:\n%s", errorText)
	}
}

func TestRunLoopToolUpdatesStartIndependentlyAndOwnTheirPayloads(t *testing.T) {
	type updateLabels []string
	type updateDetails map[string]updateLabels
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "update", Arguments: map[string]any{}}),
		loopAssistant(ai.StopReasonStop),
	}}
	secondStarted := make(chan struct{})
	mutated := make(chan struct{})
	var secondStartedOnce sync.Once
	partial := textToolResult("first")
	partial.Details = updateDetails{"phase": updateLabels{"first"}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "update", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			update(partial)
			update(textToolResult("second"))
			partial.Content[0].(*ai.TextContent).Text = "mutated"
			partial.Details.(updateDetails)["phase"][0] = "mutated"
			close(mutated)
			return textToolResult("done"), nil
		},
	}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(), StreamFn: responses.stream,
	}, func(_ context.Context, event AgentEvent) error {
		update, ok := event.(ToolExecutionUpdateEvent)
		if !ok {
			return nil
		}
		text := update.PartialResult.Content[0].(*ai.TextContent).Text
		if text == "second" {
			secondStartedOnce.Do(func() { close(secondStarted) })
			return nil
		}
		select {
		case <-secondStarted:
		case <-time.After(time.Second):
			t.Fatal("successive update events were serialized")
		}
		<-mutated
		if text != "first" || update.PartialResult.Details.(updateDetails)["phase"][0] != "first" {
			t.Fatalf("first update was mutated after callback return: %#v", update.PartialResult)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunLoopIgnoresSettledToolUpdateWhileSiblingRuns(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1", Name: "settled", Arguments: map[string]any{}},
			&ai.ToolCall{ID: "call-2", Name: "slow", Arguments: map[string]any{}},
		),
		loopAssistant(ai.StopReasonStop),
	}}
	var late AgentToolUpdateCallback
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	settledEnded := make(chan struct{})
	updateSeen := make(chan struct{}, 1)
	settled := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "settled", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			late = update
			return textToolResult("done"), nil
		},
	}
	slow := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "slow", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
			close(slowStarted)
			<-releaseSlow
			return textToolResult("done"), nil
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{settled, slow}}, AgentLoopConfig{
			Model: loopModel(), StreamFn: responses.stream,
		}, func(_ context.Context, event AgentEvent) error {
			switch value := event.(type) {
			case ToolExecutionEndEvent:
				if value.ToolCallID == "call-1" {
					close(settledEnded)
				}
			case ToolExecutionUpdateEvent:
				updateSeen <- struct{}{}
			}
			return nil
		})
		done <- err
	}()
	<-slowStarted
	<-settledEnded
	late(textToolResult("late"))
	select {
	case <-updateSeen:
		t.Fatal("settled tool emitted a late update")
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseSlow)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("parallel run did not settle")
	}
}

func loopModel() *ai.Model {
	return &ai.Model{ID: "test-model", API: ai.API("test"), Provider: ai.ProviderID("test")}
}

func loopUser(text string) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.NewUserText(text), Timestamp: 1}
}

func loopAssistant(reason ai.StopReason, content ...ai.AssistantContentBlock) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content: content, API: ai.API("test"), Provider: ai.ProviderID("test"), Model: "test-model",
		Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: reason, Timestamp: 2,
	}
}

func textToolResult(text string) AgentToolResult {
	return AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}, Details: map[string]any{}}
}

func joinStrings(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += ","
		}
		result += value
	}
	return result
}
