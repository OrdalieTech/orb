package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
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

func TestBeforeProviderHeadersPrecedesSessionAffinity(t *testing.T) {
	var sent http.Header
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		sent = request.Header.Clone()
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\ndata: [DONE]\n\n",
		)), Request: request}, nil
	})}
	defer func() { openAIHTTPClient = previousClient }()
	model, key, sessionID := responsesTestModel(), "key", "session"
	seenAffinity := false
	stream, err := StreamSimple(context.Background(), model, ai.Context{}, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{
		APIKey: &key, SessionID: &sessionID, TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
			for name := range headers {
				seenAffinity = seenAffinity || strings.EqualFold(name, "session_id") || strings.EqualFold(name, "x-client-request-id")
			}
			return headers, nil
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	if seenAffinity || sent.Get("session_id") != sessionID || sent.Get("x-client-request-id") != sessionID {
		t.Fatalf("hook saw affinity=%t, sent=%v", seenAffinity, sent)
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
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout},
	})
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

func TestOpenAIResponsesTimeoutStartsAfterRequestHooksOAM1(t *testing.T) {
	previousClient := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.Context().Err(); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_hook\",\"status\":\"completed\",\"output\":[]}}\n\n")),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	timeout := int64(20)
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{
			APIKey: &key, TimeoutMS: &timeout,
			OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
				time.Sleep(35 * time.Millisecond)
				return payload, false, nil
			},
			TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
				time.Sleep(35 * time.Millisecond)
				return headers, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop || message.ErrorMessage != nil {
		t.Fatalf("slow request hooks consumed header timeout: %#v", message)
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
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout},
	})
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

func TestOpenAIResponsesRejectsNegativeHeaderTimeoutOAM1(t *testing.T) {
	previousClient := openAIHTTPClient
	requests := 0
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("unexpected request")
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	timeout := int64(-1)
	hookCalled := false
	model := responsesTestModel()
	model.BaseURL = "https://fixture.invalid/v1"
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{
			APIKey: &key, TimeoutMS: &timeout,
			OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
				hookCalled = true
				return payload, false, nil
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if !hookCalled || requests != 0 || message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "timeout must be a positive integer" {
		t.Fatalf("hook=%v requests=%d message=%#v", hookCalled, requests, message)
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
	stream, err := StreamOpenAIResponses(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
		}},
		Options: &ai.StreamOptions{APIKey: &key, TimeoutMS: &timeout, MaxRetries: &maxRetries},
	})
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

// Gap OA-M2: the plain OpenAI stream opts into service-tier pricing
// (openai-responses.ts:143-146, openai-responses-shared.ts:436-441); Azure has
// the mirror-image test asserting the multiplier never applies.
func TestOpenAIResponsesAppliesServiceTierPricingOAM2(t *testing.T) {
	model := responsesTestModel()
	output := newAssistantMessage(model)
	processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
	err := processor.handle(json.RawMessage(
		`{"type":"response.completed","response":{"id":"resp_tier","status":"completed","service_tier":"flex","output":[],` +
			`"usage":{"input_tokens":1000000,"output_tokens":1000000,"total_tokens":2000000}}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	cost := output.Usage.Cost
	if cost.Input != 0.5 || cost.Output != 1 || cost.Total != 1.5 {
		t.Fatalf("flex cost = %#v, want halved rates", cost)
	}
}

// Gap OA-m7: degenerate Responses edges must match upstream bug-for-bug.
func TestOpenAIResponsesDegenerateEdgesOAm7(t *testing.T) {
	t.Run("error event renders missing members like a template literal", func(t *testing.T) {
		processor := newOpenAIResponsesProcessor(responsesTestModel(), newAssistantMessage(responsesTestModel()), nil, func(ai.AssistantMessageEvent) bool { return true })
		err := processor.handle(json.RawMessage(`{"type":"error"}`))
		if err == nil || err.Error() != "Error Code undefined: undefined" {
			t.Fatalf("missing members = %v, want Error Code undefined: undefined", err)
		}
		err = processor.handle(json.RawMessage(`{"type":"error","code":429,"message":null}`))
		if err == nil || err.Error() != "Error Code 429: null" {
			t.Fatalf("scalar members = %v, want Error Code 429: null", err)
		}
		err = processor.handle(json.RawMessage(`{"type":"error","code":1e2,"message":["bad",null,{"detail":true}]}`))
		if err == nil || err.Error() != "Error Code 100: bad,,[object Object]" {
			t.Fatalf("coerced members = %v, want JavaScript template coercion", err)
		}
	})
	t.Run("empty signature id falls back to msg_pi_N", func(t *testing.T) {
		model := responsesTestModel()
		signature := `{"v":1,"id":""}`
		input, err := convertResponsesMessages(model, ai.Context{Messages: ai.MessageList{
			&ai.AssistantMessage{
				Content:    ai.AssistantContent{&ai.TextContent{Text: "answer", TextSignature: &signature}},
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				Usage:      zeroUsage(),
				StopReason: ai.StopReasonStop,
			},
		}}, map[string]ai.Tool{}, true)
		if err != nil {
			t.Fatal(err)
		}
		message, ok := input[0].(responsesOutputMessage)
		if !ok || message.ID != "msg_pi_0" {
			t.Fatalf("replayed message = %#v, want id msg_pi_0", input[0])
		}
	})
	t.Run("empty phase is omitted from the text signature", func(t *testing.T) {
		model := responsesTestModel()
		output := newAssistantMessage(model)
		processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
		for _, raw := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[],"status":"in_progress"}}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"hi","annotations":[]}],"status":"completed","phase":""}}`,
		} {
			if err := processor.handle(json.RawMessage(raw)); err != nil {
				t.Fatal(err)
			}
		}
		text := output.Content[0].(*ai.TextContent)
		if text.TextSignature == nil || *text.TextSignature != `{"v":1,"id":"msg_1"}` {
			t.Fatalf("text signature = %v, want empty phase omitted", text.TextSignature)
		}
	})
	t.Run("done event for an unhandled item type keeps the slot", func(t *testing.T) {
		model := responsesTestModel()
		output := newAssistantMessage(model)
		processor := newOpenAIResponsesProcessor(model, output, nil, func(ai.AssistantMessageEvent) bool { return true })
		for _, raw := range []string{
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[],"status":"in_progress"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"before"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"web_search_call","id":"ws_1","status":"completed"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":" after"}`,
		} {
			if err := processor.handle(json.RawMessage(raw)); err != nil {
				t.Fatal(err)
			}
		}
		text := output.Content[0].(*ai.TextContent)
		if text.Text != "before after" {
			t.Fatalf("text = %q, want the slot to survive the unhandled done event", text.Text)
		}
	})
	t.Run("replayed TS-era reasoning signature is re-normalized", func(t *testing.T) {
		model := responsesTestModel()
		signature := `{"type":"reasoning","id":"rs_1","count":1e2,"lt":"<"}`
		input, err := convertResponsesMessages(model, ai.Context{Messages: ai.MessageList{
			&ai.AssistantMessage{
				Content:    ai.AssistantContent{&ai.ThinkingContent{Thinking: "", ThinkingSignature: &signature}},
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				Usage:      zeroUsage(),
				StopReason: ai.StopReasonStop,
			},
		}}, map[string]ai.Tool{}, true)
		if err != nil {
			t.Fatal(err)
		}
		raw, ok := input[0].(json.RawMessage)
		want := `{"type":"reasoning","id":"rs_1","count":100,"lt":"<"}`
		if !ok || string(raw) != want {
			t.Fatalf("replayed reasoning = %#v, want %s", input[0], want)
		}
	})
}
