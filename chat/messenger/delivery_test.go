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

	"github.com/OrdalieTech/pigo/chat"
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

func TestTypingSequenceMarkSeenThenTypingOn(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, func(int) (int, string, http.Header) {
		return http.StatusOK, `{"recipient_id":"PSID1"}`, nil
	})
	adapter := newTestAdapter(t, server.URL, nil)
	adapter.typingInterval = time.Hour // freeze the refresher for this test
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}

	requests := graph.recorded()
	if len(requests) != 3 {
		t.Fatalf("got %d requests, want mark_seen + typing_on + typing_on", len(requests))
	}
	wantActions := []string{"mark_seen", "typing_on", "typing_on"}
	for i, req := range requests {
		if req.Method != http.MethodPost || req.Path != "/v23.0/me/messages" {
			t.Fatalf("request %d = %s %s", i, req.Method, req.Path)
		}
		if got := req.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Fatalf("Authorization = %q", got)
		}
		payload := decodePayload(t, req.Body)
		if payload.SenderAction != wantActions[i] {
			t.Errorf("request %d sender_action = %q, want %q", i, payload.SenderAction, wantActions[i])
		}
		if payload.Message != nil {
			t.Errorf("request %d combines a sender_action with a message — the Send API rejects that", i)
		}
		if payload.Recipient.ID != "PSID1" {
			t.Errorf("request %d recipient = %q", i, payload.Recipient.ID)
		}
	}
}

func countActions(requests []capturedRequest, action string) int {
	n := 0
	for _, req := range requests {
		var payload sentPayload
		if json.Unmarshal(req.Body, &payload) == nil && payload.SenderAction == action {
			n++
		}
	}
	return n
}

func TestTypingRefreshLoopUntilFinalize(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil) // 10ms typing interval
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The refresher must re-fire typing_on beyond the initial call.
	deadline := time.Now().Add(2 * time.Second)
	for countActions(graph.recorded(), "typing_on") < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("typing refresher never re-fired: %d typing_on calls", countActions(graph.recorded(), "typing_on"))
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := delivery.Finalize(context.Background(), "done"); err != nil {
		t.Fatal(err)
	}
	// Finalize stops the refresher: after a settling pause the typing_on
	// count must not grow anymore.
	time.Sleep(30 * time.Millisecond)
	settled := countActions(graph.recorded(), "typing_on")
	time.Sleep(50 * time.Millisecond)
	if after := countActions(graph.recorded(), "typing_on"); after != settled {
		t.Fatalf("typing_on kept firing after Finalize: %d -> %d", settled, after)
	}
}

func TestTypingLoopStopsOnThrottle(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, func(int) (int, string, http.Header) {
		return http.StatusBadRequest, graphErrorBody(613, 0, "rate limit"), nil
	})
	adapter := newTestAdapter(t, server.URL, nil)
	adapter.typingInterval = 5 * time.Millisecond
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	if err := delivery.Typing(context.Background()); err == nil {
		t.Fatal("initial typing_on against a throttling server did not surface an error")
	}
	// The refresh loop must stop on its first throttled tick instead of
	// burning quota: the request count settles.
	time.Sleep(40 * time.Millisecond)
	settled := len(graph.recorded())
	time.Sleep(40 * time.Millisecond)
	if after := len(graph.recorded()); after != settled {
		t.Fatalf("typing loop kept firing while throttled: %d -> %d requests", settled, after)
	}
}

func TestPreviewIsNoop(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

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

func TestFinalizeEmptyTextSendsPlaceholder(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	receipt, err := delivery.Finalize(context.Background(), "   ")
	if err != nil {
		t.Fatal(err)
	}
	requests := graph.recorded()
	if len(requests) != 1 || len(receipt.MessageIDs) != 1 {
		t.Fatalf("got %d sends and receipt %+v, want one placeholder send", len(requests), receipt)
	}
	payload := decodePayload(t, requests[0].Body)
	if payload.Message == nil || payload.Message.Text != "(empty reply)" {
		t.Fatalf("message = %+v, want the empty-reply placeholder", payload.Message)
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

func TestSendErrorMapping(t *testing.T) {
	cases := []struct {
		code, subcode int
		wantRequests  int  // with maxAttempts=3
		recovers      bool // server succeeds on the final attempt
	}{
		{10, 2018278, 1, false},  // outside 24h window: surface, never retry
		{10, 2018065, 1, false},  // user opted out: surface, never retry
		{551, 1545041, 1, false}, // person unavailable: conversation permanently dead
		{190, 0, 1, false},       // bad page token: fatal, never retry
		{100, 2018001, 1, false}, // no matching user (foreign PSID): drop
		{200, 0, 1, false},       // missing permission: config error
		{613, 0, 3, true},        // API rate limit: bounded backoff retry
		{4, 0, 3, true},          // app-level throttle: bounded backoff retry
		{32, 0, 3, true},         // page-level throttle: bounded backoff retry
		{80006, 0, 3, true},      // messenger BUC throttle: bounded backoff retry
		{1200, 0, 3, true},       // temporary send failure: retried
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("code %d subcode %d", tc.code, tc.subcode), func(t *testing.T) {
			graph := &fakeGraph{}
			server := newSendServer(t, graph, func(seq int) (int, string, http.Header) {
				if tc.recovers && seq == tc.wantRequests {
					return okSend()(seq)
				}
				return http.StatusBadRequest, graphErrorBody(tc.code, tc.subcode, "boom"), nil
			})
			adapter := newTestAdapter(t, server.URL, nil)
			delivery := adapter.NewDelivery(testKey(), "m_IN", "")

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
				if !errors.As(err, &graphErr) || graphErr.Code != tc.code || graphErr.Subcode != tc.subcode {
					t.Fatalf("error %v does not expose graph code %d/%d", err, tc.code, tc.subcode)
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
	server := newSendServer(t, graph, func(int) (int, string, http.Header) {
		return http.StatusTooManyRequests, graphErrorBody(613, 0, "slow down"), nil
	})
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	_, err := delivery.Finalize(context.Background(), "hello")
	var graphErr *GraphError
	if !errors.As(err, &graphErr) || graphErr.Code != 613 {
		t.Fatalf("err = %v, want graph error 613 after retries", err)
	}
	if got := len(graph.recorded()); got != adapter.maxAttempts {
		t.Fatalf("made %d requests, want maxAttempts=%d", got, adapter.maxAttempts)
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

func TestNotifySendsUpdateMessagingType(t *testing.T) {
	graph := &fakeGraph{}
	server := newSendServer(t, graph, okSend())
	adapter := newTestAdapter(t, server.URL, nil)
	delivery := adapter.NewDelivery(testKey(), "m_IN", "")

	if err := delivery.Notify(context.Background(), "session stopped"); err != nil {
		t.Fatal(err)
	}
	requests := graph.recorded()
	if len(requests) != 1 {
		t.Fatalf("got %d sends", len(requests))
	}
	payload := decodePayload(t, requests[0].Body)
	if payload.MessagingType != "UPDATE" {
		t.Fatalf("messaging_type = %q, want UPDATE for proactive notices", payload.MessagingType)
	}
	if payload.Message == nil || payload.Message.Text != "session stopped" {
		t.Fatalf("message = %+v", payload.Message)
	}
}
