package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

const piMessagesTestNow int64 = 1_700_000_000_123

func TestPiMessagesRoundTripAgainstServer(t *testing.T) {
	var capturedMethod, capturedURL, capturedBody string
	var capturedHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		capturedMethod = request.Method
		capturedURL = request.URL.RequestURI()
		capturedBody = string(body)
		capturedHeaders = request.Header.Clone()
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.Header().Set("X-Pi-Gateway-Upstream-Provider", "fixture-upstream")
		_, _ = io.WriteString(writer, strings.Join([]string{
			`data: {"type":"start"}`,
			`data: {"type":"thinking_start","contentIndex":0}`,
			`data: {"type":"thinking_delta","contentIndex":0,"delta":"plan"}`,
			`data: {"type":"thinking_end","contentIndex":0,"content":"plan","contentSignature":"opaque-thinking","redacted":true}`,
			`data: {"type":"text_start","contentIndex":1}`,
			`data: {"type":"text_delta","contentIndex":1,"delta":"Hel"}`,
			`data: {"type":"text_delta","contentIndex":1,"delta":"lo"}`,
			`data: {"type":"text_end","contentIndex":1,"content":"Hello","contentSignature":"opaque-text"}`,
			`data: {"type":"toolcall_start","contentIndex":2,"id":"call_1","toolName":"read"}`,
			`data: {"type":"toolcall_delta","contentIndex":2,"delta":"{\"path\":"}`,
			`data: {"type":"toolcall_delta","contentIndex":2,"delta":"\"a.txt\"}"}`,
			`data: {"type":"toolcall_end","contentIndex":2,"toolCall":{"type":"toolCall","id":"call_1","name":"read","arguments":{"path":"a.txt"}}}`,
			`data: {"type":"done","reason":"toolUse","usage":{"input":10,"output":5,"cacheRead":0,"cacheWrite":0,"totalTokens":15,"cost":{"input":0.1,"output":0.2,"cacheRead":0,"cacheWrite":0,"total":0.3}},"responseId":"resp_1","rewrite":{"policyId":"fixture-policy","policyVersion":2,"changed":true,"tokenCountChange":-3,"messageCountChange":-1,"systemPromptChanged":false}}`,
			`data: [DONE]`,
		}, "\r\n\r\n"))
	}))
	defer server.Close()

	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return piMessagesTestNow }
	t.Cleanup(func() { openAINowUnixMilli = previousNow })

	modelHeaders := map[string]string{"x-model-header": "ignored"}
	model := piMessagesTestModel(server.URL+"/v1///", &modelHeaders)
	apiKey := "test-key"
	maxTokens := float64(100)
	sessionID := "session-1"
	reasoning := ai.ThinkingHigh
	customHeader := "1"
	nilHeader := (*string)(nil)
	var responseHeaders map[string]string
	stream, err := StreamPiMessagesWithOptions(context.Background(), model, piMessagesTestContext(), &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &apiKey, MaxTokens: &maxTokens, SessionID: &sessionID,
			Headers: ai.ProviderHeaders{"x-custom": &customHeader, "x-ignored": nilHeader},
			Env:     ai.ProviderEnv{"PI_CACHE_RETENTION": "long"},
			OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
				responseHeaders = response.Headers
				return nil
			},
		},
		Reasoning: reasoningPointer(reasoning),
		ToolChoice: map[string]any{
			"type": "function", "function": map[string]any{"name": "read"},
		},
		Debug: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	var events []ai.AssistantMessageEvent
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		events = append(events, event)
	}
	if len(events) != 13 {
		t.Fatalf("event count = %d, want 13", len(events))
	}
	terminal, ok := events[len(events)-1].(ai.DoneEvent)
	if !ok {
		t.Fatalf("terminal event = %T, want ai.DoneEvent", events[len(events)-1])
	}
	message := terminal.Message
	if message.StopReason != ai.StopReasonToolUse || message.ResponseID == nil || *message.ResponseID != "resp_1" {
		t.Fatalf("terminal message = %#v", message)
	}
	if len(message.Content) != 3 {
		t.Fatalf("content count = %d, want 3", len(message.Content))
	}
	thinking, ok := message.Content[0].(*ai.ThinkingContent)
	if !ok || thinking.Thinking != "plan" || thinking.ThinkingSignature == nil || *thinking.ThinkingSignature != "opaque-thinking" || thinking.Redacted == nil || !*thinking.Redacted {
		t.Fatalf("thinking block = %#v", message.Content[0])
	}
	text, ok := message.Content[1].(*ai.TextContent)
	if !ok || text.Text != "Hello" || text.TextSignature == nil || *text.TextSignature != "opaque-text" {
		t.Fatalf("text block = %#v", message.Content[1])
	}
	call, ok := message.Content[2].(*ai.ToolCall)
	if !ok || call.Name != "read" || call.Arguments["path"] != "a.txt" {
		t.Fatalf("tool call = %#v", message.Content[2])
	}
	if message.Diagnostics == nil || len(*message.Diagnostics) != 1 || (*message.Diagnostics)[0].Type != "pi_messages_rewrite" {
		t.Fatalf("diagnostics = %#v", message.Diagnostics)
	}
	if got := string((*message.Diagnostics)[0].Details); got != `{"policyId":"fixture-policy","policyVersion":2,"changed":true,"tokenCountChange":-3,"messageCountChange":-1,"systemPromptChanged":false}` {
		t.Fatalf("rewrite details = %s", got)
	}

	if capturedMethod != http.MethodPost || capturedURL != "/v1/messages?debug=1" {
		t.Fatalf("request = %s %s", capturedMethod, capturedURL)
	}
	if capturedHeaders.Get("Authorization") != "Bearer test-key" || capturedHeaders.Get("Accept") != "text/event-stream" || capturedHeaders.Get("Content-Type") != "application/json" || capturedHeaders.Get("X-Custom") != "1" {
		t.Fatalf("headers = %#v", capturedHeaders)
	}
	if capturedHeaders.Get("X-Model-Header") != "" || capturedHeaders.Get("X-Ignored") != "" {
		t.Fatalf("unexpected inherited headers = %#v", capturedHeaders)
	}
	wantBody := `{"model":"fixture-model","context":{"systemPrompt":"Be concise.","messages":[{"role":"user","content":"Hello","timestamp":1700000000123}]},"options":{"maxTokens":100,"reasoning":"high","cacheRetention":"long","sessionId":"session-1","toolChoice":{"function":{"name":"read"},"type":"function"}}}`
	if capturedBody != wantBody {
		t.Fatalf("request body mismatch\nwant: %s\n got: %s", wantBody, capturedBody)
	}
	if responseHeaders["x-pi-gateway-upstream-provider"] != "fixture-upstream" {
		t.Fatalf("response headers = %#v", responseHeaders)
	}
}

func TestPiMessagesHooksAndEnvironmentCacheRetention(t *testing.T) {
	t.Setenv("PI_CACHE_RETENTION", "long")
	var responseHeaders map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		if string(body) != `{"replacement":true}` {
			t.Errorf("hooked body = %s", body)
		}
		if request.Header.Get("X-Extension") != "yes" {
			t.Errorf("hooked header = %q", request.Header.Get("X-Extension"))
		}
		response.Header().Set("X-Pi-Gateway-Upstream-Provider", "anthropic")
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(response, `data: {"type":"done","reason":"stop","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`+"\n\n")
	}))
	t.Cleanup(server.Close)
	apiKey := "key"
	options := &PiMessagesOptions{StreamOptions: ai.StreamOptions{
		APIKey: &apiKey,
		OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
			original, ok := payload.(PiMessagesPayload)
			if !ok || original.Options.CacheRetention == nil || *original.Options.CacheRetention != ai.CacheRetentionLong {
				t.Fatalf("payload before hook = %#v", payload)
			}
			return struct {
				Replacement bool `json:"replacement"`
			}{true}, true, nil
		},
		OnResponse: func(_ context.Context, response ai.ProviderResponse, _ *ai.Model) error {
			responseHeaders = response.Headers
			return nil
		},
		TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
			value := "yes"
			headers["X-Extension"] = &value
			return headers, nil
		},
	}}
	stream, err := StreamPiMessagesWithOptions(context.Background(), piMessagesTestModel(server.URL, nil), ai.Context{Messages: ai.MessageList{}}, options)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonStop {
		t.Fatalf("stop reason = %q", message.StopReason)
	}
	if responseHeaders["x-pi-gateway-upstream-provider"] != "anthropic" {
		t.Fatalf("response headers = %#v", responseHeaders)
	}
}

func TestPiMessagesFailurePaths(t *testing.T) {
	t.Run("structured response error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, `{"error":{"message":"Token expired","code":"unauthorized","details":{"retry":false}}}`)
		}))
		defer server.Close()
		message := collectPiMessages(t, context.Background(), piMessagesTestModel(server.URL, nil), piMessagesTestContext(), "stale")
		if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "401 Unauthorized: Token expired (unauthorized)" {
			t.Fatalf("message = %#v", message)
		}
		if message.Diagnostics == nil || len(*message.Diagnostics) != 1 {
			t.Fatalf("diagnostics = %#v", message.Diagnostics)
		}
		diagnostic := (*message.Diagnostics)[0]
		if diagnostic.Type != "pi_messages_response_failure" || diagnostic.Error == nil || diagnostic.Error.Name == nil || *diagnostic.Error.Name != "PiMessagesResponseError" {
			t.Fatalf("diagnostic = %#v", diagnostic)
		}
		var details struct {
			Status int `json:"status"`
			Error  struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal(diagnostic.Details, &details); err != nil {
			t.Fatal(err)
		}
		if details.Status != http.StatusUnauthorized || details.Error.Code != "unauthorized" {
			t.Fatalf("details = %#v", details)
		}
	})

	t.Run("server event error", func(t *testing.T) {
		server := piMessagesEventServer(t, `data: {"type":"error","reason":"error","usage":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":1,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"errorMessage":"Upstream failed"}`+"\n\n")
		defer server.Close()
		message := collectPiMessages(t, context.Background(), piMessagesTestModel(server.URL, nil), piMessagesTestContext(), "key")
		if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "Upstream failed" || message.Usage.Input != 1 {
			t.Fatalf("message = %#v", message)
		}
	})

	t.Run("missing key", func(t *testing.T) {
		stream, err := StreamPiMessagesWithOptions(context.Background(), piMessagesTestModel("http://127.0.0.1:1", nil), piMessagesTestContext(), nil)
		if err != nil {
			t.Fatal(err)
		}
		message, err := ai.Collect(stream)
		if err != nil {
			t.Fatal(err)
		}
		if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "No API key provided") {
			t.Fatalf("message = %#v", message)
		}
	})

	t.Run("missing terminal event", func(t *testing.T) {
		server := piMessagesEventServer(t, "data: {\"type\":\"start\"}\n\ndata: {\"type\":\"text_start\",\"contentIndex\":0}\n\ndata: {\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"partial\"}\n\n")
		defer server.Close()
		message := collectPiMessages(t, context.Background(), piMessagesTestModel(server.URL, nil), piMessagesTestContext(), "key")
		if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, "stream ended without a terminal event") {
			t.Fatalf("message = %#v", message)
		}
	})

	t.Run("abort", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
			writer.WriteHeader(http.StatusGatewayTimeout)
		}))
		defer server.Close()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		message := collectPiMessages(t, ctx, piMessagesTestModel(server.URL, nil), piMessagesTestContext(), "key")
		if message.StopReason != ai.StopReasonAborted {
			t.Fatalf("stop reason = %q, want aborted", message.StopReason)
		}
	})
}

func TestPiMessagesPayloadHookAndHeaderOverride(t *testing.T) {
	var body string
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		contents, err := io.ReadAll(request.Body)
		if err != nil {
			t.Error(err)
			return
		}
		body = string(contents)
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, `data: {"type":"done","reason":"stop","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`+"\n\n")
	}))
	defer server.Close()
	key := "key"
	override := "Gateway custom-token"
	stream, err := StreamPiMessagesWithOptions(context.Background(), piMessagesTestModel(server.URL, nil), piMessagesTestContext(), &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{
			APIKey:  &key,
			Headers: ai.ProviderHeaders{"authorization": &override},
			OnPayload: func(_ context.Context, _ any, _ *ai.Model) (any, bool, error) {
				return map[string]any{"replacement": true}, true, nil
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
	if message.StopReason != ai.StopReasonStop || body != `{"replacement":true}` || authorization != override {
		t.Fatalf("message=%#v body=%s authorization=%q", message, body, authorization)
	}
}

// TestPiMessagesUnknownEventTypesPassThrough_OTM6 pins the upstream converter
// (pi-messages.ts:189-263): unknown event objects are re-emitted with the
// evolving partial message, and the stream still completes. (OT-M6)
func TestPiMessagesUnknownEventTypesPassThrough_OTM6(t *testing.T) {
	server := piMessagesEventServer(t, strings.Join([]string{
		`data: {"type":"start"}`,
		`data: {"type":"future_event","contentIndex":0,"delta":"ignored"}`,
		`data: {"type":"text_start","contentIndex":0}`,
		`data: {"type":"text_delta","contentIndex":0,"delta":"Hello"}`,
		`data: {"type":"another_unknown"}`,
		`data: {"type":"text_end","contentIndex":0,"content":"Hello"}`,
		`data: {"type":"done","reason":"stop","usage":{"input":1,"output":2,"cacheRead":0,"cacheWrite":0,"totalTokens":3,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}}`,
		`data: [DONE]`,
	}, "\n\n")+"\n\n")
	defer server.Close()
	key := "key"
	stream, err := StreamPiMessagesWithOptions(context.Background(), piMessagesTestModel(server.URL, nil), piMessagesTestContext(), &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	var message *ai.AssistantMessage
	var eventTypes []string
	unknown := make(map[string]map[string]any)
	for event, streamErr := range stream {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		encoded, marshalErr := ai.MarshalAssistantMessageEvent(event)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		var decoded map[string]any
		if unmarshalErr := json.Unmarshal(encoded, &decoded); unmarshalErr != nil {
			t.Fatal(unmarshalErr)
		}
		eventType, _ := decoded["type"].(string)
		eventTypes = append(eventTypes, eventType)
		if eventType == "future_event" || eventType == "another_unknown" {
			unknown[eventType] = decoded
		}
		if done, ok := event.(ai.DoneEvent); ok {
			message = done.Message
		}
	}
	wantTypes := "start,future_event,text_start,text_delta,another_unknown,text_end,done"
	if got := strings.Join(eventTypes, ","); got != wantTypes {
		t.Fatalf("event types = %q, want %q", got, wantTypes)
	}
	future := unknown["future_event"]
	if future["contentIndex"] != float64(0) || future["delta"] != "ignored" {
		t.Fatalf("future event fields = %#v", future)
	}
	partial, ok := future["partial"].(map[string]any)
	if !ok || partial["role"] != "assistant" {
		t.Fatalf("future event partial = %#v", future["partial"])
	}
	afterDelta, ok := unknown["another_unknown"]
	if !ok {
		t.Fatalf("unknown events = %#v", unknown)
	}
	afterPartial, ok := afterDelta["partial"].(map[string]any)
	if !ok {
		t.Fatalf("post-delta partial = %#v", afterDelta["partial"])
	}
	content, ok := afterPartial["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("post-delta content = %#v", afterPartial["content"])
	}
	textBlock, ok := content[0].(map[string]any)
	if !ok || textBlock["text"] != "Hello" {
		t.Fatalf("post-delta text block = %#v", content[0])
	}
	if message == nil {
		t.Fatal("missing terminal message")
	}
	if message.StopReason != ai.StopReasonStop {
		errorMessage := ""
		if message.ErrorMessage != nil {
			errorMessage = *message.ErrorMessage
		}
		t.Fatalf("stop reason = %q (error %q), want stop", message.StopReason, errorMessage)
	}
	if len(message.Content) != 1 {
		t.Fatalf("content = %#v, want a single text block", message.Content)
	}
	text, ok := message.Content[0].(*ai.TextContent)
	if !ok || text.Text != "Hello" {
		t.Fatalf("text block = %#v", message.Content[0])
	}
}

func piMessagesTestModel(baseURL string, headers *map[string]string) *ai.Model {
	return &ai.Model{
		ID: "fixture-model", Name: "Fixture Gateway Model", API: ai.APIPiMessages,
		Provider: "fixture-gateway", BaseURL: baseURL, Input: ai.InputModalities{ai.InputText},
		ContextWindow: 128_000, MaxTokens: 16_384, Headers: headers,
	}
}

func piMessagesTestContext() ai.Context {
	system := "Be concise."
	return ai.Context{SystemPrompt: &system, Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("Hello"), Timestamp: piMessagesTestNow},
	}}
}

func reasoningPointer(reasoning ai.ThinkingLevel) *ai.ThinkingLevel { return &reasoning }

func collectPiMessages(t *testing.T, ctx context.Context, model *ai.Model, requestContext ai.Context, key string) *ai.AssistantMessage {
	t.Helper()
	stream, err := StreamPiMessagesWithOptions(ctx, model, requestContext, &PiMessagesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func piMessagesEventServer(t *testing.T, events string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, events)
	}))
}
