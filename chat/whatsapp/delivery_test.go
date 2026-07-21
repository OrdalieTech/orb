package whatsapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/chat"
)

// newSendServer runs a fake Graph /messages endpoint. respond is called per
// request (1-based sequence) and returns the HTTP status and response body.
func newSendServer(t *testing.T, graph *fakeGraph, respond func(seq int) (int, string)) *httptest.Server {
	t.Helper()
	seq := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		graph.record(capturedRequest{Method: r.Method, Path: r.URL.Path, Body: body, Header: r.Header.Clone()})
		seq++
		status, response := respond(seq)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server
}

func okSend(wamid string) func(int) (int, string) {
	return func(seq int) (int, string) {
		return http.StatusOK, fmt.Sprintf(`{"messaging_product":"whatsapp","contacts":[{"input":"16505551234","wa_id":"16505551234"}],"messages":[{"id":"%s.%d"}]}`, wamid, seq)
	}
}

func testKey() chat.ConversationKey {
	return chat.ConversationKey{Platform: "whatsapp", Account: "106540352242922", ChatID: "16505551234"}
}

func TestTypingMarkReadPayload(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, func(int) (int, string) { return http.StatusOK, `{"success":true}` })
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Typing is a single mark-as-read call; repeats are no-ops.
	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := graph.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d requests, want exactly 1", len(requests))
	}
	req := requests[0]
	if req.Method != http.MethodPost || req.Path != "/v23.0/106540352242922/messages" {
		t.Fatalf("call = %s %s", req.Method, req.Path)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("Authorization = %q", got)
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["messaging_product"] != "whatsapp" || payload["status"] != "read" || payload["message_id"] != "wamid.IN1" {
		t.Fatalf("payload = %v", payload)
	}
	indicator, ok := payload["typing_indicator"].(map[string]any)
	if !ok || indicator["type"] != "text" {
		t.Fatalf("typing_indicator = %v", payload["typing_indicator"])
	}
}

func TestPreviewIsNoop(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend("wamid.OUT"))
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	if err := delivery.Preview(context.Background(), "partial text"); err != nil {
		t.Fatal(err)
	}
	if id := delivery.PreviewID(); id != "" {
		t.Fatalf("PreviewID = %q, want empty", id)
	}
	if requests := graph.recorded(); len(requests) != 0 {
		t.Fatalf("preview made %d network calls, want 0", len(requests))
	}
}

func TestFinalizeSendsReplyWithContextAndCapturesWamid(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend("wamid.OUT"))
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	receipt, err := delivery.Finalize(context.Background(), "the **answer** is 42")
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "wamid.OUT.1" {
		t.Fatalf("receipt = %+v, want the wamid from messages[0].id", receipt)
	}
	if receipt.At.IsZero() {
		t.Fatal("receipt has zero timestamp")
	}

	requests := graph.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d sends", len(requests))
	}
	var payload struct {
		To   string `json:"to"`
		Type string `json:"type"`
		Text struct {
			Body       string `json:"body"`
			PreviewURL bool   `json:"preview_url"`
		} `json:"text"`
		Context struct {
			MessageID string `json:"message_id"`
		} `json:"context"`
	}
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.To != "16505551234" || payload.Type != "text" {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Text.Body != "the *answer* is 42" {
		t.Fatalf("body = %q, want WhatsApp markup", payload.Text.Body)
	}
	if payload.Context.MessageID != "wamid.IN1" {
		t.Fatalf("context.message_id = %q, want the inbound wamid", payload.Context.MessageID)
	}
}

func TestFinalizeChunksLongText(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend("wamid.OUT"))
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	paragraph := strings.Repeat("all work and no play makes jack a dull boy ", 40) // ~1720 chars
	text := strings.Join([]string{paragraph, paragraph, paragraph, paragraph, paragraph, paragraph}, "\n\n")

	receipt, err := delivery.Finalize(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	requests := graph.recorded()
	if len(requests) < 3 {
		t.Fatalf("got %d sends, want the text chunked into at least 3", len(requests))
	}
	if len(receipt.MessageIDs) != len(requests) {
		t.Fatalf("receipt has %d ids for %d sends", len(receipt.MessageIDs), len(requests))
	}
	for i, req := range requests {
		var payload struct {
			Text struct {
				Body string `json:"body"`
			} `json:"text"`
			Context *struct {
				MessageID string `json:"message_id"`
			} `json:"context"`
		}
		if err := json.Unmarshal(req.Body, &payload); err != nil {
			t.Fatal(err)
		}
		if n := len([]rune(payload.Text.Body)); n > maxMessageLen {
			t.Errorf("chunk %d is %d chars, over the 4096 limit", i, n)
		}
		if i == 0 {
			if payload.Context == nil || payload.Context.MessageID != "wamid.IN1" {
				t.Errorf("first chunk must carry the reply context, got %+v", payload.Context)
			}
		} else if payload.Context != nil {
			t.Errorf("chunk %d carries a reply context, only the first should", i)
		}
	}
}

func graphErrorBody(code int, message string) string {
	return fmt.Sprintf(`{"error":{"message":%q,"type":"OAuthException","code":%d,"error_data":{"details":"details for %d"},"fbtrace_id":"tr"}}`, message, code, code)
}

func TestSendErrorMapping(t *testing.T) {
	cases := []struct {
		code         int
		wantRequests int  // with maxAttempts=3
		recovers     bool // server succeeds on the final attempt
	}{
		{131047, 1, false}, // 24h window: surface immediately, never retry
		{190, 1, false},    // bad token: surface immediately, never retry
		{130429, 3, true},  // throughput limit: bounded backoff retry
		{131048, 3, true},  // spam/pair limit: bounded backoff retry
		{100, 1, false},    // invalid parameter: fail fast
		{131026, 1, false}, // undeliverable: fail fast
		{131051, 1, false}, // unsupported type: fail fast
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("code %d", tc.code), func(t *testing.T) {
			graph := &fakeGraph{}
			server := newSendServer(t, graph, func(seq int) (int, string) {
				if tc.recovers && seq == tc.wantRequests {
					return okSend("wamid.OUT")(seq)
				}
				return http.StatusBadRequest, graphErrorBody(tc.code, "boom")
			})
			adapter := newTestAdapter(t, server.URL, nil)
			delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

			receipt, err := delivery.Finalize(context.Background(), "hello")
			if tc.recovers {
				if err != nil {
					t.Fatalf("retryable code %d did not recover: %v", tc.code, err)
				}
				if len(receipt.MessageIDs) != 1 {
					t.Fatalf("receipt = %+v", receipt)
				}
			} else {
				if err == nil {
					t.Fatalf("code %d did not surface an error", tc.code)
				}
				var graphErr *GraphError
				if !errors.As(err, &graphErr) || graphErr.Code != tc.code {
					t.Fatalf("error %v does not expose graph code %d", err, tc.code)
				}
			}
			if got := len(graph.recorded()); got != tc.wantRequests {
				t.Fatalf("code %d made %d requests, want %d", tc.code, got, tc.wantRequests)
			}
		})
	}
}

func TestSendRetryExhaustionSurfacesError(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, func(int) (int, string) {
		return http.StatusTooManyRequests, graphErrorBody(130429, "slow down")
	})
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	_, err := delivery.Finalize(context.Background(), "hello")
	var graphErr *GraphError
	if !errors.As(err, &graphErr) || graphErr.Code != 130429 {
		t.Fatalf("err = %v, want graph error 130429 after retries", err)
	}
	if got := len(graph.recorded()); got != adapter.maxAttempts {
		t.Fatalf("made %d requests, want maxAttempts=%d", got, adapter.maxAttempts)
	}
}

func TestNotifySendsPlainTextWithoutContext(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend("wamid.OUT"))
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	if err := delivery.Notify(context.Background(), "session stopped"); err != nil {
		t.Fatal(err)
	}
	requests := graph.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d sends", len(requests))
	}
	var payload map[string]any
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, hasContext := payload["context"]; hasContext {
		t.Fatal("notify must not thread onto the inbound message")
	}
}

func TestFinalizeEmptyTextSendsPlaceholder(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend("wamid.OUT"))
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	receipt, err := delivery.Finalize(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	requests := graph.recorded()
	if len(requests) != 1 || len(receipt.MessageIDs) != 1 {
		t.Fatalf("got %d sends and receipt %+v, want one placeholder send", len(requests), receipt)
	}
	var payload struct {
		Text struct {
			Body string `json:"body"`
		} `json:"text"`
	}
	if err := json.Unmarshal(requests[0].Body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text.Body != "(empty reply)" {
		t.Fatalf("body = %q, want the empty-reply placeholder", payload.Text.Body)
	}
}

func TestFinalizeRetryResumesFromFailedChunk(t *testing.T) {
	graph := &fakeGraph{}
	// Chunk 1 succeeds; chunk 2 fails with a non-retryable error on the
	// first Finalize, then succeeds on the retry.
	server := newSendServer(t, graph, func(seq int) (int, string) {
		if seq == 2 {
			return http.StatusBadRequest, graphErrorBody(131047, "outside 24h window")
		}
		return okSend("wamid.OUT")(seq)
	})
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "wamid.IN1", "")

	text := "first chunk " + strings.Repeat("all work and no play makes jack a dull boy ", 50) +
		"\n\nsecond chunk " + strings.Repeat("all play and no work makes jack a mere toy ", 50)

	if _, err := delivery.Finalize(context.Background(), text); err == nil {
		t.Fatal("first Finalize did not surface the chunk-2 error")
	}
	receipt, err := delivery.Finalize(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.MessageIDs) != 2 {
		t.Fatalf("receipt = %+v, want both chunk wamids", receipt)
	}
	// 3 sends total: chunk 1, failed chunk 2, resent chunk 2 — chunk 1 is
	// never duplicated on retry.
	requests := graph.recorded()
	if len(requests) != 3 {
		t.Fatalf("got %d sends, want 3 (no chunk-1 duplicate)", len(requests))
	}
	var first, last struct {
		Text struct {
			Body string `json:"body"`
		} `json:"text"`
	}
	if err := json.Unmarshal(requests[0].Body, &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(requests[2].Body, &last); err != nil {
		t.Fatal(err)
	}
	if first.Text.Body == last.Text.Body {
		t.Fatal("retry resent chunk 1 instead of resuming at chunk 2")
	}
}
