package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

func testKey(chatID string) chat.ConversationKey {
	return chat.ConversationKey{Platform: "telegram", Account: "42", ChatID: chatID}
}

// TestDeliveryFullSequence drives a complete streamed turn and asserts the
// exact call sequence: typing → preview create → preview edits → final edit
// of the preview → extra chunks as fresh sends.
func TestDeliveryFullSequence(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("77"), "tg:77:55", "")
	ctx := context.Background()

	if err := delivery.Typing(ctx); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	if err := delivery.Preview(ctx, "thinking"); err != nil {
		t.Fatalf("Preview create: %v", err)
	}
	if delivery.PreviewID() != "100" {
		t.Fatalf("PreviewID = %q, want 100", delivery.PreviewID())
	}
	if err := delivery.Preview(ctx, "thinking harder"); err != nil {
		t.Fatalf("Preview edit: %v", err)
	}
	// Unchanged text must not produce a call.
	if err := delivery.Preview(ctx, "thinking harder"); err != nil {
		t.Fatalf("Preview unchanged: %v", err)
	}

	// Two paragraphs, each too big to share one 4096-unit message.
	para := strings.Repeat("word ", 700)
	receipt, err := delivery.Finalize(ctx, para+"\n\n"+para)
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	want := []string{"sendChatAction", "sendMessage", "editMessageText", "editMessageText", "sendMessage"}
	if got := f.callMethods(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("call sequence = %v, want %v", got, want)
	}

	sends := f.callsTo("sendMessage")
	// Preview create: plain text, replies to the inbound message.
	if _, hasParse := sends[0].params["parse_mode"]; hasParse {
		t.Fatal("preview must not set parse_mode")
	}
	reply, ok := sends[0].params["reply_parameters"].(map[string]any)
	if !ok || reply["message_id"].(float64) != 55 || reply["allow_sending_without_reply"] != true {
		t.Fatalf("preview reply_parameters = %v", sends[0].params["reply_parameters"])
	}
	// Final first chunk edits the preview with HTML.
	edits := f.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if final.params["message_id"].(float64) != 100 || final.params["parse_mode"] != "HTML" {
		t.Fatalf("final edit params = %v", final.params)
	}
	if lp, ok := final.params["link_preview_options"].(map[string]any); !ok || lp["is_disabled"] != true {
		t.Fatalf("link preview not disabled: %v", final.params)
	}
	// Second chunk is a fresh send without reply parameters.
	if sends[1].params["parse_mode"] != "HTML" {
		t.Fatalf("chunk send parse_mode = %v", sends[1].params["parse_mode"])
	}
	if _, hasReply := sends[1].params["reply_parameters"]; hasReply {
		t.Fatal("only the first chunk may carry reply_parameters")
	}
	if len(receipt.MessageIDs) != 2 || receipt.MessageIDs[0] != "100" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestDeliveryFinalizeWithoutPreviewRepliesToInbound(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("77"), "tg:77:55", "")

	receipt, err := delivery.Finalize(context.Background(), "done")
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	sends := f.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("expected one send, got %d", len(sends))
	}
	reply, ok := sends[0].params["reply_parameters"].(map[string]any)
	if !ok || reply["message_id"].(float64) != 55 || reply["allow_sending_without_reply"] != true {
		t.Fatalf("reply_parameters = %v", sends[0].params["reply_parameters"])
	}
	if len(receipt.MessageIDs) != 1 {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestDeliveryResumeEditsRecordedPreview(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("77"), "tg:77:55", "123")

	if _, err := delivery.Finalize(context.Background(), "recovered"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	edits := f.callsTo("editMessageText")
	if len(edits) != 1 || edits[0].params["message_id"].(float64) != 123 {
		t.Fatalf("expected one edit of message 123, got %v", edits)
	}
	if len(f.callsTo("sendMessage")) != 0 {
		t.Fatal("resume must edit, not send")
	}
}

func TestDeliveryFinalizeRetryResumesFromFailedChunk(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("77"), "tg:77:55", "")
	ctx := context.Background()

	// Two distinct paragraphs, each too big to share one 4096-unit message:
	// chunk 1 sends fine, chunk 2 fails transiently on the first Finalize.
	text := strings.Repeat("one ", 900) + "\n\n" + strings.Repeat("two ", 900)
	f.stub("sendMessage", `{"ok":true,"result":{"message_id":200}}`)
	f.stub("sendMessage", errorBody(500, "Internal Server Error", 0))

	if _, err := delivery.Finalize(ctx, text); err == nil {
		t.Fatal("Finalize succeeded although chunk 2 failed")
	}
	if got := len(f.callsTo("sendMessage")); got != 2 {
		t.Fatalf("sends after failed Finalize = %d, want 2", got)
	}

	// The processor re-invokes Finalize with the same text: chunk 1 must not
	// be resent, only the failed chunk goes out.
	receipt, err := delivery.Finalize(ctx, text)
	if err != nil {
		t.Fatalf("retry Finalize: %v", err)
	}
	sends := f.callsTo("sendMessage")
	if len(sends) != 3 {
		t.Fatalf("sends after retry = %d, want 3 (no duplicate of chunk 1)", len(sends))
	}
	if sends[0].params["text"] == sends[2].params["text"] {
		t.Fatal("retry resent the already-delivered first chunk")
	}
	if len(receipt.MessageIDs) != 2 || receipt.MessageIDs[0] != "200" {
		t.Fatalf("receipt = %+v", receipt)
	}
}

func TestDeliveryParseEntitiesFallback(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	delivery := adapter.NewDelivery(testKey("77"), "", "")
	f.stub("sendMessage", errorBody(400, `Bad Request: can't parse entities: unsupported start tag`, 0))

	if _, err := delivery.Finalize(context.Background(), "broken <tag>"); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	sends := f.callsTo("sendMessage")
	if len(sends) != 2 {
		t.Fatalf("expected HTML attempt + plain resend, got %d sends", len(sends))
	}
	if sends[0].params["parse_mode"] != "HTML" {
		t.Fatalf("first send parse_mode = %v", sends[0].params["parse_mode"])
	}
	if _, hasParse := sends[1].params["parse_mode"]; hasParse {
		t.Fatal("fallback resend must not set parse_mode")
	}
	if sends[0].params["text"] != sends[1].params["text"] {
		t.Fatal("fallback must resend the same chunk")
	}
}

func TestDeliveryTypingRefreshesUntilNotify(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.TypingInterval = 5 * time.Millisecond })
	delivery := adapter.NewDelivery(testKey("77"), "", "")
	ctx := context.Background()

	if err := delivery.Typing(ctx); err != nil {
		t.Fatalf("Typing: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(f.callsTo("sendChatAction")) < 3 {
		if time.Now().After(deadline) {
			t.Fatal("typing refresher never ticked")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := delivery.Notify(ctx, "stopped"); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	sends := f.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].params["text"] != "stopped" {
		t.Fatalf("notify send = %v", sends)
	}
	if _, hasParse := sends[0].params["parse_mode"]; hasParse {
		t.Fatal("Notify must be plain text")
	}
}

func TestDeliveryPreviewRateLimitSkips(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.PreviewMinInterval = time.Hour })
	delivery := adapter.NewDelivery(testKey("77"), "", "")
	ctx := context.Background()

	if err := delivery.Preview(ctx, "one"); err != nil {
		t.Fatalf("Preview: %v", err)
	}
	// Inside the min interval: the edit is skipped and reported as throttled
	// so the coalescer keeps the snapshot dirty for a later tick.
	if err := delivery.Preview(ctx, "two"); !errors.Is(err, errPreviewThrottled) {
		t.Fatalf("Preview = %v, want errPreviewThrottled", err)
	}
	if got := len(f.callMethods()); got != 1 {
		t.Fatalf("expected only the preview create, got %d calls", got)
	}
	// Unchanged text is still a silent skip, not a throttle.
	if err := delivery.Preview(ctx, "one"); err != nil {
		t.Fatalf("Preview unchanged = %v, want nil", err)
	}
}
