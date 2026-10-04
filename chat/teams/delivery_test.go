package teams

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/chat"
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
