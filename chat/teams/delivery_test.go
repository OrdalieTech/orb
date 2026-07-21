package teams

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

func deliveryKey(chatID string) chat.ConversationKey {
	return chat.ConversationKey{Platform: platformName, Account: testAppID, ChatID: chatID}
}

func TestPersonalTypingAndFinalOnly(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.rememberConversation("a:1", env.connector.server.URL)
	d := env.adapter.NewDelivery(deliveryKey("a:1"), "evt-1", "")
	ctx := context.Background()

	if err := d.Typing(ctx); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if err := d.Preview(ctx, "Hello"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if err := d.Preview(ctx, "Hello world"); err != nil {
		t.Fatalf("second Preview: %v", err)
	}
	if id := d.PreviewID(); id != "" {
		t.Fatalf("PreviewID = %q, want empty for final-only delivery", id)
	}
	receipt, err := d.Finalize(ctx, "Hello world, done.")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	calls := env.connector.callList()
	if len(calls) != 2 {
		t.Fatalf("connector calls = %d, want 2 (typing, final)", len(calls))
	}
	wantPath := "/v3/conversations/a:1/activities"
	for i, call := range calls {
		if call.method != http.MethodPost || call.path != wantPath {
			t.Fatalf("call %d = %s %s, want POST %s", i, call.method, call.path, wantPath)
		}
	}
	if calls[0].activity["type"] != "typing" {
		t.Fatalf("call 0 = %v, want bare typing activity", calls[0].activity)
	}
	final := calls[1].activity
	if final["type"] != "message" || final["text"] != "Hello world, done." ||
		final["textFormat"] != "markdown" || final["replyToId"] != "evt-1" {
		t.Fatalf("final activity = %v", final)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "act-2" {
		t.Fatalf("receipt = %v", receipt.MessageIDs)
	}
}

func TestChannelTypingAndFinalOnly(t *testing.T) {
	env := newTestEnv(t)
	chatID := "19:chan@thread.tacv2;messageid=42"
	env.adapter.rememberConversation(chatID, env.connector.server.URL)
	d := env.adapter.NewDelivery(deliveryKey(chatID), "evt-9", "")
	ctx := context.Background()

	if err := d.Typing(ctx); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if err := d.Preview(ctx, "partial"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	receipt, err := d.Finalize(ctx, "## Result\n\nAll **good**.")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	calls := env.connector.callList()
	if len(calls) != 2 {
		t.Fatalf("connector calls = %d, want 2 (typing, final) — channel previews must be no-ops", len(calls))
	}
	if calls[0].activity["type"] != "typing" {
		t.Fatalf("call 0 = %v", calls[0].activity)
	}
	final := calls[1].activity
	if final["type"] != "message" || final["textFormat"] != "markdown" || final["replyToId"] != "evt-9" {
		t.Fatalf("final = %v", final)
	}
	if final["text"] != "**Result**\n\nAll **good**." {
		t.Fatalf("final text = %q (headings must downgrade to bold)", final["text"])
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "act-2" {
		t.Fatalf("receipt = %v", receipt.MessageIDs)
	}
}

func TestFinalizeChunkResumeNeverDuplicates(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.chunkLimit = 12
	env.adapter.rememberConversation("a:2", env.connector.server.URL)
	d := env.adapter.NewDelivery(deliveryKey("a:2"), "evt-2", "")
	ctx := context.Background()
	text := "aaaa aaaa\n\nbbbb bbbb\n\ncccc cccc"

	// Second chunk send fails hard (400 is not retryable).
	env.connector.stubAt(2, http.StatusBadRequest, `{"error":{"code":"BadArgument","message":"bad"}}`, nil)
	if _, err := d.Finalize(ctx, text); err == nil {
		t.Fatal("Finalize should fail on the second chunk")
	}
	// The processor retries with the same text: chunk 1 must not resend.
	receipt, err := d.Finalize(ctx, text)
	if err != nil {
		t.Fatalf("retry Finalize: %v", err)
	}
	var sentTexts []string
	for _, call := range env.connector.callList() {
		sentTexts = append(sentTexts, call.activity["text"].(string))
	}
	want := []string{"aaaa aaaa", "bbbb bbbb", "bbbb bbbb", "cccc cccc"}
	if len(sentTexts) != len(want) {
		t.Fatalf("sent texts = %q, want %q", sentTexts, want)
	}
	for i := range want {
		if sentTexts[i] != want[i] {
			t.Fatalf("sent texts = %q, want %q", sentTexts, want)
		}
	}
	if len(receipt.MessageIDs) != 3 {
		t.Fatalf("receipt ids = %v, want 3", receipt.MessageIDs)
	}
	if len(env.sleepList()) == 0 {
		t.Fatal("chunk pacing sleep not recorded")
	}
}

func TestRetryPolicy(t *testing.T) {
	newDelivery := func(t *testing.T) (*testEnv, chat.Delivery) {
		env := newTestEnv(t)
		env.adapter.rememberConversation("a:3", env.connector.server.URL)
		return env, env.adapter.NewDelivery(deliveryKey("a:3"), "", "")
	}

	t.Run("429 honors Retry-After", func(t *testing.T) {
		env, d := newDelivery(t)
		env.connector.stubAt(1, http.StatusTooManyRequests, `{"error":{"code":"Throttled"}}`, map[string]string{"Retry-After": "2"})
		if _, err := d.Finalize(context.Background(), "hi"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if calls := env.connector.callList(); len(calls) != 2 {
			t.Fatalf("calls = %d, want 2", len(calls))
		}
		sleeps := env.sleepList()
		if len(sleeps) == 0 || sleeps[0] != 2*time.Second {
			t.Fatalf("sleeps = %v, want the server-requested 2s first", sleeps)
		}
	})
	t.Run("429 without Retry-After backs off", func(t *testing.T) {
		env, d := newDelivery(t)
		env.connector.stubAt(1, http.StatusTooManyRequests, `{}`, nil)
		if _, err := d.Finalize(context.Background(), "hi"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		sleeps := env.sleepList()
		if len(sleeps) == 0 || sleeps[0] != env.adapter.client.backoffBase {
			t.Fatalf("sleeps = %v, want jittered backoff base", sleeps)
		}
	})
	for _, status := range []int{http.StatusPreconditionFailed, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			env, d := newDelivery(t)
			env.connector.stubAt(1, status, `{}`, nil)
			if _, err := d.Finalize(context.Background(), "hi"); err != nil {
				t.Fatalf("Finalize: %v", err)
			}
			if calls := env.connector.callList(); len(calls) != 2 {
				t.Fatalf("calls = %d, want retry then success", len(calls))
			}
		})
	}
	t.Run("retries are bounded", func(t *testing.T) {
		env, d := newDelivery(t)
		for i := 1; i <= 8; i++ {
			env.connector.stubAt(i, http.StatusBadGateway, `{}`, nil)
		}
		_, err := d.Finalize(context.Background(), "hi")
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusBadGateway {
			t.Fatalf("err = %v, want APIError 502", err)
		}
		if calls := env.connector.callList(); len(calls) != env.adapter.client.maxAttempts {
			t.Fatalf("calls = %d, want maxAttempts=%d", len(calls), env.adapter.client.maxAttempts)
		}
	})
	t.Run("401 refreshes token once and retries", func(t *testing.T) {
		env, d := newDelivery(t)
		env.connector.stubAt(1, http.StatusUnauthorized, `{}`, nil)
		if _, err := d.Finalize(context.Background(), "hi"); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		calls := env.connector.callList()
		if len(calls) != 2 {
			t.Fatalf("calls = %d, want 2", len(calls))
		}
		if env.tokenCalls.Load() != 2 {
			t.Fatalf("token fetches = %d, want 2 (initial + forced refresh)", env.tokenCalls.Load())
		}
		if calls[0].auth == calls[1].auth {
			t.Fatal("retry reused the invalidated token")
		}
	})
	t.Run("413 re-chunks smaller", func(t *testing.T) {
		env, d := newDelivery(t)
		env.adapter.chunkLimit = 4 * minChunkLimit
		env.connector.stubAt(1, http.StatusRequestEntityTooLarge, `{"error":{"code":"MessageSizeTooBig"}}`, nil)
		text := strings.Repeat("word ", 700) // one chunk under the limit, too big per the stub
		if _, err := d.Finalize(context.Background(), text); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		calls := env.connector.callList()
		if len(calls) < 3 {
			t.Fatalf("calls = %d, want the oversize send plus >=2 halved pieces", len(calls))
		}
		for _, call := range calls[1:] {
			if n := utf16Len(call.activity["text"].(string)); n > 2*minChunkLimit {
				t.Fatalf("re-chunked piece is %d units, want <= %d", n, 2*minChunkLimit)
			}
		}
	})
}

func TestWritesBlockedMarksConversationDead(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.rememberConversation("a:4", env.connector.server.URL)
	d := env.adapter.NewDelivery(deliveryKey("a:4"), "evt-4", "")
	ctx := context.Background()

	env.connector.stubAt(1, http.StatusForbidden, `{"errorCode":209,"message":{"subCode":"MessageWritesBlocked","details":"user blocked the bot"}}`, nil)
	receipt, err := d.Finalize(ctx, "hello?")
	if err != nil {
		t.Fatalf("Finalize on blocked conversation = %v, want silent nil", err)
	}
	if len(receipt.MessageIDs) != 0 {
		t.Fatalf("receipt = %v, want empty", receipt.MessageIDs)
	}
	if !env.adapter.isDead("a:4") {
		t.Fatal("conversation not marked dead")
	}
	// Every later send is swallowed without touching the connector.
	before := len(env.connector.callList())
	d2 := env.adapter.NewDelivery(deliveryKey("a:4"), "evt-5", "")
	if err := d2.Notify(ctx, "notice"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := d2.Typing(ctx); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if err := d2.Preview(ctx, "p"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	if _, err := d2.Finalize(ctx, "again"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	if after := len(env.connector.callList()); after != before {
		t.Fatalf("dead conversation still produced %d calls", after-before)
	}
	// A new inbound activity revives the conversation.
	env.adapter.rememberConversation("a:4", env.connector.server.URL)
	if env.adapter.isDead("a:4") {
		t.Fatal("inbound activity should clear the dead mark")
	}
}

func TestConversationCacheIsBoundedAndPrunesDeadEntries(t *testing.T) {
	adapter := &Adapter{convs: map[string]convInfo{}, dead: map[string]struct{}{}}
	const limit = 1024
	adapter.rememberConversation("oldest", "https://connector.example")
	adapter.markDead("oldest")
	for i := 0; i < limit; i++ {
		adapter.rememberConversation(fmt.Sprintf("conv-%d", i), "https://connector.example")
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	if got := len(adapter.convs); got > limit {
		t.Fatalf("conversation cache has %d entries, want at most %d", got, limit)
	}
	if _, ok := adapter.convs["oldest"]; ok {
		t.Fatal("oldest conversation was not evicted")
	}
	if _, ok := adapter.dead["oldest"]; ok {
		t.Fatal("dead marker survived eviction of its conversation")
	}
}

func TestFinalizeIgnoresUnresumablePreviewID(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.rememberConversation("a:5", env.connector.server.URL)
	d := env.adapter.NewDelivery(deliveryKey("a:5"), "evt-5", "stale-preview-id")
	receipt, err := d.Finalize(context.Background(), "recovered")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	calls := env.connector.callList()
	if len(calls) != 1 || calls[0].method != http.MethodPost {
		t.Fatalf("calls = %+v, want one POST because Teams is final-only", calls)
	}
	if len(receipt.MessageIDs) != 1 || receipt.MessageIDs[0] != "act-1" {
		t.Fatalf("receipt = %v", receipt.MessageIDs)
	}
}

func TestTokenCachedAcrossCalls(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.rememberConversation("a:6", env.connector.server.URL)
	ctx := context.Background()
	for i := range 3 {
		d := env.adapter.NewDelivery(deliveryKey("a:6"), "", "")
		if _, err := d.Finalize(ctx, "hello"); err != nil {
			t.Fatalf("Finalize %d: %v", i, err)
		}
	}
	if env.tokenCalls.Load() != 1 {
		t.Fatalf("token fetches = %d, want 1 (cached)", env.tokenCalls.Load())
	}
}

func TestDownloadAttachesTokenOnlyToTrustedHosts(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.rememberConversation("a:7", env.connector.server.URL)
	ctx := context.Background()

	body, mime, err := env.adapter.Download(ctx, chat.AttachmentRef{Kind: "photo", ID: env.connector.server.URL + "/v3/attachments/pic.png", MIME: "image/x-ref"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	content, _ := io.ReadAll(body)
	_ = body.Close()
	if string(content) != "IMG" || mime != "image/png" {
		t.Fatalf("content = %q mime = %q", content, mime)
	}
	calls := env.connector.callList()
	if len(calls) != 1 || !strings.HasPrefix(calls[0].auth, "Bearer test-token-") {
		t.Fatalf("connector download call = %+v, want Bearer token attached", calls)
	}

	var untrustedAuth string
	untrusted := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untrustedAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, "X")
	}))
	defer untrusted.Close()
	body, _, err = env.adapter.Download(ctx, chat.AttachmentRef{Kind: "document", ID: untrusted.URL + "/f.bin"})
	if err != nil {
		t.Fatalf("Download untrusted: %v", err)
	}
	_ = body.Close()
	if untrustedAuth != "" {
		t.Fatal("connector token leaked to an untrusted attachment host")
	}
}

func TestDeliveryWithoutServiceURL(t *testing.T) {
	env := newTestEnv(t)
	d := env.adapter.NewDelivery(deliveryKey("never-seen"), "", "")
	ctx := context.Background()
	if err := d.Typing(ctx); err == nil {
		t.Fatal("Typing without serviceUrl should error")
	}
	if err := d.Preview(ctx, "x"); err != nil {
		t.Fatalf("Preview without serviceUrl = %v, want nil no-op", err)
	}
	if _, err := d.Finalize(ctx, "x"); err == nil {
		t.Fatal("Finalize without serviceUrl should error")
	}
	if len(env.connector.callList()) != 0 {
		t.Fatal("no connector calls expected")
	}
}
