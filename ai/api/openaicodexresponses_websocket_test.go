package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type codexWebSocketStep struct {
	message []byte
	err     error
}

type fakeCodexWebSocket struct {
	mu     sync.Mutex
	steps  []codexWebSocketStep
	writes [][]byte
	closed bool
}

func TestF2OpenAICodexWebSocketTrace(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name                string                      `json:"name"`
			Model               ai.Model                    `json:"model"`
			Context             ai.Context                  `json:"context"`
			Options             OpenAICodexResponsesOptions `json:"options"`
			ServerEvents        []json.RawMessage           `json:"serverEvents"`
			ExpectedConnections int                         `json:"expectedConnections"`
			ExpectedRequests    []struct {
				URL     string            `json:"url"`
				Headers map[string]string `json:"headers"`
				Body    string            `json:"body"`
			} `json:"expectedRequests"`
			ExpectedEvents []json.RawMessage `json:"expectedEvents"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F2", "codex-websocket.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			steps := make([]codexWebSocketStep, len(fixtureCase.ServerEvents))
			for index, event := range fixtureCase.ServerEvents {
				steps[index].message = append([]byte(nil), event...)
			}
			socket := &fakeCodexWebSocket{steps: steps}
			connections := 0
			var endpoint string
			var headers http.Header
			withOpenAICodexWebSocketConnector(t, func(_ context.Context, gotEndpoint string, gotHeaders http.Header, _ time.Duration) (openAICodexSocket, error) {
				connections++
				endpoint, headers = gotEndpoint, gotHeaders.Clone()
				return socket, nil
			})
			withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
				return nil, errors.New("upstream websocket fixture unexpectedly used SSE")
			})
			previousNow := openAINowUnixMilli
			openAINowUnixMilli = func() int64 { return f2FixedTimestamp }
			t.Cleanup(func() { openAINowUnixMilli = previousNow })
			stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &fixtureCase.Model, fixtureCase.Context, &fixtureCase.Options)
			if err != nil {
				t.Fatal(err)
			}
			actualEvents := make([]json.RawMessage, 0)
			for event, streamErr := range stream {
				if streamErr != nil {
					t.Fatal(streamErr)
				}
				encoded, err := ai.MarshalAssistantMessageEvent(event)
				if err != nil {
					t.Fatal(err)
				}
				actualEvents = append(actualEvents, encoded)
			}
			if connections != fixtureCase.ExpectedConnections || len(fixtureCase.ExpectedRequests) != 1 {
				t.Fatalf("connections=%d, requests=%d", connections, len(fixtureCase.ExpectedRequests))
			}
			request := fixtureCase.ExpectedRequests[0]
			writes := socket.capturedWrites()
			if endpoint != request.URL || len(writes) != 1 || string(writes[0]) != request.Body {
				t.Fatalf("request endpoint=%q body=%s, want endpoint=%q body=%s", endpoint, firstCodexWrite(writes), request.URL, request.Body)
			}
			if diff := diffStringMap(request.Headers, selectedCodexWebSocketHeaders(headers)); diff != "" {
				t.Fatal(diff)
			}
			if len(actualEvents) != len(fixtureCase.ExpectedEvents) {
				t.Fatalf("events=%d, want %d", len(actualEvents), len(fixtureCase.ExpectedEvents))
			}
			for index := range actualEvents {
				want := compactF2Event(t, fixtureCase.ExpectedEvents[index])
				if diff := runner.ByteDiff(want, actualEvents[index]); diff != "" {
					t.Fatalf("event %d mismatch:\n%s\nwant: %s\n got: %s", index, diff, want, actualEvents[index])
				}
			}
		})
	}
}

func firstCodexWrite(writes [][]byte) []byte {
	if len(writes) == 0 {
		return nil
	}
	return writes[0]
}

func selectedCodexWebSocketHeaders(headers http.Header) map[string]string {
	selected := make(map[string]string)
	for _, name := range []string{
		"authorization", "chatgpt-account-id", "openai-beta", "originator",
		"session-id", "x-client-request-id", "x-fixture", "x-model-header",
	} {
		if value := headers.Get(name); value != "" {
			selected[name] = value
		}
	}
	return selected
}

func (socket *fakeCodexWebSocket) WriteText(contents []byte) error {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	if socket.closed {
		return errors.New("closed")
	}
	socket.writes = append(socket.writes, append([]byte(nil), contents...))
	return nil
}

func (socket *fakeCodexWebSocket) ReadMessage(context.Context, time.Duration) ([]byte, error) {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	if len(socket.steps) == 0 {
		return nil, io.EOF
	}
	step := socket.steps[0]
	socket.steps = socket.steps[1:]
	return append([]byte(nil), step.message...), step.err
}

func (socket *fakeCodexWebSocket) Close(uint16, string) error {
	socket.mu.Lock()
	socket.closed = true
	socket.mu.Unlock()
	return nil
}

func (socket *fakeCodexWebSocket) IsOpen() bool {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	return !socket.closed
}

func (socket *fakeCodexWebSocket) capturedWrites() [][]byte {
	socket.mu.Lock()
	defer socket.mu.Unlock()
	result := make([][]byte, len(socket.writes))
	for index, contents := range socket.writes {
		result[index] = append([]byte(nil), contents...)
	}
	return result
}

func TestOpenAICodexAutoUsesWebSocket(t *testing.T) {
	sessionID := "ws-success"
	socket := &fakeCodexWebSocket{steps: []codexWebSocketStep{{message: codexWebSocketJSON(map[string]any{
		"type": "response.done", "response": map[string]any{"id": "ws-response", "status": "completed", "output": []any{}},
	})}}}
	connectCalls, httpCalls := 0, 0
	var endpoint string
	var headers http.Header
	withOpenAICodexWebSocketConnector(t, func(_ context.Context, gotEndpoint string, gotHeaders http.Header, _ time.Duration) (openAICodexSocket, error) {
		connectCalls++
		endpoint, headers = gotEndpoint, gotHeaders.Clone()
		return socket, nil
	})
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("unexpected SSE request")
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportAuto
	message := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, SessionID: &sessionID},
	})
	if message.StopReason != ai.StopReasonStop || message.ResponseID == nil || *message.ResponseID != "ws-response" {
		t.Fatalf("message = %#v", message)
	}
	if connectCalls != 1 || httpCalls != 0 || endpoint != "wss://codex.fixture.invalid/backend-api/codex/responses" {
		t.Fatalf("connect=%d http=%d endpoint=%q", connectCalls, httpCalls, endpoint)
	}
	if headers.Get("OpenAI-Beta") != openAICodexWebSocketBeta || headers.Get("session-id") != sessionID || headers.Get("x-client-request-id") != sessionID || headers.Get("Accept") != "" || headers.Get("Content-Type") != "" {
		t.Fatalf("WebSocket headers = %#v", headers)
	}
	writes := socket.capturedWrites()
	if len(writes) != 1 {
		t.Fatalf("writes = %d", len(writes))
	}
	var request map[string]json.RawMessage
	if err := json.Unmarshal(writes[0], &request); err != nil {
		t.Fatal(err)
	}
	if string(request["type"]) != `"response.create"` || string(request["store"]) != "false" {
		t.Fatalf("WebSocket request = %s", writes[0])
	}
	stats := GetOpenAICodexWebSocketDebugStats(sessionID)
	if stats == nil || stats.Requests != 1 || stats.ConnectionsCreated != 1 || stats.CachedContextRequests != 1 || stats.FullContextRequests != 1 || stats.WebSocketFailures != 0 || stats.SSEFallbacks != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestOpenAICodexWebSocketFailureFallsBackAndSticks(t *testing.T) {
	sessionID := "ws-fallback"
	connectCalls, httpCalls := 0, 0
	withOpenAICodexWebSocketConnector(t, func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error) {
		connectCalls++
		return nil, errors.New("dial failed")
	})
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		httpCalls++
		return codexHTTPResponse(http.StatusOK, codexSSE(map[string]any{
			"type": "response.done", "response": map[string]any{"id": "sse-response", "status": "completed", "output": []any{}},
		})), nil
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportAuto
	options := &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, SessionID: &sessionID}}
	first := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, options)
	second := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, options)
	if first.StopReason != ai.StopReasonStop || first.Diagnostics == nil || len(*first.Diagnostics) != 1 || (*first.Diagnostics)[0].Error == nil || (*first.Diagnostics)[0].Error.Message != "dial failed" {
		t.Fatalf("first fallback result = %#v", first)
	}
	if second.StopReason != ai.StopReasonStop || second.Diagnostics != nil {
		t.Fatalf("sticky fallback result = %#v", second)
	}
	if connectCalls != 1 || httpCalls != 2 {
		t.Fatalf("connect=%d http=%d", connectCalls, httpCalls)
	}
	stats := GetOpenAICodexWebSocketDebugStats(sessionID)
	if stats == nil || stats.WebSocketFailures != 1 || stats.SSEFallbacks != 2 || !stats.WebSocketFallbackActive {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestOpenAICodexRetriesWebSocketConnectionLimitOnce(t *testing.T) {
	first := &fakeCodexWebSocket{steps: []codexWebSocketStep{{message: codexWebSocketJSON(map[string]any{
		"type": "error", "error": map[string]any{"code": "websocket_connection_limit_reached"},
	})}}}
	second := &fakeCodexWebSocket{steps: []codexWebSocketStep{{message: codexWebSocketJSON(map[string]any{
		"type": "response.done", "response": map[string]any{"id": "retried", "status": "completed", "output": []any{}},
	})}}}
	connectCalls, httpCalls := 0, 0
	withOpenAICodexWebSocketConnector(t, func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error) {
		connectCalls++
		if connectCalls == 1 {
			return first, nil
		}
		return second, nil
	})
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("unexpected SSE request")
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportWebSocket
	message := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport},
	})
	if message.StopReason != ai.StopReasonStop || message.ResponseID == nil || *message.ResponseID != "retried" || connectCalls != 2 || httpCalls != 0 || message.Diagnostics != nil {
		t.Fatalf("retry result = %#v, connect=%d http=%d", message, connectCalls, httpCalls)
	}
}

func TestOpenAICodexWebSocketFailureAfterStartDoesNotFallback(t *testing.T) {
	sessionID := "ws-after-start"
	socket := &fakeCodexWebSocket{steps: []codexWebSocketStep{
		{message: codexWebSocketJSON(map[string]any{"type": "response.created", "response": map[string]any{"id": "partial"}})},
		{err: io.ErrUnexpectedEOF},
	}}
	httpCalls := 0
	withOpenAICodexWebSocketConnector(t, func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error) {
		return socket, nil
	})
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		httpCalls++
		return nil, errors.New("unexpected SSE request")
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportAuto
	message := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, SessionID: &sessionID},
	})
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != io.ErrUnexpectedEOF.Error() || httpCalls != 0 || message.Diagnostics == nil {
		t.Fatalf("after-start result = %#v, http=%d", message, httpCalls)
	}
	diagnostic := (*message.Diagnostics)[0]
	if string(diagnostic.Details) == "" || !bytesContain(diagnostic.Details, `"eventsEmitted":true`) || bytesContain(diagnostic.Details, `"fallbackTransport"`) {
		t.Fatalf("diagnostic = %s", diagnostic.Details)
	}
}

func TestOpenAICodexWebSocketCachedSendsOnlyInputDelta(t *testing.T) {
	sessionID := "ws-cached"
	steps := codexWebSocketTextResponse("first-response", "first answer")
	steps = append(steps, codexWebSocketStep{message: codexWebSocketJSON(map[string]any{
		"type": "response.done", "response": map[string]any{"id": "second-response", "status": "completed", "output": []any{}},
	})})
	socket := &fakeCodexWebSocket{steps: steps}
	connectCalls := 0
	withOpenAICodexWebSocketConnector(t, func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error) {
		connectCalls++
		return socket, nil
	})
	withCodexHTTPClient(t, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("unexpected SSE request")
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportWebSocketCached
	options := &OpenAICodexResponsesOptions{StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, SessionID: &sessionID}}
	firstUser := &ai.UserMessage{Content: ai.NewUserText("first"), Timestamp: 1}
	first := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{firstUser}}, options)
	if len(first.Content) == 0 {
		t.Fatalf("first response = %#v", first)
	}
	secondUser := &ai.UserMessage{Content: ai.NewUserText("second"), Timestamp: 2}
	second := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{firstUser, first, secondUser}}, options)
	if second.StopReason != ai.StopReasonStop || connectCalls != 1 {
		t.Fatalf("second response = %#v, connects=%d", second, connectCalls)
	}
	writes := socket.capturedWrites()
	if len(writes) != 2 {
		t.Fatalf("writes = %d", len(writes))
	}
	var request struct {
		PreviousResponseID string            `json:"previous_response_id"`
		Input              []json.RawMessage `json:"input"`
		Store              bool              `json:"store"`
	}
	if err := json.Unmarshal(writes[1], &request); err != nil {
		t.Fatal(err)
	}
	if request.PreviousResponseID != "first-response" || len(request.Input) != 1 || request.Store {
		t.Fatalf("cached request = %s", writes[1])
	}
	if !bytesContain(request.Input[0], `"second"`) {
		t.Fatalf("delta input = %s", request.Input[0])
	}
	stats := GetOpenAICodexWebSocketDebugStats(sessionID)
	if stats == nil || stats.ConnectionsCreated != 1 || stats.ConnectionsReused != 1 || stats.DeltaRequests != 1 || stats.LastDeltaInputItems == nil || *stats.LastDeltaInputItems != 1 || stats.LastPreviousResponseID == nil || *stats.LastPreviousResponseID != "first-response" {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestOpenAICodexCachedWebSocketClosesWithSessionResources(t *testing.T) {
	sessionID := "ws-dispose"
	socket := &fakeCodexWebSocket{steps: []codexWebSocketStep{{message: codexWebSocketJSON(map[string]any{
		"type": "response.done", "response": map[string]any{"id": "disposed", "status": "completed", "output": []any{}},
	})}}}
	withOpenAICodexWebSocketConnector(t, func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error) {
		return socket, nil
	})
	model := codexTestModel()
	token := codexAPITestToken(t, "account")
	transport := ai.TransportWebSocketCached
	message := collectOpenAICodex(t, &model, ai.Context{Messages: ai.MessageList{}}, &OpenAICodexResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &token, Transport: &transport, SessionID: &sessionID},
	})
	if message.StopReason != ai.StopReasonStop || !socket.IsOpen() {
		t.Fatalf("cached socket before cleanup: message=%#v open=%v", message, socket.IsOpen())
	}
	if err := ai.CleanupSessionResources(sessionID); err != nil {
		t.Fatal(err)
	}
	if socket.IsOpen() {
		t.Fatal("cached Codex WebSocket remained open after session disposal")
	}
}

func codexWebSocketTextResponse(responseID, text string) []codexWebSocketStep {
	item := map[string]any{
		"type": "message", "id": "message-" + responseID, "role": "assistant", "status": "completed",
		"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
	}
	return []codexWebSocketStep{
		{message: codexWebSocketJSON(map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "message", "id": "message-" + responseID, "role": "assistant", "status": "in_progress", "content": []any{}}})},
		{message: codexWebSocketJSON(map[string]any{"type": "response.output_text.delta", "output_index": 0, "delta": text})},
		{message: codexWebSocketJSON(map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})},
		{message: codexWebSocketJSON(map[string]any{"type": "response.done", "response": map[string]any{"id": responseID, "status": "completed", "output": []any{item}}})},
	}
}

func collectOpenAICodex(t *testing.T, model *ai.Model, request ai.Context, options *OpenAICodexResponsesOptions) *ai.AssistantMessage {
	t.Helper()
	stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), model, request, options)
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	return message
}

func codexWebSocketJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func bytesContain(contents []byte, text string) bool {
	return stringContains(string(contents), text)
}

func stringContains(contents, text string) bool {
	for index := 0; index+len(text) <= len(contents); index++ {
		if contents[index:index+len(text)] == text {
			return true
		}
	}
	return false
}

func withOpenAICodexWebSocketConnector(
	t *testing.T,
	connect func(context.Context, string, http.Header, time.Duration) (openAICodexSocket, error),
) {
	t.Helper()
	CloseOpenAICodexWebSocketSessions()
	ResetOpenAICodexWebSocketDebugStats()
	previous := openAICodexConnectWebSocket
	openAICodexConnectWebSocket = connect
	t.Cleanup(func() {
		CloseOpenAICodexWebSocketSessions()
		ResetOpenAICodexWebSocketDebugStats()
		openAICodexConnectWebSocket = previous
	})
}
