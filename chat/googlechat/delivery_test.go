package googlechat

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/chat"
)

// testKey is the conversation key used across the delivery tests.
var testKey = chat.ConversationKey{
	Platform: "googlechat",
	Account:  testProjectNumber,
	ChatID:   "spaces/AAAA",
	ThreadID: "spaces/AAAA/threads/DDDD",
}

const testReplyTo = "spaces/AAAA/messages/BBBB.CCCC"

// writeCalls filters the recorded calls down to message writes.
func writeCalls(calls []apiCall) []apiCall {
	var out []apiCall
	for _, call := range calls {
		if call.Method == http.MethodPost || call.Method == http.MethodPatch {
			out = append(out, call)
		}
	}
	return out
}

func TestFinalizeCreateConflictDegradesToEdit(t *testing.T) {
	env := newTestEnv(t)
	wantName := "spaces/AAAA/messages/" + turnMessageID(testReplyTo, testKey)
	env.chatAPI.setMessage(wantName, "stale from crashed twin")
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	receipt, err := d.Finalize(context.Background(), "fresh")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := env.chatAPI.text(wantName); got != "fresh" {
		t.Fatalf("stored text %q, want the 409 to degrade into an edit", got)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != wantName {
		t.Fatalf("receipt = %v, want [%q]", receipt.MessageIDs, wantName)
	}
	writes := writeCalls(env.chatAPI.callLog())
	if len(writes) != 2 || writes[0].Method != http.MethodPost || writes[1].Method != http.MethodPatch {
		t.Fatalf("writes = %+v, want create conflict then edit", writes)
	}
}

func TestFinalizeChunksOverflow(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	paragraph := strings.Repeat("word ", 700) // ~3500 chars per paragraph
	text := paragraph + "\n\n" + paragraph + "\n\n" + paragraph
	receipt, err := d.Finalize(context.Background(), text)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 3 {
		t.Fatalf("receipt has %d ids, want 3 chunks", len(receipt.MessageIDs))
	}
	clientID := turnMessageID(testReplyTo, testKey)
	wantIDs := []string{
		"spaces/AAAA/messages/" + clientID,
		"spaces/AAAA/messages/" + clientID + "-1",
		"spaces/AAAA/messages/" + clientID + "-2",
	}
	for i, want := range wantIDs {
		if receipt.MessageIDs[i] != want {
			t.Fatalf("receipt[%d] = %q, want %q", i, receipt.MessageIDs[i], want)
		}
		if env.chatAPI.text(want) == "" {
			t.Fatalf("chunk %d not stored under %q", i, want)
		}
		if n := len([]rune(env.chatAPI.text(want))); n > maxMessageLen {
			t.Fatalf("chunk %d is %d chars, over the %d hard limit", i, n, maxMessageLen)
		}
	}
	writes := writeCalls(env.chatAPI.callLog())
	if len(writes) != 3 {
		t.Fatalf("got %d writes, want one create per final chunk", len(writes))
	}
	for _, write := range writes {
		if write.Method != http.MethodPost {
			t.Fatalf("final chunk used %s, want POST", write.Method)
		}
		if write.Body.Thread == nil || write.Body.Thread.Name != testKey.ThreadID {
			t.Fatalf("final chunk lost the thread: %+v", write.Body.Thread)
		}
	}
}

func TestFinalizeRetryResumesAtFailedChunk(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.maxAttempts = 1 // surface the scripted error immediately
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	paragraph := strings.Repeat("word ", 700)
	text := paragraph + "\n\n" + paragraph + "\n\n" + paragraph

	// Chunk 1 lands (pass-through entry), chunk 2's create fails.
	env.chatAPI.pushScript(scripted{}, scripted{status: http.StatusServiceUnavailable, grpcStatus: "UNAVAILABLE"})
	if _, err := d.Finalize(context.Background(), text); err == nil {
		t.Fatal("Finalize succeeded, want the scripted failure")
	}
	failed := len(writeCalls(env.chatAPI.callLog()))

	receipt, err := d.Finalize(context.Background(), text)
	if err != nil {
		t.Fatalf("Finalize retry: %v", err)
	}
	if len(receipt.MessageIDs) != 3 {
		t.Fatalf("receipt has %d ids, want 3", len(receipt.MessageIDs))
	}
	writes := writeCalls(env.chatAPI.callLog())
	if got := len(writes) - failed; got != 2 {
		t.Fatalf("retry made %d writes, want 2 (chunk 1 must not be re-sent)", got)
	}
}

func TestBackoffOn429HonorsRetryAfterAndCap(t *testing.T) {
	env := newTestEnv(t)
	env.chatAPI.pushScript(
		scripted{status: http.StatusTooManyRequests, grpcStatus: "RESOURCE_EXHAUSTED", retryAfter: "3"},
		scripted{status: http.StatusTooManyRequests, grpcStatus: "RESOURCE_EXHAUSTED"},
		scripted{status: http.StatusInternalServerError, grpcStatus: "INTERNAL"},
	)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	if err := d.Notify(context.Background(), "text"); err != nil {
		t.Fatalf("Notify after retries: %v", err)
	}
	delays := *env.delays
	if len(delays) != 3 {
		t.Fatalf("slept %d times, want 3", len(delays))
	}
	if delays[0] != 3*time.Second {
		t.Fatalf("first delay %v, want the server's Retry-After of 3s", delays[0])
	}
	if delays[1] != defaultBackoff(1) || delays[2] != defaultBackoff(2) {
		t.Fatalf("exponential delays %v/%v, want %v/%v", delays[1], delays[2], defaultBackoff(1), defaultBackoff(2))
	}
	if writes := writeCalls(env.chatAPI.callLog()); len(writes) != 4 {
		t.Fatalf("made %d calls, want 3 failures + 1 success", len(writes))
	}
}

func TestUnauthorizedMintsFreshTokenOnce(t *testing.T) {
	env := newTestEnv(t)
	env.chatAPI.pushScript(scripted{status: http.StatusUnauthorized, grpcStatus: "UNAUTHENTICATED"})
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	if err := d.Notify(context.Background(), "text"); err != nil {
		t.Fatalf("Notify after token refresh: %v", err)
	}
	if got := env.token.mintCount(); got != 2 {
		t.Fatalf("minted %d tokens, want 2 (401 forces one refresh)", got)
	}
	writes := writeCalls(env.chatAPI.callLog())
	if len(writes) != 2 {
		t.Fatalf("made %d calls, want 2", len(writes))
	}
	if writes[0].Bearer == writes[1].Bearer {
		t.Fatal("retry reused the rejected token")
	}
	// Errors from a persistent 401 must not leak the bearer token.
	env.chatAPI.pushScript(
		scripted{status: http.StatusUnauthorized, grpcStatus: "UNAUTHENTICATED"},
		scripted{status: http.StatusUnauthorized, grpcStatus: "UNAUTHENTICATED"},
	)
	err := d.Notify(context.Background(), "more text")
	if err == nil {
		t.Fatal("want the persistent 401 surfaced")
	}
	if strings.Contains(err.Error(), "tok-") {
		t.Fatalf("error leaks the bearer token: %v", err)
	}
}

func TestDownloadUploadedContent(t *testing.T) {
	env := newTestEnv(t)
	env.chatAPI.setMedia("uploaded/resource/0", []byte("pdf-bytes"))
	body, mime, err := env.adapter.Download(context.Background(), chat.AttachmentRef{
		Kind: "document",
		ID:   "uploaded/resource/0",
		MIME: "application/pdf",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = body.Close() }()
	content, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(content) != "pdf-bytes" || mime != "application/pdf" {
		t.Fatalf("got %q / %q", content, mime)
	}
	calls := env.chatAPI.callLog()
	last := calls[len(calls)-1]
	if last.Path != "/v1/media/uploaded/resource/0" || last.Query.Get("alt") != "media" {
		t.Fatalf("download hit %s?%s", last.Path, last.Query.Encode())
	}
	if last.Bearer == "" {
		t.Fatal("media download was unauthenticated")
	}
	if _, _, err := env.adapter.Download(context.Background(), chat.AttachmentRef{}); err == nil {
		t.Fatal("Download with no resource name must fail")
	}
}
