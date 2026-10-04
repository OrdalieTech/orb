package messenger

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/chat"
)

// newSendServer runs a fake Graph /me/messages endpoint. respond is called
// per request (1-based sequence) and returns the HTTP status, response body,
// and optional response headers.
func newSendServer(t *testing.T, graph *fakeGraph, respond func(seq int) (int, string, http.Header)) *httptest.Server {
	t.Helper()
	var seq atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		graph.record(capturedRequest{Method: r.Method, Path: r.URL.Path, Body: body, Header: r.Header.Clone()})
		status, response, headers := respond(int(seq.Add(1)))
		for key, values := range headers {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(server.Close)
	return server
}

func okSend() func(int) (int, string, http.Header) {
	return func(seq int) (int, string, http.Header) {
		return http.StatusOK, fmt.Sprintf(`{"recipient_id":"PSID1","message_id":"m_OUT.%d"}`, seq), nil
	}
}

func graphErrorBody(code, subcode int, message string) string {
	body := fmt.Sprintf(`{"error":{"message":%q,"type":"OAuthException","code":%d,"fbtrace_id":"tr"`, message, code)
	if subcode != 0 {
		body += fmt.Sprintf(`,"error_subcode":%d`, subcode)
	}
	return body + "}}"
}

func testKey() chat.ConversationKey {
	return chat.ConversationKey{Platform: "messenger", Account: "1906385232743851", ChatID: "PSID1"}
}

// sentPayload decodes the request bodies the adapter sends.
type sentPayload struct {
	Recipient struct {
		ID string `json:"id"`
	} `json:"recipient"`
	MessagingType string `json:"messaging_type"`
	SenderAction  string `json:"sender_action"`
	Message       *struct {
		Text string `json:"text"`
	} `json:"message"`
}

func decodePayload(t *testing.T, body []byte) sentPayload {
	t.Helper()
	var payload sentPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	return payload
}

func TestFinalizePayloadAndReceipt(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	receipt, err := delivery.Finalize(context.Background(), "the answer is 42")
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "m_OUT.1" {
		t.Fatalf("receipt = %+v, want the message_id from the send response", receipt)
	}
	if receipt.At.IsZero() {
		t.Fatal("receipt has zero timestamp")
	}

	requests := graph.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d sends", len(requests))
	}
	req := requests[0]
	if strings.Contains(req.Path, "access_token") {
		t.Fatal("token leaked into the request path")
	}
	payload := decodePayload(t, req.Body)
	if payload.Recipient.ID != "PSID1" {
		t.Fatalf("recipient = %q", payload.Recipient.ID)
	}
	if payload.MessagingType != "RESPONSE" {
		t.Fatalf("messaging_type = %q, want RESPONSE", payload.MessagingType)
	}
	if payload.Message == nil || payload.Message.Text != "the answer is 42" {
		t.Fatalf("message = %+v", payload.Message)
	}
	if payload.SenderAction != "" {
		t.Fatal("send combines a message with a sender_action")
	}
}

func TestFinalizeChunksLongTextSequentially(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	paragraph := strings.Repeat("all work and no play makes jack a dull boy ", 40) // ~1720 chars
	text := strings.Join([]string{paragraph, paragraph, paragraph, paragraph}, "\n\n")

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
		payload := decodePayload(t, req.Body)
		if payload.Message == nil {
			t.Fatalf("send %d has no message", i)
		}
		if n := len([]rune(payload.Message.Text)); n > chunkLimit {
			t.Errorf("chunk %d is %d runes, over the %d cut", i, n, chunkLimit)
		}
		if payload.MessagingType != "RESPONSE" {
			t.Errorf("chunk %d messaging_type = %q", i, payload.MessagingType)
		}
	}
}

func TestFinalizeRetryResumesFromFailedChunk(t *testing.T) {
	graph := &fakeGraph{}
	// Chunk 1 succeeds; chunk 2 fails with a non-retryable policy error on
	// the first Finalize, then succeeds on the retry.
	server := newSendServer(t, graph, func(seq int) (int, string, http.Header) {
		if seq == 2 {
			return http.StatusBadRequest, graphErrorBody(10, 2018278, "outside allowed window"), nil
		}
		return okSend()(seq)
	})
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	// ~1300 chars per paragraph: two chunks split at the paragraph break.
	text := "first chunk " + strings.Repeat("all work and no play makes jack a dull boy ", 30) +
		"\n\nsecond chunk " + strings.Repeat("all play and no work makes jack a mere toy ", 30)

	if _, err := delivery.Finalize(context.Background(), text); err == nil {
		t.Fatal("first Finalize did not surface the chunk-2 error")
	}
	receipt, err := delivery.Finalize(context.Background(), text)
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.MessageIDs) != 2 {
		t.Fatalf("receipt = %+v, want both chunk ids", receipt)
	}
	// 3 sends total: chunk 1, failed chunk 2, resent chunk 2 — chunk 1 is
	// never duplicated on retry.
	requests := graph.recorded()
	if len(requests) != 3 {
		t.Fatalf("got %d sends, want 3 (no chunk-1 duplicate)", len(requests))
	}
	first := decodePayload(t, requests[0].Body)
	last := decodePayload(t, requests[2].Body)
	if first.Message.Text == last.Message.Text {
		t.Fatal("retry resent chunk 1 instead of resuming at chunk 2")
	}
}

func TestSendRegainHintBeyondCapSurfacesImmediately(t *testing.T) {
	graph := &fakeGraph{}
	usage := http.Header{}
	usage.Set("X-Business-Use-Case-Usage",
		`{"1906385232743851":[{"type":"messenger","call_count":100,"estimated_time_to_regain_access":10}]}`)
	server := newSendServer(t, graph, func(int) (int, string, http.Header) {
		return http.StatusTooManyRequests, graphErrorBody(613, 0, "throttled"), usage
	})
	adapter := newTestAdapter(t, server.URL, nil)
	adapter.maxRetryWait = 10 * time.Millisecond // the 10-minute hint exceeds this
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	_, err := delivery.Finalize(context.Background(), "hello")
	var graphErr *GraphError
	if !errors.As(err, &graphErr) || graphErr.Code != 613 {
		t.Fatalf("err = %v, want graph error 613", err)
	}
	if graphErr.RegainAfter != 10*time.Minute {
		t.Fatalf("RegainAfter = %v, want the 10m header hint", graphErr.RegainAfter)
	}
	// The regain hint exceeds maxRetryWait: no blind backoff retries.
	if got := len(graph.recorded()); got != 1 {
		t.Fatalf("made %d requests, want 1 (hint-driven immediate surface)", got)
	}
}
