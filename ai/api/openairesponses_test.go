package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

func responsesTestModel() *ai.Model {
	return &ai.Model{
		ID:        "gpt-test",
		Name:      "GPT Test",
		API:       ai.APIOpenAIResponses,
		Provider:  "openai",
		BaseURL:   "https://api.openai.com/v1",
		Reasoning: true,
		Input:     ai.InputModalities{ai.InputText, ai.InputImage},
		Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{
			Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 1.5,
		}},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}
}

func TestOpenAIResponsesCustomToolCallStreamingRoundTrip(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	var deltas strings.Builder
	processor := newOpenAIResponsesProcessor(model, output, nil, func(event ai.AssistantMessageEvent) bool {
		if delta, ok := event.(ai.ToolCallDeltaEvent); ok {
			deltas.WriteString(delta.Delta)
		}
		return true
	})
	processor.grammarToolInputProperties = map[string]string{"emit": "payload"}
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","call_id":"call_1","name":"emit","input":""}}`,
		`{"type":"response.custom_tool_call_input.delta","output_index":0,"delta":"a\""}`,
		`{"type":"response.custom_tool_call_input.done","output_index":0,"input":"a\"b"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"custom_tool_call","id":"ct_1","call_id":"call_1","name":"emit","input":"a\"b"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`,
	} {
		if err := processor.handle(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if deltas.String() != `{"payload":"a\"b"}` {
		t.Fatalf("streamed JSON deltas = %q", deltas.String())
	}
	call, ok := output.Content[0].(*ai.ToolCall)
	if !ok || call.ID != "call_1|ct_1" || call.Arguments["payload"] != `a"b` ||
		output.StopReason != ai.StopReasonToolUse {
		t.Fatalf("custom tool call = %#v", output.Content)
	}

	messages, err := convertResponsesMessagesWithOptions(model, ai.Context{Messages: ai.MessageList{
		output,
		&ai.ToolResultMessage{
			ToolCallID: call.ID, ToolName: call.Name,
			Content: ai.ToolResultContent{&ai.TextContent{Text: "done"}},
		},
	}}, map[string]ai.Tool{}, responsesMessageOptions{
		supportsDeveloperRole:      true,
		grammarToolInputProperties: map[string]string{"emit": "payload"},
	})
	if err != nil {
		t.Fatal(err)
	}
	replayedCall, ok := messages[0].(responsesCustomToolCall)
	if !ok || replayedCall.Type != "custom_tool_call" || replayedCall.Input != `a"b` {
		t.Fatalf("custom call replay = %#v", messages[0])
	}
	replayedOutput, ok := messages[1].(responsesFunctionCallOutput)
	if !ok || replayedOutput.Type != "custom_tool_call_output" {
		t.Fatalf("custom output replay = %#v", messages[1])
	}
}

func TestOpenAIResponsesProcessorTextToolUsageAndScratchCleanup(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	var events []ai.AssistantMessageEvent
	processor := newOpenAIResponsesProcessor(model, output, nil, func(event ai.AssistantMessageEvent) bool {
		events = append(events, event)
		return true
	})
	rawEvents := []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[],"status":"in_progress"}}`,
		`{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}],"status":"completed","phase":"final_answer"}}`,
		`{"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":1,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{\"path\":\"README.md\"}"}`,
		`{"type":"response.output_item.done","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"README.md\"}"}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","usage":{"input_tokens":20,"output_tokens":7,"total_tokens":27,"input_tokens_details":{"cached_tokens":2,"cache_write_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}}}`,
	}
	for _, raw := range rawEvents {
		if err := processor.handle(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if !processor.sawTerminalResponseEvent || output.StopReason != ai.StopReasonToolUse {
		t.Fatalf("terminal=%v stop=%q", processor.sawTerminalResponseEvent, output.StopReason)
	}
	if output.Usage.Input != 15 || output.Usage.Output != 7 || output.Usage.CacheRead != 2 || output.Usage.CacheWrite != 3 || output.Usage.Reasoning == nil || *output.Usage.Reasoning != 1 {
		t.Fatalf("usage = %#v", output.Usage)
	}
	if len(output.Content) != 2 {
		t.Fatalf("content count = %d", len(output.Content))
	}
	text := output.Content[0].(*ai.TextContent)
	if text.Text != "hello" || text.TextSignature == nil || *text.TextSignature != `{"v":1,"id":"msg_1","phase":"final_answer"}` {
		t.Fatalf("text = %#v", text)
	}
	call := output.Content[1].(*ai.ToolCall)
	if call.PartialJSON != nil || call.Arguments["path"] != "README.md" {
		t.Fatalf("tool call = %#v", call)
	}
	if len(events) != 7 {
		t.Fatalf("event count = %d, want 7", len(events))
	}
}

func TestOpenAIResponsesProcessorRejectsFailedAndUnknownTerminal(t *testing.T) {
	output := newAssistantMessage(responsesTestModel())
	processor := newOpenAIResponsesProcessor(responsesTestModel(), output, nil, func(ai.AssistantMessageEvent) bool { return true })
	err := processor.handle(json.RawMessage(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`))
	if err == nil || err.Error() != "server_error: boom" {
		t.Fatalf("failed error = %v", err)
	}
	if !processor.sawTerminalResponseEvent {
		t.Fatal("failed response did not count as terminal")
	}
	if output.RawStopReason == nil || *output.RawStopReason != "failed" {
		t.Fatalf("raw stop reason = %v", output.RawStopReason)
	}
	if _, _, err := mapResponsesStopReason("future", ""); err == nil {
		t.Fatal("unknown status was accepted")
	}
}

func TestOpenAIResponsesTracksMessagePhaseAndTerminalStatus(t *testing.T) {
	for _, test := range []struct {
		name       string
		addedPhase string
		donePhase  string
		status     string
		wantBefore ai.StopReason
		wantAfter  ai.StopReason
	}{
		{name: "commentary stays pending", addedPhase: "commentary", donePhase: "commentary", status: "completed", wantBefore: ai.StopReasonPending, wantAfter: ai.StopReasonStop},
		{name: "final answer is provisional stop", addedPhase: "final_answer", donePhase: "final_answer", status: "completed", wantBefore: ai.StopReasonStop, wantAfter: ai.StopReasonStop},
		{name: "done phase can switch to final answer", addedPhase: "commentary", donePhase: "final_answer", status: "completed", wantBefore: ai.StopReasonPending, wantAfter: ai.StopReasonStop},
		{name: "incomplete overrides provisional stop", addedPhase: "final_answer", donePhase: "final_answer", status: "incomplete", wantBefore: ai.StopReasonStop, wantAfter: ai.StopReasonError},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := responsesTestModel()
			output := newAssistantMessage(model)
			processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
			added := fmt.Sprintf(`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_phase","phase":%q,"content":[]}}`, test.addedPhase)
			if err := processor.handle(json.RawMessage(added)); err != nil {
				t.Fatal(err)
			}
			if output.StopReason != test.wantBefore {
				t.Fatalf("after added stop = %q, want %q", output.StopReason, test.wantBefore)
			}
			done := fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_phase","phase":%q,"content":[]}}`, test.donePhase)
			if err := processor.handle(json.RawMessage(done)); err != nil {
				t.Fatal(err)
			}
			terminal := fmt.Sprintf(`{"type":"response.%s","response":{"id":"resp_phase","status":%q}}`, test.status, test.status)
			if err := processor.handle(json.RawMessage(terminal)); err != nil {
				t.Fatal(err)
			}
			if output.StopReason != test.wantAfter || output.RawStopReason == nil || *output.RawStopReason != test.status {
				t.Fatalf("terminal output = %#v", output)
			}
		})
	}
}

func TestStreamOpenAIResponsesErrorsOnEarlyEOF(t *testing.T) {
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_early\"}}\n\ndata: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()

	key := "fixture-key"
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}, &OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &key}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != ai.StopReasonError || result.ErrorMessage == nil || *result.ErrorMessage != "OpenAI Responses stream ended before a terminal response event" {
		t.Fatalf("result = %#v", result)
	}
}

func TestOpenAIResponsesBackfillsMissingEncryptedReasoning(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","summary":[]}}`,
		`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"cipher"}]}}`,
	} {
		if err := processor.handle(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	thinking := output.Content[0].(*ai.ThinkingContent)
	if thinking.ThinkingSignature == nil || !strings.Contains(*thinking.ThinkingSignature, `"encrypted_content":"cipher"`) {
		t.Fatalf("signature = %v", thinking.ThinkingSignature)
	}
}

func TestOpenAIResponsesStreamedToolArgumentsReplayPreservesOrder(t *testing.T) {
	const arguments = `{"text":"hello","mode":"plain","metadata":{"count":2}}`
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_replay"}}`,
			`data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_replay","call_id":"call_replay","name":"echo","arguments":""}}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"text\":\"hello\",\"mode\":\"plain\","}`,
			`data: {"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"metadata\":{\"count\":2}}"}`,
			`data: {"type":"response.function_call_arguments.done","output_index":0,"arguments":"{\"text\":\"hello\",\"mode\":\"plain\",\"metadata\":{\"count\":2}}"}`,
			`data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_replay","call_id":"call_replay","name":"echo","arguments":"{\"text\":\"hello\",\"mode\":\"plain\",\"metadata\":{\"count\":2}}"}}`,
			`data: {"type":"response.completed","response":{"id":"resp_replay","status":"completed","output":[]}}`,
			`data: [DONE]`,
			``,
		}, "\n\n")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	key := "fixture-key"
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("call echo"), Timestamp: 1},
	}}, &OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &key}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q, want toolUse", message.StopReason)
	}

	payload, _, err := buildOpenAIResponsesPayload(model, ai.Context{Messages: ai.MessageList{message}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var replayed *responsesFunctionCall
	for _, item := range payload.Input {
		if call, ok := item.(responsesFunctionCall); ok {
			replayed = &call
			break
		}
	}
	if replayed == nil {
		t.Fatalf("replay payload has no function call: %#v", payload.Input)
	}
	if replayed.Arguments != arguments {
		t.Fatalf("replayed arguments = %s, want %s", replayed.Arguments, arguments)
	}
}

// Gap OA-M1: upstream openai-node applies timeoutMs to time-to-headers only
// (openai-responses.ts:134-138), so a long generation must keep streaming after
// the headers arrive even when the body outlives the timeout.
func TestOpenAIResponsesTimeoutDisarmsAfterHeadersOAM1(t *testing.T) {
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: &contextGatedBody{
				ctx:   request.Context(),
				delay: 200 * time.Millisecond,
				reader: strings.NewReader(
					"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_slow\",\"status\":\"completed\",\"output\":[]}}\n\n",
				),
			},
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	timeout := int64(50)
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}, &OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop || message.ErrorMessage != nil {
		t.Fatalf("slow body after fast headers = %#v", message)
	}
}

// Gap OA-M1: the timeout must still bound time-to-headers.
func TestOpenAIResponsesTimeoutStillBoundsHeadersOAM1(t *testing.T) {
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(5 * time.Second):
			return nil, errors.New("headers were not bounded by the timeout")
		}
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	timeout := int64(50)
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}, &OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	errorMessage := "<nil>"
	if message.ErrorMessage != nil {
		errorMessage = *message.ErrorMessage
	}
	if message.StopReason != ai.StopReasonError || errorMessage != "Request timed out." {
		t.Fatalf("blocked headers did not time out: reason=%q error=%q", message.StopReason, errorMessage)
	}
}

func TestOpenAIResponsesHeaderTimeoutResetsForEachRetryOAM1(t *testing.T) {
	previousClient := openAIHTTPClient
	attempts := 0
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		select {
		case <-request.Context().Done():
			return nil, request.Context().Err()
		case <-time.After(45 * time.Millisecond):
		}
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Header:     http.Header{"Retry-After-Ms": []string{"0"}},
				Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"retry"}}`)),
				Request:    request,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_retry\",\"status\":\"completed\",\"output\":[]}}\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	timeout := int64(70)
	maxRetries := 1
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}, &OpenAIResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout, MaxRetries: &maxRetries}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || message.StopReason != ai.StopReasonStop || message.ErrorMessage != nil {
		t.Fatalf("retry attempts=%d message=%#v", attempts, message)
	}
}

// Namespaces round-trip only where the target can also replay the item that
// loaded the namespaced tool (upstream openai-responses-namespace.test.ts).
func TestResponsesToolCallNamespaceRoundTrip(t *testing.T) {
	model := responsesTestModel()
	model.ID = "gpt-5.4"
	output := newAssistantMessage(model)
	processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_test","call_id":"call_test","name":"lookup","arguments":""}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_test","call_id":"call_test","name":"lookup","arguments":"{\"value\":\"hello\"}","namespace":"dynamic_tools"}}`,
	} {
		if err := processor.handle(json.RawMessage(event)); err != nil {
			t.Fatal(err)
		}
	}
	call, ok := output.Content[0].(*ai.ToolCall)
	if !ok || call.Namespace == nil || *call.Namespace != "dynamic_tools" {
		t.Fatalf("tool call = %#v", output.Content[0])
	}

	replay := func(target *ai.Model, deferred map[string]ai.Tool) responsesFunctionCall {
		t.Helper()
		messages, err := convertResponsesMessagesWithOptions(
			target, ai.Context{Messages: ai.MessageList{output}}, deferred, responsesMessageOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := messages[0].(responsesFunctionCall)
		if !ok {
			t.Fatalf("replayed item = %#v", messages[0])
		}
		return call
	}
	if got := replay(model, nil); got.Namespace == nil || *got.Namespace != "dynamic_tools" {
		t.Fatalf("same-model replay dropped the namespace: %#v", got)
	}
	other := responsesTestModel()
	other.ID = "gpt-5.2"
	if got := replay(other, nil); got.Namespace != nil {
		t.Fatalf("other-model replay kept the namespace: %#v", got)
	}
	if got := replay(other, map[string]ai.Tool{"lookup": {Name: "lookup"}}); got.Namespace == nil {
		t.Fatalf("deferred-tool replay dropped the namespace: %#v", got)
	}
}

func TestOpenAIResponsesRejectsUnfinishedToolCalls(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
	for _, event := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"bash","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"command\":\"ls"}`,
	} {
		if err := processor.handle(json.RawMessage(event)); err != nil {
			t.Fatal(err)
		}
	}
	err := processor.handle(json.RawMessage(`{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[]}}`))
	if err == nil || err.Error() != "OpenAI Responses stream completed with an unfinished tool call: bash (call_1|fc_1)" {
		t.Fatalf("err = %v", err)
	}
}
