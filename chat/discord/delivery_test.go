package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// restCall is one recorded REST request.
type restCall struct {
	method string
	path   string
	body   map[string]any
}

// fakeRest records every REST call and lets tests script per-request
// responses. Every message create/edit payload is checked for the
// allowed_mentions {"parse":[]} guard.
type fakeRest struct {
	t  *testing.T
	mu sync.Mutex
	// calls records every request in order.
	calls []restCall
	// respond, when set, may write a scripted response and return true;
	// returning false falls through to the default success response.
	respond func(n int, call restCall, w http.ResponseWriter) bool
	nextID  int
}

func newFakeRest(t *testing.T) (*fakeRest, *Adapter) {
	t.Helper()
	f := &fakeRest{t: t}
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(srv.Close)
	adapter, err := New(Options{
		Token:              "OTk5.fake.token",
		BaseURL:            srv.URL,
		BotUserID:          "999",
		TypingInterval:     10 * time.Millisecond,
		PreviewMinInterval: 25 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return f, adapter
}

func (f *fakeRest) handle(w http.ResponseWriter, r *http.Request) {
	if got, want := r.Header.Get("Authorization"), "Bot OTk5.fake.token"; got != want {
		f.t.Errorf("Authorization = %q, want %q", got, want)
	}
	call := restCall{method: r.Method, path: r.URL.Path}
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		if err := json.Unmarshal(data, &call.body); err != nil {
			f.t.Errorf("undecodable request body on %s %s: %v", r.Method, r.URL.Path, err)
		}
	}
	// Every message payload must disarm mass mentions.
	if strings.Contains(call.path, "/messages") && call.body != nil {
		am, ok := call.body["allowed_mentions"].(map[string]any)
		if !ok {
			f.t.Errorf("%s %s: missing allowed_mentions", call.method, call.path)
		} else if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
			f.t.Errorf("%s %s: allowed_mentions.parse = %v, want []", call.method, call.path, am["parse"])
		}
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	n := len(f.calls)
	respond := f.respond
	f.nextID++
	id := f.nextID
	f.mu.Unlock()
	if respond != nil && respond(n, call, w) {
		return
	}
	switch {
	case strings.HasSuffix(call.path, "/typing"):
		w.WriteHeader(http.StatusNoContent)
	case call.method == http.MethodPost && strings.HasSuffix(call.path, "/messages"):
		_, _ = fmt.Fprintf(w, `{"id":"msg-%d"}`, id)
	case call.method == http.MethodPatch:
		_, _ = fmt.Fprintf(w, `{"id":"edited"}`)
	default:
		f.t.Errorf("unexpected REST call: %s %s", call.method, call.path)
		http.NotFound(w, r)
	}
}

// setRespond installs a scripted responder, synchronized with the handler.
func (f *fakeRest) setRespond(fn func(n int, call restCall, w http.ResponseWriter) bool) {
	f.mu.Lock()
	f.respond = fn
	f.mu.Unlock()
}

// snapshot returns a copy of the recorded calls.
func (f *fakeRest) snapshot() []restCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]restCall(nil), f.calls...)
}

// countCalls counts recorded calls matching method and path suffix.
func (f *fakeRest) countCalls(method, pathSuffix string) int {
	count := 0
	for _, call := range f.snapshot() {
		if call.method == method && strings.HasSuffix(call.path, pathSuffix) {
			count++
		}
	}
	return count
}

func newTestDelivery(adapter *Adapter, replyTo, resumePreviewID string) chat.Delivery {
	key := chat.ConversationKey{Platform: platformName, Account: "999", ChatID: "chan1"}
	return adapter.NewDelivery(key, replyTo, resumePreviewID)
}

func TestTypingRefreshUntilFinalize(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "dc:chan1:m0", "")
	if err := d.Typing(t.Context()); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	// The refresher ticks every 10ms; wait for at least two refreshes.
	deadline := time.Now().Add(2 * time.Second)
	for fake.countCalls(http.MethodPost, "/typing") < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fake.countCalls(http.MethodPost, "/typing"); got < 3 {
		t.Fatalf("typing calls = %d, want >= 3 (initial + refreshes)", got)
	}
	if _, err := d.Finalize(t.Context(), "done"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	settled := fake.countCalls(http.MethodPost, "/typing")
	time.Sleep(50 * time.Millisecond)
	if got := fake.countCalls(http.MethodPost, "/typing"); got > settled {
		t.Errorf("typing refreshed after Finalize: %d -> %d", settled, got)
	}
}

func TestPreviewCreateThenEditThenFinalizeEdits(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "dc:chan1:m0", "")

	if err := d.Preview(t.Context(), "partial…"); err != nil {
		t.Fatalf("first Preview: %v", err)
	}
	if got := d.PreviewID(); got != "msg-1" {
		t.Fatalf("PreviewID = %q, want msg-1", got)
	}
	// Within the throttle window the edit is refused, not dropped silently.
	if err := d.Preview(t.Context(), "partial, more"); !errors.Is(err, errPreviewThrottled) {
		t.Fatalf("throttled Preview error = %v, want errPreviewThrottled", err)
	}
	time.Sleep(30 * time.Millisecond)
	if err := d.Preview(t.Context(), "partial, more"); err != nil {
		t.Fatalf("second Preview: %v", err)
	}
	// Unchanged text is skipped without a call.
	before := len(fake.snapshot())
	time.Sleep(30 * time.Millisecond)
	if err := d.Preview(t.Context(), "partial, more"); err != nil {
		t.Fatalf("unchanged Preview: %v", err)
	}
	if got := len(fake.snapshot()); got != before {
		t.Errorf("unchanged preview issued a call (%d -> %d)", before, got)
	}

	receipt, err := d.Finalize(t.Context(), "final text")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "msg-1" {
		t.Errorf("receipt ids = %v, want [msg-1]", receipt.MessageIDs)
	}

	calls := fake.snapshot()
	var sequence []string
	for _, call := range calls {
		if strings.Contains(call.path, "/messages") {
			sequence = append(sequence, call.method+" "+call.path)
		}
	}
	want := []string{
		"POST /channels/chan1/messages",
		"PATCH /channels/chan1/messages/msg-1",
		"PATCH /channels/chan1/messages/msg-1",
	}
	if len(sequence) != len(want) {
		t.Fatalf("message calls = %v, want %v", sequence, want)
	}
	for i := range want {
		if sequence[i] != want[i] {
			t.Errorf("call %d = %q, want %q", i, sequence[i], want[i])
		}
	}
	// The preview create is the first chunk in spirit: it must carry the
	// reply reference with fail_if_not_exists false.
	first := calls[0]
	ref, ok := first.body["message_reference"].(map[string]any)
	if !ok {
		t.Fatal("preview create missing message_reference")
	}
	if ref["message_id"] != "m0" {
		t.Errorf("message_reference.message_id = %v, want m0", ref["message_id"])
	}
	if fail, ok := ref["fail_if_not_exists"].(bool); !ok || fail {
		t.Errorf("fail_if_not_exists = %v, want explicit false", ref["fail_if_not_exists"])
	}
}

func TestFinalizeChunksReplyOnFirstOnlyAndResumesOnRetry(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "dc:chan1:m0", "")

	paragraph := strings.Repeat("word ", 300) // ~1500 runes
	text := strings.TrimSpace(paragraph) + "\n\n" + strings.TrimSpace(paragraph) + "\n\n" + strings.TrimSpace(paragraph)

	fail := true
	fake.setRespond(func(n int, call restCall, w http.ResponseWriter) bool {
		// Fail the third chunk send once.
		if fail && call.method == http.MethodPost && fake.countCalls(http.MethodPost, "/messages") == 3 {
			fail = false
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"message":"upstream hiccup","code":0}`)
			return true
		}
		return false
	})

	if _, err := d.Finalize(t.Context(), text); err == nil {
		t.Fatal("Finalize succeeded, want mid-way failure")
	}
	if got := fake.countCalls(http.MethodPost, "/messages"); got != 3 {
		t.Fatalf("sends before retry = %d, want 3 (two delivered, one failed)", got)
	}

	receipt, err := d.Finalize(t.Context(), text)
	if err != nil {
		t.Fatalf("Finalize retry: %v", err)
	}
	if got := len(receipt.MessageIDs); got != 3 {
		t.Fatalf("receipt ids = %v, want 3 chunks", receipt.MessageIDs)
	}

	var sends []restCall
	for _, call := range fake.snapshot() {
		if call.method == http.MethodPost && strings.HasSuffix(call.path, "/messages") {
			sends = append(sends, call)
		}
	}
	if len(sends) != 4 {
		t.Fatalf("total sends = %d, want 4 (3 + 1 retried chunk, never re-sending delivered ones)", len(sends))
	}
	for i, call := range sends {
		_, hasRef := call.body["message_reference"]
		if (i == 0) != hasRef {
			t.Errorf("send %d message_reference presence = %t, want first-chunk-only", i, hasRef)
		}
		content, _ := call.body["content"].(string)
		if n := len([]rune(content)); n == 0 || n > messageLimit {
			t.Errorf("send %d content length = %d runes, want 1..%d", i, n, messageLimit)
		}
	}
	// The retried chunk must be the third chunk's text, not a duplicate.
	if sends[3].body["content"] != sends[2].body["content"] {
		t.Errorf("retried chunk content differs from the failed chunk")
	}
}

func TestFinalizeResumePreviewFromCrashRecovery(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "dc:chan1:m0", "recovered-7")
	receipt, err := d.Finalize(t.Context(), "recovered reply")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "recovered-7" {
		t.Errorf("receipt ids = %v, want [recovered-7]", receipt.MessageIDs)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].method != http.MethodPatch ||
		!strings.HasSuffix(calls[0].path, "/messages/recovered-7") {
		t.Errorf("calls = %+v, want a single PATCH of recovered-7", calls)
	}
}

func TestPreviewRecreatedWhenDeleted(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "", "")
	if err := d.Preview(t.Context(), "first"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	fake.setRespond(func(n int, call restCall, w http.ResponseWriter) bool {
		if call.method == http.MethodPatch {
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"message":"Unknown Message","code":10008}`)
			return true
		}
		return false
	})
	time.Sleep(30 * time.Millisecond)
	if err := d.Preview(t.Context(), "second"); err != nil {
		t.Fatalf("Preview after deletion: %v", err)
	}
	if got := d.PreviewID(); got == "msg-1" || got == "" {
		t.Errorf("PreviewID = %q, want a freshly created id", got)
	}
	if got := fake.countCalls(http.MethodPost, "/messages"); got != 2 {
		t.Errorf("creates = %d, want 2 (original + recreated)", got)
	}
}

func TestRateLimit429HonorsRetryAfter(t *testing.T) {
	fake, adapter := newFakeRest(t)
	var slept []time.Duration
	adapter.client.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	limited := true
	fake.setRespond(func(n int, call restCall, w http.ResponseWriter) bool {
		if limited && call.method == http.MethodPost && strings.HasSuffix(call.path, "/messages") {
			limited = false
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = fmt.Fprint(w, `{"message":"You are being rate limited.","retry_after":1.337,"global":false}`)
			return true
		}
		return false
	})
	d := newTestDelivery(adapter, "", "")
	receipt, err := d.Finalize(t.Context(), "rate limited once")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt = %v, want one message", receipt.MessageIDs)
	}
	if len(slept) != 1 || slept[0] != time.Duration(1.337*float64(time.Second)) {
		t.Errorf("sleeps = %v, want one 1.337s pause", slept)
	}
	if got := fake.countCalls(http.MethodPost, "/messages"); got != 2 {
		t.Errorf("sends = %d, want 2 (429 then success)", got)
	}
}

func TestUnauthorizedSurfacesWithoutRetry(t *testing.T) {
	fake, adapter := newFakeRest(t)
	fake.setRespond(func(n int, call restCall, w http.ResponseWriter) bool {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"message":"401: Unauthorized","code":0}`)
		return true
	})
	d := newTestDelivery(adapter, "", "")
	_, err := d.Finalize(t.Context(), "should fail fast")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("Finalize error = %v, want *APIError with 401", err)
	}
	if got := len(fake.snapshot()); got != 1 {
		t.Errorf("calls = %d, want exactly 1 (401 must never be retried)", got)
	}
}

func TestNotifyPlainSend(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "dc:chan1:m0", "")
	if err := d.Notify(t.Context(), "heads up"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	calls := fake.snapshot()
	if len(calls) != 1 || calls[0].method != http.MethodPost {
		t.Fatalf("calls = %+v, want one POST", calls)
	}
	if _, hasRef := calls[0].body["message_reference"]; hasRef {
		t.Error("Notify attached a message_reference, want a bare send")
	}
	if calls[0].body["content"] != "heads up" {
		t.Errorf("content = %v, want %q", calls[0].body["content"], "heads up")
	}
}

func TestEmptyFinalizeDeliversPlaceholder(t *testing.T) {
	fake, adapter := newFakeRest(t)
	d := newTestDelivery(adapter, "", "")
	receipt, err := d.Finalize(t.Context(), "   \n ")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt = %v, want one message", receipt.MessageIDs)
	}
	if got := fake.snapshot()[0].body["content"]; got != "(empty reply)" {
		t.Errorf("content = %v, want %q", got, "(empty reply)")
	}
}

func TestDownloadPlainGetWithoutAuth(t *testing.T) {
	var sawAuth string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "image/png")
		_, _ = fmt.Fprint(w, "pngbytes")
	}))
	t.Cleanup(cdn.Close)
	_, adapter := newFakeRest(t)
	body, mime, err := adapter.Download(t.Context(), chat.AttachmentRef{
		Kind: "photo", ID: cdn.URL + "/attachments/1/2/a.png?ex=1&is=2&hm=3", Name: "a.png", MIME: "image/png",
	})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = body.Close() }()
	data, _ := io.ReadAll(body)
	if string(data) != "pngbytes" || mime != "image/png" {
		t.Errorf("Download = %q/%q, want pngbytes/image/png", data, mime)
	}
	if sawAuth != "" {
		t.Errorf("Download sent Authorization %q, pre-signed URLs must be fetched bare", sawAuth)
	}
	if _, _, err := adapter.Download(t.Context(), chat.AttachmentRef{Kind: "photo", ID: "not-a-url"}); err == nil {
		t.Error("Download accepted a non-URL ref")
	}
}
