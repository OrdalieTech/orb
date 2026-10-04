package whatsapp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/chat"
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
