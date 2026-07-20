package googlechat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
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

func TestTurnMessageIDShape(t *testing.T) {
	id := turnMessageID(testReplyTo, testKey)
	if !strings.HasPrefix(id, "client-") {
		t.Fatalf("id %q must start with client-", id)
	}
	if len(id) > 63 {
		t.Fatalf("id %q is %d chars, max 63", id, len(id))
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			t.Fatalf("id %q contains %q outside [a-z0-9-]", id, r)
		}
	}
	if again := turnMessageID(testReplyTo, testKey); again != id {
		t.Fatalf("id not deterministic: %q vs %q", id, again)
	}
	if fallback := turnMessageID("", testKey); fallback == id || !strings.HasPrefix(fallback, "client-") {
		t.Fatalf("fallback id %q must differ and keep the prefix", fallback)
	}
}

func TestTypingAndPreviewAreNoops(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	if err := d.Typing(context.Background()); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if err := d.Preview(context.Background(), "thinking…"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if id := d.PreviewID(); id != "" {
		t.Fatalf("PreviewID = %q, want empty for final-only delivery", id)
	}
	if calls := env.chatAPI.callLog(); len(calls) != 0 {
		t.Fatalf("Typing/Preview made %d API calls, want 0", len(calls))
	}
}

func TestFinalizePacesOverflowChunksPerSpace(t *testing.T) {
	env := newTestEnv(t)
	current := time.Now()
	env.adapter.now = func() time.Time { return current }
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	paragraph := strings.Repeat("word ", 700)
	if _, err := d.Finalize(context.Background(), paragraph+"\n\n"+paragraph); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	delays := *env.delays
	if len(delays) == 0 || delays[0] < time.Second {
		t.Fatalf("overflow chunk pacing delays = %v, want at least one >=1s delay", delays)
	}
}

func TestSpaceWriteCacheIsBounded(t *testing.T) {
	current := time.Now()
	adapter := &Adapter{now: func() time.Time { return current }, lastSpaceWrite: map[string]time.Time{}}
	for i := 0; i <= maxSpaceWriteCache; i++ {
		if err := adapter.waitSpaceWrite(context.Background(), fmt.Sprintf("spaces/%d", i), time.Second); err != nil {
			t.Fatalf("reserve space %d: %v", i, err)
		}
	}
	if got := len(adapter.lastSpaceWrite); got != maxSpaceWriteCache {
		t.Fatalf("space write cache has %d entries, want %d", got, maxSpaceWriteCache)
	}
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

func TestFinalizeIsIdempotent(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	receipt, err := d.Finalize(context.Background(), "short answer")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	wantName := "spaces/AAAA/messages/" + turnMessageID(testReplyTo, testKey)
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != wantName {
		t.Fatalf("receipt %v, want [%q]", receipt.MessageIDs, wantName)
	}

	// A crash-recovered turn re-finalizes from scratch: the create collides
	// and degrades to an edit — no duplicate message.
	fresh := env.adapter.NewDelivery(testKey, testReplyTo, "")
	receipt2, err := fresh.Finalize(context.Background(), "recovered answer")
	if err != nil {
		t.Fatalf("re-Finalize: %v", err)
	}
	if len(receipt2.MessageIDs) != 1 || receipt2.MessageIDs[0] != wantName {
		t.Fatalf("re-finalize receipt %v, want the same message", receipt2.MessageIDs)
	}
	if got := env.chatAPI.text(wantName); got != "recovered answer" {
		t.Fatalf("stored text %q, want the retry's text", got)
	}
}

func TestFinalizeIgnoresResumePreviewID(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "stale-preview-name")
	receipt, err := d.Finalize(context.Background(), "final text")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	wantName := "spaces/AAAA/messages/" + turnMessageID(testReplyTo, testKey)
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != wantName {
		t.Fatalf("receipt %v, want deterministic final %q", receipt.MessageIDs, wantName)
	}
	writes := writeCalls(env.chatAPI.callLog())
	if len(writes) != 1 || writes[0].Method != http.MethodPost {
		t.Fatalf("writes = %+v, want one final POST", writes)
	}
}

func TestFinalizeEmptyReplyPlaceholder(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	receipt, err := d.Finalize(context.Background(), "  \n ")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt %v", receipt.MessageIDs)
	}
	if got := env.chatAPI.text(receipt.MessageIDs[0]); got != "(empty reply)" {
		t.Fatalf("stored text %q, want the placeholder", got)
	}
}

func TestNotifySendsPlainCreate(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	if err := d.Notify(context.Background(), "**status**: ok"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	writes := writeCalls(env.chatAPI.callLog())
	if len(writes) != 1 || writes[0].Method != http.MethodPost {
		t.Fatalf("want one POST, got %+v", writes)
	}
	if got := writes[0].Query.Get("messageId"); got != "" {
		t.Fatalf("Notify used client id %q; notices may repeat and must not collide", got)
	}
	if got := writes[0].Body.Text; got != "**status**: ok" {
		t.Fatalf("Notify text %q, want it verbatim (no markdown conversion)", got)
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

func TestBackoffGivesUpAfterMaxAttempts(t *testing.T) {
	env := newTestEnv(t)
	for range 10 {
		env.chatAPI.pushScript(scripted{status: http.StatusTooManyRequests, grpcStatus: "RESOURCE_EXHAUSTED"})
	}
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	err := d.Notify(context.Background(), "text")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != http.StatusTooManyRequests {
		t.Fatalf("error %v, want the surfaced 429", err)
	}
	if writes := writeCalls(env.chatAPI.callLog()); len(writes) != env.adapter.maxAttempts {
		t.Fatalf("made %d attempts, want %d", len(writes), env.adapter.maxAttempts)
	}
}

func TestDefaultBackoffTruncatesAt64s(t *testing.T) {
	if defaultBackoff(0) != time.Second || defaultBackoff(3) != 8*time.Second {
		t.Fatal("backoff is not 1s·2^attempt")
	}
	if defaultBackoff(10) != 64*time.Second {
		t.Fatalf("backoff(10) = %v, want the 64s cap", defaultBackoff(10))
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

func TestTokenIsCachedAcrossCalls(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(testKey, testReplyTo, "")
	if err := d.Notify(context.Background(), "one"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := d.Notify(context.Background(), "two"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if got := env.token.mintCount(); got != 1 {
		t.Fatalf("minted %d tokens for two calls, want 1 (cached)", got)
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

func TestNewRefusesMissingConfig(t *testing.T) {
	creds := testCredentialsJSON(t)
	if _, err := New(Options{CredentialsJSON: creds}); err == nil {
		t.Fatal("New without ProjectNumber must fail (unverifiable ingress)")
	}
	if _, err := New(Options{ProjectNumber: testProjectNumber}); err == nil {
		t.Fatal("New without credentials must fail")
	}
	if _, err := New(Options{ProjectNumber: testProjectNumber, CredentialsJSON: []byte(`{"client_email":"a@b","private_key":"not-pem"}`)}); err == nil {
		t.Fatal("New with a bad key must fail")
	}
	adapter, err := New(Options{ProjectNumber: testProjectNumber, CredentialsJSON: creds})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if adapter.Platform() != "googlechat" {
		t.Fatalf("Platform %q", adapter.Platform())
	}
	if adapter.Account() != testProjectNumber {
		t.Fatalf("Account %q, want the project number", adapter.Account())
	}
}
