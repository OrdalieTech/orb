package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestOpenAICompletionsRejectsStreamWithoutFinishReason(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-truncated","choices":[{"delta":{"content":"partial"},"finish_reason":null}]}`,
	)
	message, events := collectOpenAICompletionsFixture(t, stream)
	if message.StopReason != ai.StopReasonError {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "Stream ended without finish_reason" {
		t.Fatalf("error message = %v", message.ErrorMessage)
	}
	if _, ok := events[len(events)-1].(ai.ErrorEvent); !ok {
		t.Fatalf("terminal event = %T, want ai.ErrorEvent", events[len(events)-1])
	}
}

func TestOpenAICompletionsKeepsToolCallsWithoutIndexesSeparate(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-tools","choices":[{"delta":{"tool_calls":[{"id":"call_a","function":{"name":"read","arguments":"{\"path\":\"A"}},{"id":"call_b","function":{"name":"read","arguments":"{\"path\":\"B"}}]},"finish_reason":null}]}`,
		`{"id":"chatcmpl-tools","choices":[{"delta":{"tool_calls":[{"id":"call_b","function":{"arguments":".txt\"}"}},{"id":"call_a","function":{"arguments":".txt\"}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if message.StopReason != ai.StopReasonToolUse {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if len(message.Content) != 2 {
		t.Fatalf("content length = %d", len(message.Content))
	}
	want := []struct {
		id   string
		path string
	}{{"call_a", "A.txt"}, {"call_b", "B.txt"}}
	for index, expected := range want {
		call, ok := message.Content[index].(*ai.ToolCall)
		if !ok {
			t.Fatalf("content[%d] = %T", index, message.Content[index])
		}
		if call.ID != expected.id || call.Arguments["path"] != expected.path {
			t.Fatalf("call[%d] = %#v", index, call)
		}
		if call.StreamIndex != nil || call.PartialArgs != nil {
			t.Fatalf("call[%d] retained streaming scratch: %#v", index, call)
		}
	}
}

func TestOpenAICompletionsPreservesStreamedArgumentOrderOnReplay(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-order","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_order","function":{"name":"echo","arguments":"{\"text\":\"first\",\"mode\":\"plain\",\"metadata\":{\"count\":1}}"}}]},"finish_reason":"tool_calls"}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	converted, include, err := convertOpenAICompletionsAssistantMessage(
		&ai.Model{},
		message,
		resolvedOpenAICompletionsCompat{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !include {
		t.Fatal("streamed tool call replay was omitted")
	}
	calls := converted["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	want := `{"text":"first","mode":"plain","metadata":{"count":1}}`
	if got := function["arguments"]; got != want {
		t.Fatalf("arguments = %q, want %s", got, want)
	}
}

func TestOpenAICompletionsUsesChoiceUsageFallback(t *testing.T) {
	stream := openAICompletionsFixtureStream(t,
		`{"id":"chatcmpl-usage","choices":[{"delta":{},"finish_reason":"stop","usage":{"prompt_tokens":100,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":50,"cache_write_tokens":30},"completion_tokens_details":{"reasoning_tokens":2}}}]}`,
	)
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if message.Usage.Input != 20 || message.Usage.Output != 5 || message.Usage.CacheRead != 50 || message.Usage.CacheWrite != 30 {
		t.Fatalf("usage = %#v", message.Usage)
	}
	if message.Usage.Reasoning == nil || *message.Usage.Reasoning != 2 {
		t.Fatalf("reasoning usage = %v", message.Usage.Reasoning)
	}
	if message.Usage.TotalTokens != 105 {
		t.Fatalf("total tokens = %d", message.Usage.TotalTokens)
	}
}

func TestOpenAICompletionsJSONStringifiesNonFiniteReplayArguments(t *testing.T) {
	negativeZero := math.Copysign(0, -1)
	message := &ai.AssistantMessage{Content: ai.AssistantContent{
		&ai.ToolCall{ID: "call", Name: "echo", Arguments: map[string]any{
			"values": []any{math.NaN(), math.Inf(1), negativeZero},
		}},
	}}
	converted, include, err := convertOpenAICompletionsAssistantMessage(&ai.Model{}, message, resolvedOpenAICompletionsCompat{})
	if err != nil {
		t.Fatal(err)
	}
	if !include {
		t.Fatal("tool call replay was omitted")
	}
	calls := converted["tool_calls"].([]any)
	function := calls[0].(map[string]any)["function"].(map[string]any)
	if got := function["arguments"]; got != `{"values":[null,null,0]}` {
		t.Fatalf("arguments = %q", got)
	}
}

func TestOpenAICompletionsNormalizesReasoningDetailJSON(t *testing.T) {
	output := &ai.AssistantMessage{}
	state := newCompletionsStreamState(output)
	call := &ai.ToolCall{ID: "call"}
	tool := &completionsToolState{block: call}
	state.toolsByID[call.ID] = tool
	state.consumeReasoningDetail([]byte(`{"type":"reasoning.encrypted","id":"call","data":"\u003c","extra":1e2}`))
	want := `{"type":"reasoning.encrypted","id":"call","data":"<","extra":100}`
	if call.ThoughtSignature == nil || *call.ThoughtSignature != want {
		t.Fatalf("thought signature = %v, want %s", call.ThoughtSignature, want)
	}
}

func TestOpenAICompletionsValidatesAuthBeforePayloadHook(t *testing.T) {
	called := false
	model := &ai.Model{ID: "fixture", API: ai.APIOpenAICompletions, Provider: "missing", BaseURL: "https://fixture.invalid/v1"}
	stream, err := StreamOpenAICompletions(context.Background(), ai.Request{
		Model: model,
		Options: &ai.StreamOptions{OnPayload: func(context.Context, any, *ai.Model) (any, bool, error) {
			called = true
			return nil, false, nil
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if called {
		t.Fatal("payload hook ran without authentication")
	}
	if message.ErrorMessage == nil || *message.ErrorMessage != "No API key for provider: missing" {
		t.Fatalf("error message = %v", message.ErrorMessage)
	}
}

func TestOpenAICompletionsOmitsAffinityHeadersForEmptySession(t *testing.T) {
	empty := ""
	headers := buildOpenAICompletionsHeaders(
		&ai.Model{},
		ai.Context{},
		&ai.StreamOptions{SessionID: &empty},
		resolvedOpenAICompletionsCompat{sendSessionAffinityHeaders: true, sessionAffinityFormat: ai.SessionAffinityOpenAI},
		ai.CacheRetentionShort,
	)
	for _, name := range []string{"session_id", "x-client-request-id", "x-session-affinity"} {
		if headers.Get(name) != "" {
			t.Fatalf("%s = %q", name, headers.Get(name))
		}
	}
}

func TestOpenAICompletionsToolCallIDUsesUTF16Semantics(t *testing.T) {
	model := &ai.Model{Provider: "openai"}
	if got := normalizeOpenAICompletionsToolCallID("a😀b|suffix", model, nil); got != "a__b" {
		t.Fatalf("pipe ID = %q", got)
	}
	got := normalizeOpenAICompletionsToolCallID(strings.Repeat("a", 39)+"😀tail", model, nil)
	if utf8.ValidString(got) {
		t.Fatal("UTF-16 slice did not retain the boundary surrogate")
	}
	encoded, err := marshalOpenAICompletionsValue(map[string]any{"id": got})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"id":"` + strings.Repeat("a", 39) + `\ud83d"}`
	if string(encoded) != want {
		t.Fatalf("encoded ID = %s, want %s", encoded, want)
	}
}

func TestSimpleOpenAICompletionsDefaultsToModelMaxTokens(t *testing.T) {
	model := simpleOpenAICompletionsModel()
	model.MaxTokens = 321
	apiKey := "simple-key"
	payload, _ := captureSimpleOpenAICompletionsRequest(t, model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey}})
	if got := payload["max_completion_tokens"]; got != float64(321) {
		t.Fatalf("max_completion_tokens = %v", got)
	}
}

func TestSimpleOpenAICompletionsClampsMaxTokensToContext(t *testing.T) {
	model := simpleOpenAICompletionsModel()
	model.ContextWindow = 5000
	model.MaxTokens = 4096
	apiKey := "simple-key"
	requested := float64(2000)
	payload, _ := captureSimpleOpenAICompletionsRequest(t, model, ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText(strings.Repeat("a", 400))}},
	}, &ai.SimpleStreamOptions{StreamOptions: ai.StreamOptions{APIKey: &apiKey, MaxTokens: &requested}})
	// 5000 context - 100 estimated input - 4096 safety = 804 available.
	if got := payload["max_completion_tokens"]; got != float64(804) {
		t.Fatalf("max_completion_tokens = %v", got)
	}
}

func TestSimpleOpenAICompletionsClampsAndMapsReasoning(t *testing.T) {
	model := simpleOpenAICompletionsModel()
	model.Reasoning = true
	mappedMedium := "vendor-medium"
	mapping := map[ai.ModelThinkingLevel]*string{
		ai.ModelThinkingLow:    nil,
		ai.ModelThinkingMedium: &mappedMedium,
	}
	model.ThinkingLevelMap = &mapping
	apiKey := "simple-key"
	requested := ai.ThinkingLow
	payload, _ := captureSimpleOpenAICompletionsRequest(t, model, ai.Context{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey},
		Reasoning:     &requested,
	})
	if got := payload["reasoning_effort"]; got != mappedMedium {
		t.Fatalf("reasoning_effort = %v", got)
	}

	off := ai.ThinkingLevel("off")
	payload, _ = captureSimpleOpenAICompletionsRequest(t, model, ai.Context{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{APIKey: &apiKey},
		Reasoning:     &off,
	})
	if _, exists := payload["reasoning_effort"]; exists {
		t.Fatalf("off reasoning emitted reasoning_effort: %v", payload["reasoning_effort"])
	}
}

func TestOpenAICompletionsPreservesChatTemplateKwargOrderOnWire(t *testing.T) {
	model := simpleOpenAICompletionsModel()
	model.Reasoning = true
	mapped := "max"
	mapping := map[ai.ModelThinkingLevel]*string{ai.ModelThinkingXHigh: &mapped}
	model.ThinkingLevelMap = &mapping
	model.Compat = json.RawMessage(`{
		"thinkingFormat":"chat-template",
		"supportsReasoningEffort":false,
		"chatTemplateKwargs":{
			"z_static":true,
			"preserve_thinking":true,
			"reasoning_effort":{"$var":"thinking.effort","omitWhenOff":true},
			"a_static":7
		}
	}`)
	apiKey := "simple-key"
	reasoning := ai.ThinkingXHigh
	hookSawMap := false
	body, _ := captureSimpleOpenAICompletionsRequestBody(t, model, ai.Context{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &apiKey,
			OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
				kwargs, ok := payload.(map[string]any)["chat_template_kwargs"].(map[string]any)
				hookSawMap = ok && len(kwargs) == 4
				return nil, false, nil
			},
		},
		Reasoning: &reasoning,
	})
	if !hookSawMap {
		t.Fatal("payload hook did not receive mutable chat_template_kwargs map")
	}
	want := `"chat_template_kwargs":{"z_static":true,"preserve_thinking":true,"reasoning_effort":"max","a_static":7}`
	if !strings.Contains(string(body), want) {
		t.Fatalf("request body does not preserve compat order:\n%s\nwant substring:\n%s", body, want)
	}
}

func TestSimpleOpenAICompletionsForwardsBaseOptions(t *testing.T) {
	model := simpleOpenAICompletionsModel()
	model.BaseURL = "https://api.openai.com/v1"
	apiKey := "simple-key"
	temperature := 0.0
	long := ai.CacheRetentionLong
	sessionID := "simple-session"
	headerValue := "forwarded"
	payloadHookCalled := false
	headersHookCalled := false
	responseHookCalled := false
	payload, headers := captureSimpleOpenAICompletionsRequest(t, model, ai.Context{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey:         &apiKey,
			Temperature:    &temperature,
			CacheRetention: &long,
			SessionID:      &sessionID,
			Headers:        ai.ProviderHeaders{"x-simple-option": &headerValue},
			OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
				payloadHookCalled = true
				payload.(map[string]any)["simple_hook"] = true
				return nil, false, nil
			},
			TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
				hasSimpleOption := false
				for name, header := range headers {
					if strings.EqualFold(name, "X-Simple-Option") && header != nil {
						hasSimpleOption = true
						delete(headers, name)
					}
				}
				headersHookCalled = hasSimpleOption
				value := "extension"
				headers["x-extension"] = &value
				return headers, nil
			},
			OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
				responseHookCalled = response.Status == http.StatusOK
				return nil
			},
		},
	})
	if !payloadHookCalled || !headersHookCalled || !responseHookCalled {
		t.Fatalf("hooks called = payload %v, headers %v, response %v", payloadHookCalled, headersHookCalled, responseHookCalled)
	}
	if payload["temperature"] != float64(0) || payload["prompt_cache_key"] != sessionID || payload["prompt_cache_retention"] != "24h" || payload["simple_hook"] != true {
		t.Fatalf("forwarded payload = %#v", payload)
	}
	if _, exists := payload["tool_choice"]; exists {
		t.Fatalf("typed simple options unexpectedly emitted tool_choice: %v", payload["tool_choice"])
	}
	if headers.Get("Authorization") != "Bearer "+apiKey || headers.Get("X-Simple-Option") != "" || headers.Get("X-Extension") != "extension" {
		t.Fatalf("forwarded headers = %#v", headers)
	}
}

func simpleOpenAICompletionsModel() *ai.Model {
	return &ai.Model{
		ID:            "simple-model",
		API:           ai.APIOpenAICompletions,
		Provider:      "openai",
		BaseURL:       "https://fixture.invalid/v1",
		Input:         ai.InputModalities{ai.InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}
}

func captureSimpleOpenAICompletionsRequest(
	t *testing.T,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (map[string]any, http.Header) {
	t.Helper()
	body, headers := captureSimpleOpenAICompletionsRequestBody(t, model, requestContext, options)
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode request body: %v\n%s", err, body)
	}
	return payload, headers
}

func captureSimpleOpenAICompletionsRequestBody(
	t *testing.T,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) ([]byte, http.Header) {
	t.Helper()
	previousClient := openAIHTTPClient
	var body []byte
	var headers http.Header
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		headers = request.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"id":"chatcmpl-simple","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	stream, err := StreamSimpleOpenAICompletions(context.Background(), model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	message, _ := collectOpenAICompletionsFixture(t, stream)
	if message.StopReason != ai.StopReasonStop {
		t.Fatalf("stop reason = %q, error = %v", message.StopReason, message.ErrorMessage)
	}
	return body, headers
}

func openAICompletionsFixtureStream(t *testing.T, chunks ...string) ai.AssistantMessageEventStream {
	t.Helper()
	previousClient := openAIHTTPClient
	var body strings.Builder
	for _, chunk := range chunks {
		fmt.Fprintf(&body, "data: %s\n\n", chunk)
	}
	body.WriteString("data: [DONE]\n\n")
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", request.URL.Path)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(body.String())),
			Request:    request,
		}, nil
	})}
	t.Cleanup(func() { openAIHTTPClient = previousClient })

	key := "fixture-key"
	model := &ai.Model{
		ID:       "fixture-model",
		API:      ai.APIOpenAICompletions,
		Provider: "openai",
		BaseURL:  "https://fixture.invalid/v1/",
		Input:    ai.InputModalities{ai.InputText},
		Cost:     ai.ModelCost{},
	}
	stream, err := StreamOpenAICompletions(context.Background(), ai.Request{
		Model: model,
		Context: ai.Context{Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("test")},
		}},
		Options: &ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func collectOpenAICompletionsFixture(
	t *testing.T,
	stream ai.AssistantMessageEventStream,
) (*ai.AssistantMessage, []ai.AssistantMessageEvent) {
	t.Helper()
	var terminal *ai.AssistantMessage
	events := make([]ai.AssistantMessageEvent, 0)
	for event, err := range stream {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
		switch value := event.(type) {
		case ai.DoneEvent:
			terminal = value.Message
		case ai.ErrorEvent:
			terminal = value.Error
		}
	}
	if terminal == nil {
		t.Fatal("stream did not emit a terminal event")
	}
	return terminal, events
}
