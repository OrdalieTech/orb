package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// recorder collects published messages.
type recorder struct {
	mu       sync.Mutex
	messages []chat.Message
	fail     error
}

func (r *recorder) publish(m chat.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.messages = append(r.messages, m)
	return nil
}

func (r *recorder) snapshot() []chat.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]chat.Message(nil), r.messages...)
}

func textUpdate(updateID, chatID, messageID int64, text string) apiUpdate {
	return apiUpdate{UpdateID: updateID, Message: &apiMessage{
		MessageID: messageID,
		Date:      1752900000,
		From:      &apiUser{ID: 111, FirstName: "Léa", Username: "lea"},
		Chat:      apiChat{ID: chatID, Type: "private"},
		Text:      text,
	}}
}

func TestWebhookSecretConstantTimeCheck(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.Secret = "s3cret" })
	rec := &recorder{}
	handler := adapter.Webhook(rec.publish)

	body := `{"update_id":1,"message":{"message_id":55,"date":1752900000,` +
		`"from":{"id":111,"first_name":"Léa"},"chat":{"id":10,"type":"private"},"text":"hello"}}`

	for _, tc := range []struct {
		name   string
		secret string
		want   int
	}{
		{"missing secret", "", http.StatusForbidden},
		{"wrong secret", "nope", http.StatusForbidden},
		{"right secret", "s3cret", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(body))
		if tc.secret != "" {
			req.Header.Set(secretTokenHeader, tc.secret)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("%s: status = %d, want %d", tc.name, w.Code, tc.want)
		}
	}

	messages := rec.snapshot()
	if len(messages) != 1 {
		t.Fatalf("expected exactly one published message, got %d", len(messages))
	}
	if messages[0].EventID != "tg:10:55" || messages[0].Text != "hello" || messages[0].ChatType != "dm" {
		t.Fatalf("unexpected message: %+v", messages[0])
	}
}

func TestWebhookEmptySecretRejectsEverything(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f) // no Secret configured
	rec := &recorder{}
	handler := adapter.Webhook(rec.publish)

	req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(
		`{"update_id":1,"message":{"message_id":55,"date":1,"from":{"id":111,"first_name":"L"},"chat":{"id":10,"type":"private"},"text":"hi"}}`))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: an empty secret must not accept posts", w.Code)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("nothing may be published without a webhook secret")
	}
}

func TestWebhookPublishFailureAnswers500(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.Secret = "s3cret" })
	rec := &recorder{fail: errors.New("spool down")}
	handler := adapter.Webhook(rec.publish)

	req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(
		`{"update_id":1,"message":{"message_id":55,"date":1,"from":{"id":111,"first_name":"L"},"chat":{"id":10,"type":"private"},"text":"hi"}}`))
	req.Header.Set(secretTokenHeader, "s3cret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so Telegram redelivers", w.Code)
	}
}

func TestWebhookAlbumHeldUntilFlush(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f, func(o *Options) { o.Secret = "s3cret" })
	rec := &recorder{fail: errors.New("spool down")}
	handler := adapter.Webhook(rec.publish)

	body := `{"update_id":1,"message":{"message_id":55,"date":1,"from":{"id":111,"first_name":"L"},` +
		`"chat":{"id":10,"type":"private"},"media_group_id":"a1","caption":"pics",` +
		`"photo":[{"file_id":"AgA1","file_size":9}]}}`
	post := func() int {
		req := httptest.NewRequest(http.MethodPost, "/tg", strings.NewReader(body))
		req.Header.Set(secretTokenHeader, "s3cret")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w.Code
	}

	// The response must wait for the group flush and answer non-2xx on a
	// failed publish so Telegram redelivers the album.
	if code := post(); code != http.StatusInternalServerError {
		t.Fatalf("failed album flush answered %d, want 500", code)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatal("failed flush must not record a message")
	}

	// Redelivery with a healthy spool publishes and answers 200.
	rec.fail = nil
	if code := post(); code != http.StatusOK {
		t.Fatalf("album part answered %d, want 200", code)
	}
	messages := rec.snapshot()
	if len(messages) != 1 || messages[0].Text != "pics" || len(messages[0].Attachments) != 1 {
		t.Fatalf("expected the published album, got %+v", messages)
	}
}

func TestPollOffsetAdvancesOnlyAfterPublish(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)

	// First run: publish fails, Poll must return without acking the update.
	f.pushBatch(textUpdate(7, 10, 55, "hello"))
	rec := &recorder{fail: errors.New("spool down")}
	err := adapter.Poll(context.Background(), rec.publish)
	if err == nil || !strings.Contains(err.Error(), "spool down") {
		t.Fatalf("expected the publish error, got %v", err)
	}
	first := f.callsTo("getUpdates")
	if len(first) != 1 {
		t.Fatalf("expected one getUpdates call, got %d", len(first))
	}
	if got := first[0].params["offset"].(float64); got != 0 {
		t.Fatalf("first poll offset = %v, want 0", got)
	}
	// deleteWebhook must precede polling.
	methods := f.callMethods()
	if methods[0] != "deleteWebhook" {
		t.Fatalf("expected deleteWebhook first, got %v", methods)
	}

	// Second run (fresh process): the unacked update is redelivered, publish
	// succeeds, and the offset advances past it.
	f.pushBatch(textUpdate(7, 10, 55, "hello"))
	rec.fail = nil
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Poll(ctx, rec.publish) }()
	<-f.drained
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Poll returned %v, want context.Canceled", err)
	}

	messages := rec.snapshot()
	if len(messages) != 1 || messages[0].EventID != "tg:10:55" {
		t.Fatalf("expected the redelivered message once, got %+v", messages)
	}
	calls := f.callsTo("getUpdates")
	last := calls[len(calls)-1]
	if got := last.params["offset"].(float64); got != 8 {
		t.Fatalf("final offset = %v, want 8 (7+1 acked only after publish)", got)
	}
	if allowed, ok := last.params["allowed_updates"].([]any); !ok || len(allowed) != 1 || allowed[0] != "message" {
		t.Fatalf(`allowed_updates = %v, want ["message"]`, last.params["allowed_updates"])
	}
}

func TestPollMergesMediaGroups(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	photo := func(updateID, messageID int64, caption, fileID string) apiUpdate {
		return apiUpdate{UpdateID: updateID, Message: &apiMessage{
			MessageID:    messageID,
			Date:         1752900000,
			From:         &apiUser{ID: 111, FirstName: "Léa"},
			Chat:         apiChat{ID: 10, Type: "private"},
			Caption:      caption,
			Photo:        []apiPhotoSize{{FileID: fileID + "-small", FileSize: 10}, {FileID: fileID, FileSize: 999}},
			MediaGroupID: "album-1",
		}}
	}
	f.pushBatch(photo(1, 55, "look at these", "AgA1"), photo(2, 56, "", "AgA2"))

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Poll(ctx, rec.publish) }()
	<-f.drained
	cancel()
	<-done

	messages := rec.snapshot()
	if len(messages) != 1 {
		t.Fatalf("expected one merged album message, got %d", len(messages))
	}
	m := messages[0]
	if m.EventID != "tg:10:55" {
		t.Fatalf("EventID = %q, want the first part's id", m.EventID)
	}
	if m.Text != "look at these" {
		t.Fatalf("Text = %q, want the album caption", m.Text)
	}
	if len(m.Attachments) != 2 || m.Attachments[0].ID != "AgA1" || m.Attachments[1].ID != "AgA2" {
		t.Fatalf("expected the two largest photos, got %+v", m.Attachments)
	}
	if m.Attachments[0].Kind != "photo" || m.Attachments[0].Size != 999 {
		t.Fatalf("expected largest size picked, got %+v", m.Attachments[0])
	}
	if m.Attachments[0].MIME != "image/jpeg" {
		t.Fatalf("photo MIME = %q, want image/jpeg", m.Attachments[0].MIME)
	}
}

func TestPollFlushesAlbumBeforeLaterText(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	photo := func(updateID, messageID int64, fileID string) apiUpdate {
		return apiUpdate{UpdateID: updateID, Message: &apiMessage{
			MessageID:    messageID,
			Date:         1752900000,
			From:         &apiUser{ID: 111, FirstName: "Léa"},
			Chat:         apiChat{ID: 10, Type: "private"},
			Photo:        []apiPhotoSize{{FileID: fileID, FileSize: 999}},
			MediaGroupID: "album-1",
		}}
	}
	// A text follows the album parts within the same batch: the album must
	// publish first or the user's turns swap order.
	f.pushBatch(photo(1, 55, "AgA1"), photo(2, 56, "AgA2"),
		textUpdate(3, 10, 57, "what do you think of these?"))

	rec := &recorder{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- adapter.Poll(ctx, rec.publish) }()
	<-f.drained
	cancel()
	<-done

	messages := rec.snapshot()
	if len(messages) != 2 {
		t.Fatalf("expected album then text, got %d messages", len(messages))
	}
	if len(messages[0].Attachments) != 2 || messages[0].EventID != "tg:10:55" {
		t.Fatalf("first message must be the merged album, got %+v", messages[0])
	}
	if messages[1].Text != "what do you think of these?" {
		t.Fatalf("second message must be the later text, got %+v", messages[1])
	}
}

func TestNormalizeGroupGating(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f) // BotUsername pre-seeded: no getMe needed
	ctx := context.Background()
	group := apiChat{ID: -100123, Type: "supergroup"}
	from := &apiUser{ID: 111, FirstName: "Léa"}
	botReply := &apiMessage{MessageID: 9, From: &apiUser{ID: 42, IsBot: true, Username: "pigobot"}}

	tests := []struct {
		name     string
		msg      *apiMessage
		wantOK   bool
		wantText string
	}{
		{
			name:   "unaddressed group chatter is dropped",
			msg:    &apiMessage{MessageID: 1, From: from, Chat: group, Text: "morning all"},
			wantOK: false,
		},
		{
			name: "mention triggers and is stripped",
			msg: &apiMessage{MessageID: 2, From: from, Chat: group, Text: "@pigobot run the tests",
				Entities: []apiEntity{{Type: "mention", Offset: 0, Length: 8}}},
			wantOK: true, wantText: "run the tests",
		},
		{
			name: "mention after emoji uses UTF-16 offsets",
			msg: &apiMessage{MessageID: 3, From: from, Chat: group, Text: "😀 @pigobot hi",
				Entities: []apiEntity{{Type: "mention", Offset: 3, Length: 8}}},
			wantOK: true, wantText: "😀 hi",
		},
		{
			name: "command with bot suffix normalizes",
			msg: &apiMessage{MessageID: 4, From: from, Chat: group, Text: "/status@pigobot",
				Entities: []apiEntity{{Type: "bot_command", Offset: 0, Length: 15}}},
			wantOK: true, wantText: "/status",
		},
		{
			name: "command for another bot is dropped",
			msg: &apiMessage{MessageID: 5, From: from, Chat: group, Text: "/status@otherbot",
				Entities: []apiEntity{{Type: "bot_command", Offset: 0, Length: 16}}},
			wantOK: false,
		},
		{
			name: "mention of someone else is dropped",
			msg: &apiMessage{MessageID: 6, From: from, Chat: group, Text: "@lea ping",
				Entities: []apiEntity{{Type: "mention", Offset: 0, Length: 4}}},
			wantOK: false,
		},
		{
			name:   "reply to the bot triggers",
			msg:    &apiMessage{MessageID: 7, From: from, Chat: group, Text: "yes do it", ReplyTo: botReply},
			wantOK: true, wantText: "yes do it",
		},
		{
			name:   "DM always triggers",
			msg:    &apiMessage{MessageID: 8, From: from, Chat: apiChat{ID: 10, Type: "private"}, Text: "hello"},
			wantOK: true, wantText: "hello",
		},
		{
			name: "DM command with bot suffix normalizes",
			msg: &apiMessage{MessageID: 9, From: from, Chat: apiChat{ID: 10, Type: "private"}, Text: "/status@pigobot",
				Entities: []apiEntity{{Type: "bot_command", Offset: 0, Length: 15}}},
			wantOK: true, wantText: "/status",
		},
		{
			name:   "bot senders are dropped",
			msg:    &apiMessage{MessageID: 10, From: &apiUser{ID: 5, IsBot: true, Username: "spam"}, Chat: group, Text: "@pigobot hi"},
			wantOK: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, ok, err := adapter.normalizeMessage(ctx, tc.msg, attachmentsOf(tc.msg))
			if err != nil {
				t.Fatalf("normalizeMessage: %v", err)
			}
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && m.Text != tc.wantText {
				t.Fatalf("Text = %q, want %q", m.Text, tc.wantText)
			}
		})
	}
}

func TestNormalizeThreadAndReplyFields(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	msg := &apiMessage{
		MessageID:      60,
		Date:           1752900000,
		ThreadID:       12,
		IsTopicMessage: true,
		From:           &apiUser{ID: 111, FirstName: "Léa", LastName: "B", Username: "lea"},
		Chat:           apiChat{ID: -100123, Type: "supergroup"},
		Text:           "answer me @pigobot",
		Entities:       []apiEntity{{Type: "mention", Offset: 10, Length: 8}},
		ReplyTo:        &apiMessage{MessageID: 44, From: &apiUser{ID: 7}},
	}
	m, ok, err := adapter.normalizeMessage(context.Background(), msg, nil)
	if err != nil || !ok {
		t.Fatalf("normalizeMessage: ok=%v err=%v", ok, err)
	}
	if m.ThreadID != "12" {
		t.Fatalf("ThreadID = %q, want 12", m.ThreadID)
	}
	if m.ReplyToID != "tg:-100123:44" {
		t.Fatalf("ReplyToID = %q", m.ReplyToID)
	}
	if m.SenderName != "Léa B" || m.SenderID != "111" {
		t.Fatalf("sender = %q/%q", m.SenderID, m.SenderName)
	}
	if m.ChatType != "group" {
		t.Fatalf("ChatType = %q, want group", m.ChatType)
	}
	if m.Text != "answer me" {
		t.Fatalf("Text = %q", m.Text)
	}
	if !m.SentAt.Equal(time.Unix(1752900000, 0).UTC()) {
		t.Fatalf("SentAt = %v", m.SentAt)
	}
}

func TestDownloadViaGetFile(t *testing.T) {
	f := newFakeAPI(t)
	adapter := newTestAdapter(t, f)
	f.filePaths["BQ1"] = "documents/file_12.pdf"
	f.files["documents/file_12.pdf"] = "PDFDATA"

	reader, mime, err := adapter.Download(context.Background(),
		chat.AttachmentRef{Kind: "document", ID: "BQ1", MIME: "application/pdf"})
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	defer func() { _ = reader.Close() }()
	content := make([]byte, 16)
	n, _ := reader.Read(content)
	if got := string(content[:n]); got != "PDFDATA" {
		t.Fatalf("content = %q", got)
	}
	if mime != "application/pdf" {
		t.Fatalf("mime = %q", mime)
	}
}
