package slack

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

func testKey(chatID, threadID string) chat.ConversationKey {
	return chat.ConversationKey{Platform: "slack", Account: testBotUser, ChatID: chatID, ThreadID: threadID}
}

func TestTypingIsNoop(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("D0DM", ""), "", "")
	if err := delivery.Typing(context.Background()); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if calls := f.callMethods(); len(calls) != 0 {
		t.Fatalf("Typing made API calls: %v", calls)
	}
}

func TestPreviewCreateThenEdit(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.PreviewMinInterval = 50 * time.Millisecond })
	delivery := adapter.NewDelivery(testKey("C0CHAN", "1700000000.000100"), "", "")
	ctx := context.Background()

	if err := delivery.Preview(ctx, "Hello"); err != nil {
		t.Fatalf("first Preview: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 1 {
		t.Fatalf("postMessage calls = %d, want 1", len(posts))
	}
	if got := posts[0].params["text"]; got != "Hello" {
		t.Fatalf("preview text = %v", got)
	}
	if got := posts[0].params["thread_ts"]; got != "1700000000.000100" {
		t.Fatalf("preview thread_ts = %v", got)
	}
	previewID := delivery.PreviewID()
	if previewID == "" {
		t.Fatal("PreviewID empty after preview creation")
	}

	// Within the min interval the edit is throttled, not dropped.
	if err := delivery.Preview(ctx, "Hello wor"); !errors.Is(err, errPreviewThrottled) {
		t.Fatalf("throttled Preview error = %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	if err := delivery.Preview(ctx, "Hello world"); err != nil {
		t.Fatalf("edit Preview: %v", err)
	}
	updates := f.callsTo("chat.update")
	if len(updates) != 1 {
		t.Fatalf("chat.update calls = %d, want 1", len(updates))
	}
	if got := updates[0].params["ts"]; got != previewID {
		t.Fatalf("update ts = %v, want %v", got, previewID)
	}
	if got := updates[0].params["text"]; got != "Hello world" {
		t.Fatalf("update text = %v", got)
	}

	// Unchanged text is skipped without an API call.
	time.Sleep(60 * time.Millisecond)
	if err := delivery.Preview(ctx, "Hello world"); err != nil {
		t.Fatalf("unchanged Preview: %v", err)
	}
	if got := len(f.callsTo("chat.update")); got != 1 {
		t.Fatalf("unchanged text still edited: %d updates", got)
	}
}

func TestPreviewEditWindowClosedFallsBack(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("C0CHAN", ""), "", "")
	ctx := context.Background()

	if err := delivery.Preview(ctx, "partial"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	f.stub("chat.update", stubResponse{body: `{"ok":false,"error":"edit_window_closed"}`})
	if err := delivery.Preview(ctx, "partial more"); err != nil {
		t.Fatalf("Preview after edit_window_closed = %v, want nil (edits abandoned)", err)
	}
	// Further previews stop entirely.
	if err := delivery.Preview(ctx, "partial even more"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if got := len(f.callsTo("chat.update")); got != 1 {
		t.Fatalf("chat.update calls = %d, want 1 (edits dead)", got)
	}

	// Finalize must not try to edit: the final text is a new post.
	receipt, err := delivery.Finalize(ctx, "final answer")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if got := len(f.callsTo("chat.update")); got != 1 {
		t.Fatalf("Finalize edited a dead preview: %d updates", got)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 2 { // preview + final
		t.Fatalf("postMessage calls = %d, want 2", len(posts))
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] == delivery.PreviewID() {
		t.Fatalf("receipt = %+v, want one new message id", receipt.MessageIDs)
	}
}

func TestFinalizeEditsPreviewAndChunksRest(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("C0CHAN", "1700000000.000100"), "", "")
	ctx := context.Background()

	if err := delivery.Preview(ctx, "thinking…"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	previewID := delivery.PreviewID()

	// Two paragraphs that cannot share a 4,000-char chunk.
	long := strings.Repeat("a", 2500) + "\n\n" + strings.Repeat("b", 2500)
	receipt, err := delivery.Finalize(ctx, long)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	updates := f.callsTo("chat.update")
	if len(updates) != 1 {
		t.Fatalf("chat.update calls = %d, want 1 (chunk 1 edits the preview)", len(updates))
	}
	if got := updates[0].params["ts"]; got != previewID {
		t.Fatalf("final edit ts = %v, want preview %v", got, previewID)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 2 { // preview creation + chunk 2
		t.Fatalf("postMessage calls = %d, want 2", len(posts))
	}
	if got := posts[1].params["thread_ts"]; got != "1700000000.000100" {
		t.Fatalf("chunk 2 thread_ts = %v", got)
	}
	if len(receipt.MessageIDs) != 2 || receipt.MessageIDs[0] != previewID {
		t.Fatalf("receipt = %+v", receipt.MessageIDs)
	}
}

func TestFinalizeWithoutPreviewPostsNew(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("D0DM", ""), "", "")
	receipt, err := delivery.Finalize(context.Background(), "**bold** answer")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 1 {
		t.Fatalf("postMessage calls = %d, want 1", len(posts))
	}
	if got := posts[0].params["text"]; got != "*bold* answer" {
		t.Fatalf("final text = %v, want mrkdwn transcoding", got)
	}
	if _, present := posts[0].params["thread_ts"]; present {
		t.Fatalf("DM final carried thread_ts: %v", posts[0].params)
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt = %+v", receipt.MessageIDs)
	}
}

func TestFinalizeResumesEditingRecoveredPreview(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	// Crash recovery: the processor passes the ledger's preview id.
	delivery := adapter.NewDelivery(testKey("C0CHAN", "1700000000.000100"), "", "1700000000.000777")
	if _, err := delivery.Finalize(context.Background(), "recovered"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	updates := f.callsTo("chat.update")
	if len(updates) != 1 || updates[0].params["ts"] != "1700000000.000777" {
		t.Fatalf("updates = %+v, want one edit of the resumed preview", updates)
	}
	if got := len(f.callsTo("chat.postMessage")); got != 0 {
		t.Fatalf("postMessage calls = %d, want 0", got)
	}
}

func TestFinalizeRetrySkipsSentChunks(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("C0CHAN", "1700000000.000100"), "", "")
	ctx := context.Background()

	long := strings.Repeat("a", 2500) + "\n\n" + strings.Repeat("b", 2500)
	// Chunk 1 posts fine; chunk 2 fails with a permanent error.
	f.stub("chat.postMessage", stubResponse{body: `{"ok":true,"channel":"C0CHAN","ts":"1700000001.000001"}`})
	f.stub("chat.postMessage", stubResponse{body: `{"ok":false,"error":"fatal_error"}`})
	if _, err := delivery.Finalize(ctx, long); err == nil {
		t.Fatal("Finalize succeeded, want chunk-2 failure")
	}
	if got := len(f.callsTo("chat.postMessage")); got != 2 {
		t.Fatalf("postMessage calls = %d, want 2", got)
	}

	// The processor retries Finalize with the same text: chunk 1 must not be
	// sent again.
	receipt, err := delivery.Finalize(ctx, long)
	if err != nil {
		t.Fatalf("retry Finalize: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 3 {
		t.Fatalf("postMessage calls = %d, want 3 (chunk 1 exactly once)", len(posts))
	}
	if got, want := posts[2].params["text"], strings.Repeat("b", 2500); got != want {
		t.Fatalf("retried chunk = %.20v…, want chunk 2", got)
	}
	if len(receipt.MessageIDs) != 2 {
		t.Fatalf("receipt = %+v", receipt.MessageIDs)
	}
}

func TestFinalizeEmptyReply(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("D0DM", ""), "", "")
	if _, err := delivery.Finalize(context.Background(), "  \n "); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 1 || posts[0].params["text"] != "(empty reply)" {
		t.Fatalf("posts = %+v, want one \"(empty reply)\"", posts)
	}
}

func TestNotifyPostsPlainText(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("C0CHAN", "1700000000.000100"), "", "")
	if err := delivery.Notify(context.Background(), "**status**: 42 tokens"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 1 {
		t.Fatalf("postMessage calls = %d, want 1", len(posts))
	}
	if got := posts[0].params["text"]; got != "**status**: 42 tokens" {
		t.Fatalf("notify text = %v, want raw text (no transcoding)", got)
	}
	if got := posts[0].params["thread_ts"]; got != "1700000000.000100" {
		t.Fatalf("notify thread_ts = %v", got)
	}
}

func TestPreviewAndNotifyEscapeSlackControlSequences(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("C0CHAN", ""), "", "")
	const raw = "<!channel> ask <@U123> about A&B"
	const escaped = "&lt;!channel&gt; ask &lt;@U123&gt; about A&amp;B"
	if err := delivery.Preview(context.Background(), raw); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if err := delivery.Notify(context.Background(), raw); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	posts := f.callsTo("chat.postMessage")
	if len(posts) != 2 {
		t.Fatalf("postMessage calls = %d, want preview and notification", len(posts))
	}
	for i, post := range posts {
		if got := post.params["text"]; got != escaped {
			t.Errorf("post %d text = %q, want %q", i, got, escaped)
		}
	}
}

func TestRateLimit429Honored(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	var slept []time.Duration
	adapter.client.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	f.stub("chat.postMessage", stubResponse{status: 429, retryAfter: 7, body: `{"ok":false,"error":"ratelimited"}`})
	delivery := adapter.NewDelivery(testKey("D0DM", ""), "", "")
	receipt, err := delivery.Finalize(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(slept) != 1 || slept[0] != 7*time.Second {
		t.Fatalf("slept = %v, want one 7s pause from Retry-After", slept)
	}
	if got := len(f.callsTo("chat.postMessage")); got != 2 {
		t.Fatalf("postMessage calls = %d, want 2 (429 then success)", got)
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt = %+v", receipt.MessageIDs)
	}
}

func TestBodyLevelRatelimitedRetried(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	var slept []time.Duration
	adapter.client.sleep = func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}
	f.stub("chat.postMessage", stubResponse{body: `{"ok":false,"error":"ratelimited"}`})
	delivery := adapter.NewDelivery(testKey("D0DM", ""), "", "")
	if _, err := delivery.Finalize(context.Background(), "hello"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if len(slept) != 1 || slept[0] != time.Second {
		t.Fatalf("slept = %v, want one default 1s pause", slept)
	}
}

func TestAPIErrorSurfacesCode(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	f.stub("chat.postMessage", stubResponse{body: `{"ok":false,"error":"channel_not_found"}`})
	delivery := adapter.NewDelivery(testKey("C0GONE", ""), "", "")
	_, err := delivery.Finalize(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "channel_not_found" {
		t.Fatalf("err = %v, want APIError channel_not_found", err)
	}
	if !strings.Contains(err.Error(), "invited") {
		t.Fatalf("error lacks operator hint: %v", err)
	}
}
