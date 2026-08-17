package engine

import (
	"context"
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonschema"
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

func TestRunLoopRecoversProviderDeclaredToolUseWithoutCalls(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.TextContent{Text: "Let me inspect that."}),
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"value": "checked"}}),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"}),
	}}
	executions := 0
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "echo",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, _ string, params any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			executions++
			return textToolResult("echo:" + params.(map[string]any)["value"].(string)), nil
		},
	}

	messages, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(),
		Now:   func() int64 { return 1234 },
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("tool executions = %d, want 1", executions)
	}
	if got := len(responses.contexts); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}

	retryContext := responses.contexts[1].Messages
	if got := len(retryContext); got != 3 {
		t.Fatalf("retry context messages = %d, want 3", got)
	}
	interim, ok := retryContext[1].(*ai.AssistantMessage)
	if !ok || interim.StopReason != ai.StopReasonToolUse || len(assistantToolCalls(interim)) != 0 {
		t.Fatalf("retry interim assistant = %#v", retryContext[1])
	}
	nudge, ok := retryContext[2].(*ai.UserMessage)
	if !ok || nudge.Content.Text == nil || !strings.Contains(strings.ToLower(*nudge.Content.Text), "tool call") {
		t.Fatalf("retry nudge = %#v", retryContext[2])
	}

	postToolContext := responses.contexts[2].Messages
	if got := len(postToolContext); got != 3 {
		t.Fatalf("post-tool context messages = %d, want 3; recovery scaffold leaked: %#v", got, postToolContext)
	}
	if got := len(messages); got != 4 {
		t.Fatalf("returned messages = %d, want prompt + recovered tool turn + result + final", got)
	}
	for _, message := range messages {
		if user, ok := message.(*ai.UserMessage); ok && user.Content.Text != nil && strings.Contains(*user.Content.Text, "previous turn indicated") {
			t.Fatalf("recovery nudge leaked into returned messages: %#v", messages)
		}
		if assistant, ok := message.(*ai.AssistantMessage); ok && len(assistant.Content) == 1 {
			if text, ok := assistant.Content[0].(*ai.TextContent); ok && text.Text == "Let me inspect that." {
				t.Fatalf("interim assistant leaked into returned messages: %#v", messages)
			}
		}
	}
}

func TestRunLoopBoundsProviderDeclaredToolUseWithoutCalls(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.TextContent{Text: "attempt 1"}),
		loopAssistant(ai.StopReasonToolUse, &ai.TextContent{Text: "attempt 2"}),
		loopAssistant(ai.StopReasonToolUse, &ai.TextContent{Text: "attempt 3"}),
		loopAssistant(ai.StopReasonToolUse, &ai.TextContent{Text: "attempt 4"}),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "must not be consumed"}),
	}}

	messages, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{}, AgentLoopConfig{
		Model: loopModel(),
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(responses.contexts); got != 4 {
		t.Fatalf("provider calls = %d, want initial + 3 bounded retries", got)
	}
	if got := len(messages); got != 2 {
		t.Fatalf("returned messages = %d, want prompt + final bounded response", got)
	}
	assistant, ok := messages[1].(*ai.AssistantMessage)
	if !ok || len(assistant.Content) != 1 || assistant.Content[0].(*ai.TextContent).Text != "attempt 4" {
		t.Fatalf("final bounded response = %#v", messages[1])
	}
}

func TestRunLoopUniquifiesDuplicateToolCallIDs(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1|fc-1", Name: "echo", Arguments: map[string]any{"value": "first"}},
			&ai.ToolCall{ID: "call-1|fc-2", Name: "echo", Arguments: map[string]any{"value": "second"}},
		),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"}),
	}}
	var executionIDs []string
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "echo",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, callID string, _ any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			executionIDs = append(executionIDs, callID)
			return textToolResult("ok"), nil
		},
	}

	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model:         loopModel(),
		ToolExecution: ToolExecutionSequential,
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(executionIDs, ","), "call-1|fc-1,call-1_d2|fc-2"; got != want {
		t.Fatalf("execution IDs = %q, want %q", got, want)
	}
	postToolContext := responses.contexts[1].Messages
	if got := len(postToolContext); got != 4 {
		t.Fatalf("post-tool context messages = %d, want prompt + assistant + 2 results", got)
	}
	assistant, ok := postToolContext[1].(*ai.AssistantMessage)
	if !ok {
		t.Fatalf("tool assistant = %#v", postToolContext[1])
	}
	calls := assistantToolCalls(assistant)
	if len(calls) != 2 || calls[0].ID != "call-1|fc-1" || calls[1].ID != "call-1_d2|fc-2" {
		t.Fatalf("normalized calls = %#v", calls)
	}
	for index, want := range []string{"call-1|fc-1", "call-1_d2|fc-2"} {
		result, ok := postToolContext[index+2].(*ai.ToolResultMessage)
		if !ok || result.ToolCallID != want {
			t.Fatalf("result %d = %#v, want id %q", index, postToolContext[index+2], want)
		}
	}
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
			Name:                "echo",
			Parameters:          jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
			ConstrainedSampling: &ai.ConstrainedSamplingConfig{Type: ai.ConstrainedSamplingJSONSchema, Strict: ai.ConstrainedSamplingPrefer},
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
	}, responses.stream)
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
	tools := responses.contexts[0].Tools
	if tools == nil || len(*tools) != 1 || (*tools)[0].ConstrainedSampling == nil ||
		(*tools)[0].ConstrainedSampling.Strict != ai.ConstrainedSamplingPrefer {
		t.Fatalf("provider tools = %#v", tools)
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

func TestRunLoopSuppliesModelSnapshotToParallelToolExecutions(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1", Name: "inspect", Arguments: map[string]any{}},
			&ai.ToolCall{ID: "call-2", Name: "inspect", Arguments: map[string]any{}},
		),
		loopAssistant(ai.StopReasonStop),
	}}
	model := loopModel()
	model.Input = ai.InputModalities{ai.InputText}
	var seenMu sync.Mutex
	seen := make([]*ai.Model, 0, 2)
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "inspect", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(ctx context.Context, _ string, _ any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			seenMu.Lock()
			seen = append(seen, ToolExecutionModel(ctx))
			seenMu.Unlock()
			return textToolResult("done"), nil
		},
	}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: model, ToolExecution: ToolExecutionParallel,
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	seenMu.Lock()
	defer seenMu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("tool model contexts = %d, want 2", len(seen))
	}
	for index, snapshot := range seen {
		if snapshot == nil || snapshot == model || len(snapshot.Input) != 1 || snapshot.Input[0] != ai.InputText {
			t.Fatalf("tool model snapshot %d = %#v", index, snapshot)
		}
	}
	if seen[0] == seen[1] {
		t.Fatal("parallel tool calls shared a mutable model snapshot")
	}
}

type preparingLoopTool struct {
	mu       sync.Mutex
	prepared []string
}

func (*preparingLoopTool) Spec() AgentToolSpec {
	return AgentToolSpec{
		Name:       "prepare",
		Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
	}
}

func (tool *preparingLoopTool) PrepareParallelExecution(ctx context.Context, params any) (context.Context, func(), error) {
	tool.mu.Lock()
	tool.prepared = append(tool.prepared, params.(map[string]any)["value"].(string))
	tool.mu.Unlock()
	return ctx, func() {}, nil
}

func (*preparingLoopTool) Execute(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
	return textToolResult("done"), nil
}

func TestRunLoopPreparesParallelToolsInSourceOrder(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1", Name: "prepare", Arguments: map[string]any{"value": "first"}},
			&ai.ToolCall{ID: "call-2", Name: "prepare", Arguments: map[string]any{"value": "second"}},
		),
		loopAssistant(ai.StopReasonStop),
	}}
	tool := &preparingLoopTool{}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(), ToolExecution: ToolExecutionParallel,
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	tool.mu.Lock()
	defer tool.mu.Unlock()
	if got, want := strings.Join(tool.prepared, ","), "first,second"; got != want {
		t.Fatalf("preparation order = %q, want %q", got, want)
	}
}

func TestRunLoopPreparesInvocationOrderedResourcesInSourceOrder(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse,
			&ai.ToolCall{ID: "call-1", Name: "ordered", Arguments: map[string]any{"value": "first"}},
			&ai.ToolCall{ID: "call-2", Name: "ordered", Arguments: map[string]any{"value": "second"}},
		),
		loopAssistant(ai.StopReasonStop),
	}}
	tool := &parallelPreparationTestTool{}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(), ToolExecution: ToolExecutionParallel,
	}, nil, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	tool.mutex.Lock()
	defer tool.mutex.Unlock()
	if got, want := joinStrings(tool.prepared), "first,second"; got != want {
		t.Fatalf("parallel preparation order = %s, want %s", got, want)
	}
	if tool.released != 2 {
		t.Fatalf("parallel releases = %d, want 2", tool.released)
	}
}

type parallelPreparationTestTool struct {
	mutex    sync.Mutex
	prepared []string
	released int
}

func (*parallelPreparationTestTool) Spec() AgentToolSpec {
	return AgentToolSpec{
		Name:       "ordered",
		Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
	}
}

func (tool *parallelPreparationTestTool) PrepareParallelExecution(ctx context.Context, args any) (context.Context, func(), error) {
	value := args.(map[string]any)["value"].(string)
	tool.mutex.Lock()
	tool.prepared = append(tool.prepared, value)
	tool.mutex.Unlock()
	return ctx, func() {
		tool.mutex.Lock()
		tool.released++
		tool.mutex.Unlock()
	}, nil
}

func (*parallelPreparationTestTool) Execute(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
	return textToolResult("done"), nil
}

func TestRunLoopHooksMutateWithoutRevalidationAndIgnoreLateUpdates(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "mutate", Arguments: map[string]any{"value": "valid"}}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, call),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"}),
	}}
	var late AgentToolUpdateCallback
	var executed any
	toolUsage := &ai.Usage{Input: 1, Output: 2, TotalTokens: 3, Cost: ai.Cost{Total: 0.3}}
	patchedUsage := &ai.Usage{Input: 4, Output: 5, TotalTokens: 9, Cost: ai.Cost{Total: 0.9}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "mutate",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}}}`),
		},
		Run: func(_ context.Context, _ string, params any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			executed = params.(map[string]any)["value"]
			update(textToolResult("working"))
			late = update
			result := textToolResult("complete")
			result.Usage = toolUsage
			return result, nil
		},
	}
	updates := 0
	var final ToolExecutionEndEvent
	messages, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(),
		BeforeToolCall: func(_ context.Context, hook BeforeToolCallContext) (*BeforeToolCallResult, error) {
			hook.Args.(map[string]any)["value"] = 17
			return nil, nil
		},
		AfterToolCall: func(_ context.Context, hook AfterToolCallContext) (*AfterToolCallResult, error) {
			if hook.Args.(map[string]any)["value"] != 17 {
				t.Fatalf("after hook args = %#v", hook.Args)
			}
			if hook.Result.Usage == nil || hook.Result.Usage.TotalTokens != toolUsage.TotalTokens {
				t.Fatalf("hook usage = %#v", hook.Result.Usage)
			}
			isError := true
			return &AfterToolCallResult{Content: ai.ToolResultContent{}, IsError: &isError, Usage: patchedUsage}, nil
		},
	}, func(_ context.Context, event AgentEvent) error {
		switch value := event.(type) {
		case ToolExecutionUpdateEvent:
			updates++
		case ToolExecutionEndEvent:
			final = value
		}
		return nil
	}, responses.stream)
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
	if final.Result.Usage == nil || final.Result.Usage.TotalTokens != patchedUsage.TotalTokens {
		t.Fatalf("final usage = %#v", final.Result.Usage)
	}
	foundToolResult := false
	for _, message := range messages {
		if result, ok := message.(*ai.ToolResultMessage); ok && (result.Usage == nil || result.Usage.TotalTokens != patchedUsage.TotalTokens) {
			t.Fatalf("persisted tool usage = %#v", result.Usage)
		} else if ok {
			foundToolResult = true
		}
	}
	if !foundToolResult {
		t.Fatal("tool result message missing")
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
		Model: loopModel(),
	}, func(_ context.Context, event AgentEvent) error {
		if end, ok := event.(ToolExecutionEndEvent); ok {
			result, isError = end.Result, end.IsError
		}
		return nil
	}, responses.stream)
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
	stream := (&loopResponseQueue{}).stream
	config := AgentLoopConfig{Model: loopModel()}
	if _, err := RunLoopContinue(context.Background(), &AgentContext{}, config, nil, stream); err == nil || err.Error() != "Cannot continue: no messages in context" {
		t.Fatalf("empty continuation error = %v", err)
	}
	if _, err := RunLoopContinue(context.Background(), &AgentContext{Messages: AgentMessages{loopAssistant(ai.StopReasonStop)}}, config, nil, stream); err == nil || err.Error() != "Cannot continue from message role: assistant" {
		t.Fatalf("assistant continuation error = %v", err)
	}
}

func TestRunLoopContinueAppendsToCallerContextLikeUpstream(t *testing.T) {
	response := loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"})
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{response}}
	loopContext := &AgentContext{Messages: AgentMessages{loopUser("existing")}, Tools: []AgentTool{}}
	newMessages, err := RunLoopContinue(context.Background(), loopContext, AgentLoopConfig{
		Model: loopModel(),
	}, nil, responses.stream)
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
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			return &empty, nil
		},
	}, nil, stream)
	if err != nil {
		t.Fatal(err)
	}
	if received == nil || *received != fallback {
		t.Fatalf("received API key = %v", received)
	}
}

func TestRunLoopAppliesRequestAuthWithoutMutatingConfiguredOptions(t *testing.T) {
	configuredKey := "configured-key"
	configuredHeader := "configured"
	response := loopAssistant(ai.StopReasonStop)
	model := loopModel()
	baseURL := "https://vertex.example.test/v1"
	stream := func(
		_ context.Context,
		gotModel *ai.Model,
		_ ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		if gotModel.BaseURL != baseURL {
			t.Fatalf("base URL = %q, want %q", gotModel.BaseURL, baseURL)
		}
		if options.APIKey == nil || *options.APIKey != configuredKey {
			t.Fatalf("API key = %v, want configured override", options.APIKey)
		}
		if options.Env["GOOGLE_CLOUD_PROJECT"] != "configured-project" || options.Env["GOOGLE_CLOUD_LOCATION"] != "us-central1" {
			t.Fatalf("request environment = %#v", options.Env)
		}
		if options.Headers["authorization"] == nil || *options.Headers["authorization"] != configuredHeader {
			t.Fatalf("request headers = %#v", options.Headers)
		}
		if _, duplicate := options.Headers["Authorization"]; duplicate {
			t.Fatalf("case-insensitive auth override left duplicate headers: %#v", options.Headers)
		}
		if value, exists := options.Headers["x-api-key"]; !exists || value != nil {
			t.Fatalf("nullable auth header was not preserved: %#v", options.Headers)
		}
		return func(yield func(ai.AssistantMessageEvent, error) bool) {
			yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: response}, nil)
		}, nil
	}
	configuredOptions := ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey:  &configuredKey,
		Env:     ai.ProviderEnv{"GOOGLE_CLOUD_PROJECT": "configured-project"},
		Headers: ai.ProviderHeaders{"authorization": &configuredHeader},
	}}
	modelHeadersResolved := false
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{}, AgentLoopConfig{
		SimpleStreamOptions: configuredOptions,
		Model:               model,
		GetRequestAuth: func(context.Context, ai.ProviderID) (*RequestAuth, error) {
			resolvedKey := "resolved-key"
			resolvedHeader := "resolved"
			return &RequestAuth{
				APIKey: &resolvedKey,
				Env: ai.ProviderEnv{
					"GOOGLE_CLOUD_PROJECT":  "resolved-project",
					"GOOGLE_CLOUD_LOCATION": "us-central1",
				},
				Headers: ai.ProviderHeaders{"Authorization": &resolvedHeader, "x-api-key": nil},
				BaseURL: &baseURL,
			}, nil
		},
		GetModelHeaders: func(_ context.Context, _ *ai.Model, _ *string, env ai.ProviderEnv) (*map[string]string, error) {
			modelHeadersResolved = true
			if env["GOOGLE_CLOUD_PROJECT"] != "configured-project" || env["GOOGLE_CLOUD_LOCATION"] != "us-central1" {
				t.Fatalf("model-header environment = %#v", env)
			}
			return nil, nil
		},
	}, nil, stream)
	if err != nil {
		t.Fatal(err)
	}
	if configuredOptions.Env["GOOGLE_CLOUD_LOCATION"] != "" || len(configuredOptions.Env) != 1 {
		t.Fatalf("configured environment was mutated: %#v", configuredOptions.Env)
	}
	if !modelHeadersResolved {
		t.Fatal("model headers were not resolved")
	}
}

func TestRunLoopResolvesModelHeadersForEveryProviderRequest(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "noop", Arguments: map[string]any{}}),
		loopAssistant(ai.StopReasonStop),
	}}
	seen := make([]map[string]string, 0, 2)
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		copy := make(map[string]string)
		for name, value := range *model.Headers {
			copy[name] = value
		}
		seen = append(seen, copy)
		return responses.stream(ctx, model, request, options)
	}
	base := map[string]string{"X-Static": "static", "X-Same": "base"}
	model := loopModel()
	model.Headers = &base
	resolutions := 0
	tool := AgentToolFunc{AgentToolSpec: AgentToolSpec{Name: "noop", Parameters: jsonschema.Schema(`{"type":"object"}`)}, Run: func(context.Context, string, any, AgentToolUpdateCallback) (AgentToolResult, error) {
		return textToolResult("done"), nil
	}}
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: model,
		GetModelHeaders: func(_ context.Context, _ *ai.Model, apiKey *string, env ai.ProviderEnv) (*map[string]string, error) {
			if apiKey != nil {
				t.Fatalf("unexpected API key: %q", *apiKey)
			}
			if env != nil {
				t.Fatalf("unexpected provider environment: %#v", env)
			}
			resolutions++
			headers := map[string]string{"X-Dynamic": strconv.Itoa(resolutions), "x-same": "resolved"}
			return &headers, nil
		},
	}, nil, stream)
	if err != nil {
		t.Fatal(err)
	}
	if resolutions != 2 || len(seen) != 2 || seen[0]["X-Dynamic"] != "1" || seen[1]["X-Dynamic"] != "2" {
		t.Fatalf("request-time resolutions = %d, headers = %#v", resolutions, seen)
	}
	for _, headers := range seen {
		if headers["X-Static"] != "static" || headers["x-same"] != "resolved" {
			t.Fatalf("headers were not merged case-insensitively: %#v", headers)
		}
		if _, duplicate := headers["X-Same"]; duplicate {
			t.Fatalf("case-insensitive override left duplicate header: %#v", headers)
		}
	}
}

// Upstream's update callback hands the event to an event-loop-driven
// subscriber (agent-loop.ts:679-692): the tool never waits for it. A sink that
// only unblocks once the tool has returned must not deadlock, and its update
// must still be delivered before the run completes.
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
	// Written by the drain goroutine, read after RunLoop returns: close() joins
	// that goroutine, so -race also proves the teardown happens-before edge.
	delivered := 0
	done := make(chan error, 1)
	go func() {
		_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
			Model: loopModel(),
		}, func(_ context.Context, event AgentEvent) error {
			if _, ok := event.(ToolExecutionUpdateEvent); ok {
				<-toolReturned
				delivered++
			}
			return nil
		}, responses.stream)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tool update blocked tool execution")
	}
	if delivered != 1 {
		t.Fatalf("delivered %d updates, want 1", delivered)
	}
}

// Updates are handed to one drain goroutine, so a sink observes them one at a
// time, in the order the tool produced them, and always before the call's
// tool_execution_end.
func TestRunLoopToolUpdatesEmitSequentiallyInOrder(t *testing.T) {
	const updateCount = 200
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "update", Arguments: map[string]any{}}),
		loopAssistant(ai.StopReasonStop),
	}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "update", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			for index := 0; index < updateCount; index++ {
				update(textToolResult(strconv.Itoa(index)))
			}
			return textToolResult("done"), nil
		},
	}
	var mu sync.Mutex
	var inFlight, overlaps int
	var seen []string
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(),
	}, func(_ context.Context, event AgentEvent) error {
		if _, ok := event.(ToolExecutionEndEvent); ok {
			mu.Lock()
			seen = append(seen, "end")
			mu.Unlock()
			return nil
		}
		update, ok := event.(ToolExecutionUpdateEvent)
		if !ok {
			return nil
		}
		mu.Lock()
		inFlight++
		if inFlight > 1 {
			overlaps++
		}
		mu.Unlock()
		time.Sleep(50 * time.Microsecond)
		mu.Lock()
		seen = append(seen, update.PartialResult.Content[0].(*ai.TextContent).Text)
		inFlight--
		mu.Unlock()
		return nil
	}, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	if overlaps != 0 {
		t.Fatalf("sink invocations overlapped %d times", overlaps)
	}
	if len(seen) != updateCount+1 {
		t.Fatalf("saw %d events, want %d updates plus one end", len(seen), updateCount)
	}
	for index, text := range seen[:updateCount] {
		if text != strconv.Itoa(index) {
			t.Fatalf("update %d delivered out of order: %q", index, text)
		}
	}
	if seen[updateCount] != "end" {
		t.Fatalf("tool_execution_end did not follow every update: %q", seen[updateCount])
	}
}

// The update drain goroutine is owned by the run: every queued update must be
// delivered and the goroutine must exit before RunLoop returns.
func TestRunLoopDrainsUpdatesAndStopsDrainGoroutine(t *testing.T) {
	const (
		runs    = 100
		updates = 50
	)
	run := func() int {
		responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
			loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "update", Arguments: map[string]any{}}),
			loopAssistant(ai.StopReasonStop),
		}}
		tool := AgentToolFunc{
			AgentToolSpec: AgentToolSpec{Name: "update", Parameters: jsonschema.Schema(`{"type":"object"}`)},
			Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
				for index := 0; index < updates; index++ {
					update(textToolResult("x"))
				}
				return textToolResult("done"), nil
			},
		}
		delivered := 0
		if _, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
			Model: loopModel(),
		}, func(_ context.Context, event AgentEvent) error {
			if _, ok := event.(ToolExecutionUpdateEvent); ok {
				delivered++
			}
			return nil
		}, responses.stream); err != nil {
			t.Fatal(err)
		}
		return delivered
	}
	baseline := runtime.NumGoroutine()
	for index := 0; index < runs; index++ {
		if got := run(); got != updates {
			t.Fatalf("run %d delivered %d updates, want %d", index, got, updates)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > baseline+2 {
		t.Fatalf("goroutines after %d runs = %d, baseline %d", runs, after, baseline)
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
			Model: loopModel(),
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
		}, responses.stream)
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
		Model: loopModel(),
	}, func(_ context.Context, event AgentEvent) error {
		if end, ok := event.(ToolExecutionEndEvent); ok {
			errorText = end.Result.Content[0].(*ai.TextContent).Text
		}
		return nil
	}, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	zIndex := strings.Index(errorText, `"z": 1`)
	aIndex := strings.Index(errorText, `"a": 2`)
	if zIndex < 0 || aIndex < 0 || zIndex >= aIndex {
		t.Fatalf("validation diagnostic lost raw order:\n%s", errorText)
	}
}

func TestRunLoopIdentityPrepareValidatesMutatedArguments(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "prepared", Arguments: map[string]any{"value": "before"}}
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, call),
		loopAssistant(ai.StopReasonStop),
	}}
	var executed any
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:       "prepared",
			Parameters: jsonschema.Schema(`{"type":"object","required":["value"],"properties":{"value":{"type":"number"}}}`),
			PrepareArguments: func(args any) (any, error) {
				args.(map[string]any)["value"] = 42
				return args, nil
			},
		},
		Run: func(_ context.Context, _ string, args any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			executed = args.(map[string]any)["value"]
			return textToolResult("done"), nil
		},
	}
	if _, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(),
	}, nil, responses.stream); err != nil {
		t.Fatal(err)
	}
	if executed != float64(42) {
		t.Fatalf("executed prepared value = %#v", executed)
	}
}

func TestRunLoopToolUpdatesOwnTheirPayloads(t *testing.T) {
	type updateLabels []string
	type updateDetails map[string]updateLabels
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonToolUse, &ai.ToolCall{ID: "call-1", Name: "update", Arguments: map[string]any{}}),
		loopAssistant(ai.StopReasonStop),
	}}
	partial := textToolResult("first")
	partial.Details = updateDetails{"phase": updateLabels{"first"}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{Name: "update", Parameters: jsonschema.Schema(`{"type":"object"}`)},
		Run: func(_ context.Context, _ string, _ any, update AgentToolUpdateCallback) (AgentToolResult, error) {
			update(partial)
			update(textToolResult("second"))
			partial.Content[0].(*ai.TextContent).Text = "mutated"
			partial.Details.(updateDetails)["phase"][0] = "mutated"
			return textToolResult("done"), nil
		},
	}
	var seen []ToolExecutionUpdateEvent
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("go")}, AgentContext{Tools: []AgentTool{tool}}, AgentLoopConfig{
		Model: loopModel(),
	}, func(_ context.Context, event AgentEvent) error {
		if update, ok := event.(ToolExecutionUpdateEvent); ok {
			seen = append(seen, update)
		}
		return nil
	}, responses.stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("saw %d updates, want 2", len(seen))
	}
	if text := seen[0].PartialResult.Content[0].(*ai.TextContent).Text; text != "first" {
		t.Fatalf("first update was mutated after callback return: %q", text)
	}
	if label := seen[0].PartialResult.Details.(updateDetails)["phase"][0]; label != "first" {
		t.Fatalf("first update details were mutated after callback return: %q", label)
	}
	if text := seen[1].PartialResult.Content[0].(*ai.TextContent).Text; text != "second" {
		t.Fatalf("second update = %q", text)
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
			Model: loopModel(),
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
		}, responses.stream)
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
