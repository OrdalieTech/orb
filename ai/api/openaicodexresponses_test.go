package api

import (
	"bytes"
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

	"github.com/klauspost/compress/zstd"

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

	var capturedBody []byte
	var capturedHeaders http.Header
	var capturedURL string
	withCodexHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		capturedBody, capturedHeaders, capturedURL = body, request.Header.Clone(), request.URL.String()
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
	if capturedHeaders.Get("Content-Encoding") != "zstd" {
		t.Fatalf("Content-Encoding = %q", capturedHeaders.Get("Content-Encoding"))
	}
	decodedBody, err := codexDecodeZstd(capturedBody)
	if err != nil {
		t.Fatal(err)
	}
	wantBody := `{"model":"gpt-codex-fixture","store":false,"stream":true,"instructions":"Use the tool.","input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}],"text":{"verbosity":"high"},"include":["reasoning.encrypted_content"],"prompt_cache_key":"` + strings.Repeat("s", 64) + `","tool_choice":"required","parallel_tool_calls":true,"temperature":0,"service_tier":"priority","tools":[{"type":"function","name":"echo","description":"Echo text","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},"strict":null}],"reasoning":{"effort":"xhigh","summary":"concise"}}`
	if string(decodedBody) != wantBody {
		t.Fatalf("body = %s\nwant = %s", decodedBody, wantBody)
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

// CX-M4: upstream coalesces a null thinkingLevelMap entry back to the requested
// effort with ??, so reasoning{effort, summary:"auto"} must still be sent.
func TestCXM4OpenAICodexNullThinkingMappingFallsBackToEffort(t *testing.T) {
	model := codexTestModel()
	model.ThinkingLevelMap = &map[ai.ModelThinkingLevel]*string{ai.ModelThinkingHigh: nil, ai.ModelThinkingOff: nil}
	high := "high"
	payload, err := buildOpenAICodexResponsesPayload(&model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		ReasoningEffort: &high,
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "high" || payload.Reasoning.Summary == nil || *payload.Reasoning.Summary != "auto" {
		t.Fatalf("reasoning = %#v", payload.Reasoning)
	}

	none := "none"
	payload, err = buildOpenAICodexResponsesPayload(&model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		ReasoningEffort: &none,
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "none" {
		t.Fatalf("none reasoning = %#v", payload.Reasoning)
	}

	minimal := "minimal"
	model.ThinkingLevelMap = &map[ai.ModelThinkingLevel]*string{ai.ModelThinkingOff: &minimal}
	payload, err = buildOpenAICodexResponsesPayload(&model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		ReasoningEffort: &none,
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload.Reasoning == nil || payload.Reasoning.Effort != "minimal" {
		t.Fatalf("mapped none reasoning = %#v", payload.Reasoning)
	}
}

// B1: a consumer breaking out of the SSE event stream must end the iterator
// silently instead of panicking via a yield-after-false fail().
func TestB1OpenAICodexSSEConsumerBreakStopsCleanly(t *testing.T) {
	for breakAfter := 1; breakAfter <= 3; breakAfter++ {
		model := codexTestModel()
		token := codexAPITestToken(t, "account")
		transport := ai.TransportSSE
		withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
			return codexHTTPResponse(http.StatusOK, codexSSE(
				map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "m1", "role": "assistant", "status": "in_progress", "content": []any{}}},
				map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": "hello"},
				map[string]any{"type": "response.done", "response": map[string]any{"id": "resp-b1", "status": "completed", "output": []any{}}},
			)), nil
		})
		stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
			StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport},
		})
		if err != nil {
			t.Fatal(err)
		}
		events := 0
		for _, streamErr := range stream {
			if streamErr != nil {
				t.Fatal(streamErr)
			}
			events++
			if events == breakAfter {
				break
			}
		}
		if events != breakAfter {
			t.Fatalf("breakAfter=%d saw %d events", breakAfter, events)
		}
	}
}

// CX-M3: the SSE path sends the JSON body zstd-compressed (level 3) with
// content-encoding: zstd, and the body decompresses to the exact JSON.
func TestCXM3OpenAICodexSSERequestBodyIsZstdCompressed(t *testing.T) {
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportSSE
	options := &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport}}
	requestContext := ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("compress me"), Timestamp: 1}}}
	var rawBody []byte
	var headers http.Header
	withCodexHTTPClient(t, func(request *http.Request) (*http.Response, error) {
		rawBody, _ = io.ReadAll(request.Body)
		headers = request.Header.Clone()
		return codexHTTPResponse(http.StatusOK, codexSSE(map[string]any{
			"type": "response.done", "response": map[string]any{"id": "compressed", "status": "completed", "output": []any{}},
		})), nil
	})
	message := collectOpenAICodex(t, &model, requestContext, options)
	if message.StopReason != ai.StopReasonStop {
		t.Fatalf("message = %#v", message)
	}
	if headers.Get("Content-Encoding") != "zstd" {
		t.Fatalf("Content-Encoding = %q", headers.Get("Content-Encoding"))
	}
	decoded, err := codexDecodeZstd(rawBody)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := buildOpenAICodexResponsesPayload(&model, requestContext, options)
	if err != nil {
		t.Fatal(err)
	}
	want, err := ai.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, want) {
		t.Fatalf("decompressed body = %s\nwant = %s", decoded, want)
	}
}

// CX-m1: retryable markers use the upstream regex rate.?limit etc, so
// rate_limit_exceeded and friends match regardless of separator or case.
func TestCXm1CodexRetryableErrorMatchesUpstreamRegex(t *testing.T) {
	retryable := []string{"rate_limit_exceeded", "Rate Limit", "ratelimit", "OVERLOADED", "service_unavailable", "Service Unavailable", "upstream connect error", "connection_refused"}
	for _, text := range retryable {
		if !retryableCodexError(http.StatusBadRequest, text) {
			t.Fatalf("%q should be retryable", text)
		}
	}
	if retryableCodexError(http.StatusBadRequest, "invalid request") {
		t.Fatal("plain 400 should not be retryable")
	}
	if retryableCodexError(http.StatusTooManyRequests, "insufficient_quota") {
		t.Fatal("terminal 429 should not be retryable")
	}
}

// CX-m2: numeric error codes must not hard-fail the strict unmarshal, nested
// string codes still drive connection-limit detection, fallback messages use
// parsed JSON spelling, and events without a string type are dropped.
func TestCXm2CodexEventErrorCodeDecoding(t *testing.T) {
	model := codexTestModel()
	emitted := 0
	processor := newOpenAIResponsesProcessor(&model, newAssistantMessage(&model), &OpenAIResponsesOptions{}, func(ai.AssistantMessageEvent) bool {
		emitted++
		return true
	})

	err := handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"error","code":4009,"error":{"code":"websocket_connection_limit_reached","message":"limit reached"}}`))
	var apiFailure *codexAPIError
	if !errors.As(err, &apiFailure) || apiFailure.code != "websocket_connection_limit_reached" || apiFailure.message != "Codex error: limit reached" {
		t.Fatalf("error event = %#v", err)
	}
	if !isCodexConnectionLimitError(err) {
		t.Fatal("numeric top-level code defeated connection-limit detection")
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"error","code":500}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "" || apiFailure.message != `Codex error: {"type":"error","code":500}` {
		t.Fatalf("numeric-only code event = %#v", err)
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"error","value":1e3}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "" || apiFailure.message != `Codex error: {"type":"error","value":1000}` {
		t.Fatalf("parsed numeric fallback event = %#v", err)
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"response.failed","response":{"error":{"code":429,"message":""}}}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "429" || apiFailure.message != "Codex response failed" {
		t.Fatalf("response.failed numeric code = %#v", err)
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"response.failed","response":{"error":{"code":"quota_exceeded","message":"over quota"}}}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "quota_exceeded" || apiFailure.message != "over quota" {
		t.Fatalf("response.failed string code = %#v", err)
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"error","message":42,"error":{"code":[],"message":{"bad":true}}}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "" || apiFailure.message != `Codex error: {"type":"error","message":42,"error":{"code":[],"message":{"bad":true}}}` {
		t.Fatalf("malformed error fields = %#v", err)
	}

	err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":"response.failed","response":{"error":"not-an-object"}}`))
	if !errors.As(err, &apiFailure) || apiFailure.code != "" || apiFailure.message != "Codex response failed" {
		t.Fatalf("malformed response.failed fields = %#v", err)
	}

	emittedBefore := emitted
	if err = handleOpenAICodexEvent(processor, json.RawMessage(`{"type":7,"response":{"status":"failed"}}`)); err != nil {
		t.Fatalf("non-string event type = %v, want dropped event", err)
	}
	if emitted != emittedBefore {
		t.Fatalf("non-string event type emitted %d events, want 0", emitted-emittedBefore)
	}
}

// CX-m3: User-Agent platform/arch tokens follow Node os naming (win32, ia32).
func TestCXm3CodexUserAgentUsesNodeOsNaming(t *testing.T) {
	platforms := map[string]string{
		"windows": "win32",
		"solaris": "sunos",
		"illumos": "sunos",
		"linux":   "linux",
		"darwin":  "darwin",
	}
	for goos, want := range platforms {
		if got := codexNodePlatform(goos); got != want {
			t.Fatalf("platform mapping %s = %q, want %q", goos, got, want)
		}
	}
	architectures := map[string]string{
		"386":     "ia32",
		"amd64":   "x64",
		"mipsle":  "mipsel",
		"ppc64le": "ppc64",
		"arm64":   "arm64",
	}
	for goarch, want := range architectures {
		if got := codexNodeArchitecture(goarch); got != want {
			t.Fatalf("architecture mapping %s = %q, want %q", goarch, got, want)
		}
	}
}

func codexDecodeZstd(contents []byte) ([]byte, error) {
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	return decoder.DecodeAll(contents, nil)
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
