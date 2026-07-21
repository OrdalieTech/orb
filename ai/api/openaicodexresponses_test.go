package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestOpenAICodexRequestShapeAndDoneEvent(t *testing.T) {
	model := codexTestModel()
	high := "xhigh"
	model.ThinkingLevelMap = &map[ai.ModelThinkingLevel]*string{ai.ModelThinkingHigh: &high}
	system := "Use the tool."
	message := "hello"
	tools := []ai.Tool{{
		Name: "echo", Description: "Echo text",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}`),
	}}
	requestContext := ai.Context{
		SystemPrompt: &system,
		Messages:     ai.MessageList{&ai.UserMessage{Content: ai.NewUserText(message), Timestamp: 1}},
		Tools:        &tools,
	}
	token := codexAPITestToken(t, "account-fixture")
	session := strings.Repeat("s", 64) + "-tail"
	temperature := 0.0
	effort, summary, tier, verbosity, toolChoice := "high", "concise", "priority", "high", "required"
	transport := ai.TransportSSE
	override := "wrong"
	options := &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &token, Transport: &transport, SessionID: &session, Temperature: &temperature,
			Headers: ai.ProviderHeaders{"authorization": &override, "originator": &override},
		},
		ReasoningEffort: &effort, ReasoningSummary: &summary, ServiceTier: &tier,
		TextVerbosity: &verbosity, ToolChoice: &toolChoice,
	}

	var capturedBody string
	var capturedHeaders http.Header
	var capturedURL string
	withCodexHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		capturedBody, capturedHeaders, capturedURL = string(body), request.Header.Clone(), request.URL.String()
		return codexHTTPResponse(http.StatusOK, codexSSE(
			map[string]any{"type": "response.done", "response": map[string]any{
				"id": "resp-codex", "status": "completed", "service_tier": "default", "output": []any{},
				"usage": map[string]any{"input_tokens": 10, "output_tokens": 3, "total_tokens": 13, "input_tokens_details": map[string]any{"cached_tokens": 2}, "output_tokens_details": map[string]any{"reasoning_tokens": 1}},
			}},
		)), nil
	})
	previousNow := openAINowUnixMilli
	openAINowUnixMilli = func() int64 { return 1_700_000_000_123 }
	t.Cleanup(func() { openAINowUnixMilli = previousNow })
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	messageResult, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if messageResult.StopReason != ai.StopReasonStop || messageResult.ResponseID == nil || *messageResult.ResponseID != "resp-codex" {
		t.Fatalf("message = %#v", messageResult)
	}
	if messageResult.Usage.Input != 8 || messageResult.Usage.CacheRead != 2 || messageResult.Usage.Cost.Input != 16 || messageResult.Usage.Cost.Output != 12 || messageResult.Usage.Cost.CacheRead != 12 || messageResult.Usage.Cost.Total != 40 {
		t.Fatalf("usage = %#v", messageResult.Usage)
	}
	wantBody := `{"model":"gpt-codex-fixture","store":false,"stream":true,"instructions":"Use the tool.","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"text":{"verbosity":"high"},"include":["reasoning.encrypted_content"],"prompt_cache_key":"` + strings.Repeat("s", 64) + `","tool_choice":"required","parallel_tool_calls":true,"temperature":0,"service_tier":"priority","tools":[{"type":"function","name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},"strict":null}],"reasoning":{"effort":"xhigh","summary":"concise"}}`
	if capturedBody != wantBody {
		t.Fatalf("body = %s\nwant = %s", capturedBody, wantBody)
	}
	if capturedURL != "https://codex.fixture.invalid/backend-api/codex/responses" {
		t.Fatalf("URL = %q", capturedURL)
	}
	if capturedHeaders.Get("Authorization") != "Bearer "+token || capturedHeaders.Get("chatgpt-account-id") != "account-fixture" || capturedHeaders.Get("originator") != "pi" || capturedHeaders.Get("session-id") != strings.Repeat("s", 64) || capturedHeaders.Get("x-client-request-id") != strings.Repeat("s", 64) || capturedHeaders.Get("OpenAI-Beta") != "responses=experimental" || !strings.HasPrefix(capturedHeaders.Get("User-Agent"), "pi (") {
		t.Fatalf("headers = %#v", capturedHeaders)
	}
}

func TestOpenAICodexStreamErrorAndInvalidToken(t *testing.T) {
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportSSE
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return codexHTTPResponse(http.StatusOK, codexSSE(map[string]any{"type": "error", "code": "bad", "message": "denied"})), nil
	})
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "Codex error: denied" {
		t.Fatalf("message = %#v", message)
	}

	invalid := "not-a-token"
	stream, err = StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &invalid, Transport: &transport}})
	if err != nil {
		t.Fatal(err)
	}
	message, err = ai.Collect(stream)
	if err != nil || message.ErrorMessage == nil || *message.ErrorMessage != "Failed to extract accountId from token" {
		t.Fatalf("invalid token result = %#v, %v", message, err)
	}
}

func TestOpenAICodexRetriesAndCaps429Delay(t *testing.T) {
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	maxRetries := 1
	maxDelay := int64(3)
	transport := ai.TransportSSE
	requests := 0
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			response := codexHTTPResponse(http.StatusTooManyRequests, `{"error":{"message":"busy"}}`)
			response.Header.Set("Retry-After-Ms", "10000")
			return response, nil
		}
		return codexHTTPResponse(http.StatusOK, codexSSE(map[string]any{"type": "response.completed", "response": map[string]any{"id": "ok", "status": "completed", "output": []any{}}})), nil
	})
	previousSleep := openAICodexSleep
	var slept time.Duration
	openAICodexSleep = func(_ context.Context, duration time.Duration) error { slept = duration; return nil }
	t.Cleanup(func() { openAICodexSleep = previousSleep })
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, MaxRetries: &maxRetries, MaxRetryDelayMS: &maxDelay}})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil || message.StopReason != ai.StopReasonStop || requests != 2 || slept != 3*time.Millisecond {
		t.Fatalf("retry result = %#v, %v, requests=%d slept=%s", message, err, requests, slept)
	}
}

func TestOpenAICodexRetriesNonRetryableHTTPErrorLikeUpstream(t *testing.T) {
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	maxRetries := 1
	transport := ai.TransportSSE
	requests := 0
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return codexHTTPResponse(http.StatusBadRequest, `{"error":{"message":"bad request"}}`), nil
		}
		return codexHTTPResponse(http.StatusOK, codexSSE(map[string]any{
			"type": "response.done", "response": map[string]any{"id": "retried", "status": "completed", "output": []any{}},
		})), nil
	})
	previousSleep := openAICodexSleep
	var slept time.Duration
	openAICodexSleep = func(_ context.Context, duration time.Duration) error { slept = duration; return nil }
	t.Cleanup(func() { openAICodexSleep = previousSleep })
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, MaxRetries: &maxRetries},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil || message.StopReason != ai.StopReasonStop || requests != 2 || slept != time.Second {
		t.Fatalf("retry result = %#v, %v, requests=%d slept=%s", message, err, requests, slept)
	}
}

func TestOpenAICodexRecordsTransportFailure(t *testing.T) {
	output := newAssistantMessage(&ai.Model{API: ai.APIOpenAICodexResponses, Provider: "openai-codex", ID: "fixture"})
	failure := errors.New("dial failed")
	if err := appendOpenAICodexTransportFailure(output, nil, failure, false, 123); err != nil {
		t.Fatal(err)
	}
	if output.Diagnostics == nil || len(*output.Diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v", output.Diagnostics)
	}
	diagnostic := (*output.Diagnostics)[0]
	if diagnostic.Type != "provider_transport_failure" || diagnostic.Error == nil || diagnostic.Error.Message != "dial failed" {
		t.Fatalf("diagnostic = %#v", diagnostic)
	}
	want := `{"configuredTransport":"auto","fallbackTransport":"sse","eventsEmitted":false,"phase":"before_message_stream_start","requestBytes":123}`
	if string(diagnostic.Details) != want {
		t.Fatalf("details = %s, want %s", diagnostic.Details, want)
	}

	websocket := ai.TransportWebSocket
	output.Diagnostics = nil
	if err := appendOpenAICodexTransportFailure(output, &ai.StreamOptions{Transport: &websocket}, failure, true, 123); err != nil {
		t.Fatal(err)
	}
	want = `{"configuredTransport":"websocket","eventsEmitted":true,"phase":"after_message_stream_start","requestBytes":123}`
	if output.Diagnostics == nil || len(*output.Diagnostics) != 1 || string((*output.Diagnostics)[0].Details) != want {
		t.Fatalf("started diagnostic = %#v, want details %s", output.Diagnostics, want)
	}
}

func TestOpenAICodexTimeoutOnlyBoundsResponseHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		writer.(http.Flusher).Flush()
		time.Sleep(250 * time.Millisecond)
		_, _ = io.WriteString(writer, codexSSE(map[string]any{
			"type": "response.done", "response": map[string]any{"id": "delayed-body", "status": "completed", "output": []any{}},
		}))
	}))
	defer server.Close()
	previous := openAIHTTPClient
	openAIHTTPClient = server.Client()
	t.Cleanup(func() { openAIHTTPClient = previous })

	model := codexTestModel()
	model.BaseURL = server.URL
	token := codexAPITestToken(t, "account")
	transport := ai.TransportSSE
	timeout := int64(100)
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, TimeoutMS: &timeout},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil || message.StopReason != ai.StopReasonStop || message.ResponseID == nil || *message.ResponseID != "delayed-body" {
		t.Fatalf("delayed body result = %#v, %v", message, err)
	}
}

func TestOpenAICodexEmptySessionIDStillSerializesPromptCacheKey(t *testing.T) {
	model := codexTestModel()
	empty := ""
	payload, err := buildOpenAICodexResponsesPayload(&model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{SessionID: &empty},
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.PromptCacheKey == nil || *payload.PromptCacheKey != "" {
		t.Fatalf("prompt_cache_key = %#v", payload.PromptCacheKey)
	}
	encoded, err := ai.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"prompt_cache_key":""`) {
		t.Fatalf("payload omitted explicit empty prompt_cache_key: %s", encoded)
	}
}

func TestOpenAICodexNullThinkingMappingOmitsReasoning(t *testing.T) {
	model := codexTestModel()
	model.ThinkingLevelMap = &map[ai.ModelThinkingLevel]*string{ai.ModelThinkingHigh: nil}
	high := "high"
	payload, err := buildOpenAICodexResponsesPayload(&model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		ReasoningEffort: &high,
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning != nil {
		t.Fatalf("reasoning = %#v", payload.Reasoning)
	}
}

func codexTestModel() ai.Model {
	return ai.Model{
		ID: "gpt-codex-fixture", API: ai.APIOpenAICodexResponses, Provider: "openai-codex",
		BaseURL: "https://codex.fixture.invalid/backend-api/", Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1_000_000, Output: 2_000_000, CacheRead: 3_000_000, CacheWrite: 4_000_000}},
		ContextWindow: 128000, MaxTokens: 8192,
	}
}

func codexAPITestToken(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_account_id": accountID}})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func codexSSE(events ...map[string]any) string {
	var builder strings.Builder
	for _, event := range events {
		encoded, _ := json.Marshal(event)
		builder.WriteString("data: ")
		builder.Write(encoded)
		builder.WriteString("\n\n")
	}
	return builder.String()
}

func codexHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(body)),
	}
}

func withCodexHTTPClient(t *testing.T, roundTrip func(*http.Request) (*http.Response, error)) {
	t.Helper()
	previous := openAIHTTPClient
	openAIHTTPClient = &http.Client{Transport: roundTripFunc(roundTrip)}
	t.Cleanup(func() { openAIHTTPClient = previous })
}
