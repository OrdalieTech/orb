package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
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

func TestBuildOpenAIResponsesPayloadHonorsCompatAndReasoning(t *testing.T) {
	model := responsesTestModel()
	model.Compat = json.RawMessage(`{
		"supportsDeveloperRole":false,
		"sessionAffinityFormat":"openrouter",
		"supportsLongCacheRetention":false
	}`)
	mapping := map[ai.ModelThinkingLevel]*string{}
	mapped := "high"
	mapping[ai.ModelThinkingMedium] = &mapped
	model.ThinkingLevelMap = &mapping
	system := "system"
	sessionID := strings.Repeat("🙂", 65)
	maxTokens := float64(1)
	cache := ai.CacheRetentionLong
	effort := "medium"
	options := &OpenAIResponsesOptions{
		StreamOptions:   ai.StreamOptions{SessionID: &sessionID, MaxTokens: &maxTokens, CacheRetention: &cache},
		ReasoningEffort: &effort,
	}
	payload, compat, err := buildOpenAIResponsesPayload(model, ai.Context{
		SystemPrompt: &system,
		Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		},
	}, options)
	if err != nil {
		t.Fatal(err)
	}
	if compat.supportsDeveloperRole || compat.sessionAffinityFormat != ai.SessionAffinityOpenRouter {
		t.Fatalf("compat = %#v", compat)
	}
	if payload.MaxOutputTokens == nil || *payload.MaxOutputTokens != openAIResponsesMinOutputTokens {
		t.Fatalf("max output tokens = %v", payload.MaxOutputTokens)
	}
	if payload.PromptCacheKey == nil || len([]rune(*payload.PromptCacheKey)) != 64 {
		t.Fatalf("prompt cache key = %v", payload.PromptCacheKey)
	}
	if payload.PromptCacheRetention != nil {
		t.Fatalf("long retention was sent despite compat=false: %q", *payload.PromptCacheRetention)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "high" || payload.Reasoning.Summary == nil || *payload.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %#v", payload.Reasoning)
	}
	message, ok := payload.Input[0].(responsesInputMessage)
	if !ok || message.Role != "system" {
		t.Fatalf("system message = %#v", payload.Input[0])
	}
	headers := buildOpenAIResponsesHeaders(model, ai.Context{}, &options.StreamOptions, compat)
	if headers.Get("x-session-id") != sessionID || headers.Get("session_id") != "" {
		t.Fatalf("affinity headers = %#v", headers)
	}
}

func TestBuildOpenAIResponsesPayloadDefaultOffAndZeroMaxTokens(t *testing.T) {
	model := responsesTestModel()
	zero := float64(0)
	payload, _, err := buildOpenAIResponsesPayload(model, ai.Context{Messages: ai.MessageList{}}, &OpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{MaxTokens: &zero},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.MaxOutputTokens != nil {
		t.Fatalf("zero max tokens was not omitted: %v", payload.MaxOutputTokens)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "none" || payload.Reasoning.Summary != nil {
		t.Fatalf("default-off reasoning = %#v", payload.Reasoning)
	}
	off := map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: nil}
	model.ThinkingLevelMap = &off
	payload, _, err = buildOpenAIResponsesPayload(model, ai.Context{Messages: ai.MessageList{}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning != nil {
		t.Fatalf("unsupported off reasoning was sent: %#v", payload.Reasoning)
	}
}

func TestStreamSimpleOpenAIResponsesClampsContextAndReasoning(t *testing.T) {
	var requestBody []byte
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_simple\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()

	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	model.ContextWindow = 5_000
	model.MaxTokens = 1_000
	key := "fixture-key"
	temperature := 0.25
	hookedTemperature := 0.5
	reasoning := ai.ThinkingHigh
	hookCalled := false
	stream, err := StreamSimpleOpenAIResponses(context.Background(), model, ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText(strings.Repeat("x", 400)), Timestamp: 1},
	}}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey:      &key,
			Temperature: &temperature,
			OnPayload: func(_ context.Context, value any, _ *ai.Model) (any, bool, error) {
				payload, ok := value.(*OpenAIResponsesPayload)
				if !ok {
					t.Fatalf("payload hook value = %T", value)
				}
				payload.Temperature = &hookedTemperature
				hookCalled = true
				return nil, false, nil
			},
		},
		Reasoning: &reasoning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := ai.Collect(stream); err != nil || result.StopReason != ai.StopReasonStop {
		t.Fatalf("result = %#v, err = %v", result, err)
	}
	var payload struct {
		MaxOutputTokens int64   `json:"max_output_tokens"`
		Temperature     float64 `json:"temperature"`
		Reasoning       struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
	}
	if err := json.Unmarshal(requestBody, &payload); err != nil {
		t.Fatal(err)
	}
	if !hookCalled || payload.MaxOutputTokens != 804 || payload.Temperature != hookedTemperature || payload.Reasoning.Effort != "high" {
		t.Fatalf("simple payload = %#v", payload)
	}
}

func TestOpenAIResponsesValidatesAuthBeforePayloadHook(t *testing.T) {
	model := responsesTestModel()
	model.Provider = "fixture-provider"
	hookCalled := false
	stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, ai.Context{}, &OpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{OnPayload: func(context.Context, any, *ai.Model) (any, bool, error) {
			hookCalled = true
			return nil, false, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if hookCalled {
		t.Fatal("payload hook ran without valid provider authentication")
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "No API key for provider: fixture-provider") {
		t.Fatalf("message = %#v", message)
	}
}

func TestConvertResponsesMessagesHashesIDsOver64UTF16Units(t *testing.T) {
	model := responsesTestModel()
	id := strings.Repeat("🙂", 33)
	input, err := convertResponsesMessages(model, ai.Context{Messages: ai.MessageList{
		&ai.AssistantMessage{
			Content:    ai.AssistantContent{&ai.TextContent{Text: "hello", TextSignature: &id}},
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      zeroUsage(),
			StopReason: ai.StopReasonStop,
		},
	}}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := input[0].(responsesOutputMessage)
	if !ok {
		t.Fatalf("message = %T", input[0])
	}
	if !strings.HasPrefix(message.ID, "msg_") || message.ID == id {
		t.Fatalf("long UTF-16 id was not hashed: %q", message.ID)
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
	processor := newOpenAIResponsesProcessor(responsesTestModel(), newAssistantMessage(responsesTestModel()), nil, func(ai.AssistantMessageEvent) bool { return true })
	err := processor.handle(json.RawMessage(`{"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}`))
	if err == nil || err.Error() != "server_error: boom" {
		t.Fatalf("failed error = %v", err)
	}
	if !processor.sawTerminalResponseEvent {
		t.Fatal("failed response did not count as terminal")
	}
	if _, err := mapResponsesStopReason("future"); err == nil {
		t.Fatal("unknown status was accepted")
	}
}

func TestOpenAIResponsesNormalizesReasoningSignatureLikeJSONStringify(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
	for _, raw := range []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_numbers","summary":[]}}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_numbers","summary":[],"integerExponent":1e2,"escaped":"\u003c","negativeZero":-0,"overflow":1e400}}`,
	} {
		if err := processor.handle(json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
	}
	thinking := output.Content[0].(*ai.ThinkingContent)
	want := `{"type":"reasoning","id":"rs_numbers","summary":[],"integerExponent":100,"escaped":"<","negativeZero":0,"overflow":null}`
	if thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != want {
		t.Fatalf("thinking signature = %v, want %s", thinking.ThinkingSignature, want)
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
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{APIKey: &key},
	})
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
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("call echo"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{APIKey: &key},
	})
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

func TestSetResponsesObjectFieldMatchesObjectSpread(t *testing.T) {
	value := []byte(`"cipher"`)
	for _, test := range []struct {
		name string
		in   string
		want string
	}{
		{name: "missing", in: `{"type":"reasoning","count":1e2}`, want: `{"type":"reasoning","count":100,"encrypted_content":"cipher"}`},
		{name: "null retained position", in: `{"type":"reasoning","encrypted_content":null,"tail":"\u003c"}`, want: `{"type":"reasoning","encrypted_content":"cipher","tail":"<"}`},
		{name: "duplicate collapses at first position", in: `{"encrypted_content":null,"tail":1,"encrypted_content":"old"}`, want: `{"encrypted_content":"cipher","tail":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := setResponsesObjectField(test.in, "encrypted_content", value)
			if !ok || got != test.want {
				t.Fatalf("updated = %q, %v; want %q", got, ok, test.want)
			}
		})
	}
}
