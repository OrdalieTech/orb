package googlechat

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// messageEvent is a representative SPACE mention event.
const messageEvent = `{
  "type": "MESSAGE",
  "eventTime": "2026-07-19T12:00:00.000000Z",
  "message": {
    "name": "spaces/AAAA/messages/BBBB.CCCC",
    "sender": {"name": "users/1234567890", "displayName": "Léa", "type": "HUMAN"},
    "createTime": "2026-07-19T11:59:59Z",
    "text": "@pi summarize this",
    "argumentText": " summarize this",
    "thread": {"name": "spaces/AAAA/threads/DDDD"},
    "space": {"name": "spaces/AAAA", "type": "ROOM", "spaceType": "SPACE"},
    "attachment": [
      {"name": "spaces/AAAA/messages/BBBB.CCCC/attachments/0",
       "contentName": "report.pdf", "contentType": "application/pdf",
       "attachmentDataRef": {"resourceName": "uploaded/resource/0"},
       "source": "UPLOADED_CONTENT"},
      {"name": "spaces/AAAA/messages/BBBB.CCCC/attachments/1",
       "contentName": "notes.gdoc", "contentType": "application/vnd.google-apps.document",
       "source": "DRIVE_FILE"}
    ]
  },
  "space": {"name": "spaces/AAAA", "spaceType": "SPACE"},
  "user": {"name": "users/1234567890"}
}`

// postEvent POSTs body to the webhook with the given bearer token.
func postEvent(t *testing.T, handler http.Handler, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/chat/events", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWebhookNormalizesSpaceMention(t *testing.T) {
	env := newTestEnv(t)
	var published []chat.Message
	handler := env.adapter.Webhook(func(m chat.Message) error {
		published = append(published, m)
		return nil
	})
	rec := postEvent(t, handler, inboundJWT(t, nil), messageEvent)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if body, _ := io.ReadAll(rec.Result().Body); len(body) != 0 {
		t.Fatalf("ack body %q, want empty (sync reply must never be used)", body)
	}
	if len(published) != 1 {
		t.Fatalf("published %d messages, want 1", len(published))
	}
	got := published[0]
	want := chat.Message{
		EventID:    "spaces/AAAA/messages/BBBB.CCCC",
		Platform:   "googlechat",
		Account:    testProjectNumber,
		ChatID:     "spaces/AAAA",
		ThreadID:   "spaces/AAAA/threads/DDDD",
		ChatType:   "group",
		SenderID:   "users/1234567890",
		SenderName: "Léa",
		Text:       "summarize this\n[Drive attachment: notes.gdoc]",
		Attachments: []chat.AttachmentRef{{
			Kind: "document",
			ID:   "uploaded/resource/0",
			Name: "report.pdf",
			MIME: "application/pdf",
		}},
		SentAt: time.Date(2026, 7, 19, 11, 59, 59, 0, time.UTC),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized message mismatch\n got: %+v\nwant: %+v", got, want)
	}
}

func TestWebhookNormalizesDM(t *testing.T) {
	env := newTestEnv(t)
	var published []chat.Message
	handler := env.adapter.Webhook(func(m chat.Message) error {
		published = append(published, m)
		return nil
	})
	event := `{
	  "type": "MESSAGE",
	  "message": {
	    "name": "spaces/DM1/messages/M1",
	    "sender": {"name": "users/7", "displayName": "Bo", "type": "HUMAN"},
	    "text": "/status",
	    "thread": {"name": "spaces/DM1/threads/T1"},
	    "space": {"name": "spaces/DM1", "spaceType": "DIRECT_MESSAGE"}
	  }
	}`
	if rec := postEvent(t, handler, inboundJWT(t, nil), event); rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if len(published) != 1 {
		t.Fatalf("published %d, want 1", len(published))
	}
	if published[0].ChatType != "dm" {
		t.Fatalf("ChatType %q, want dm", published[0].ChatType)
	}
	if published[0].Text != "/status" {
		t.Fatalf("Text %q, want /status (text used when argumentText absent)", published[0].Text)
	}
}

func TestWebhookAuth(t *testing.T) {
	env := newTestEnv(t)
	published := 0
	handler := env.adapter.Webhook(func(chat.Message) error {
		published++
		return nil
	})
	cases := []struct {
		name  string
		token string
	}{
		{"missing bearer", ""},
		{"garbage token", "not-a-jwt"},
		{"wrong audience", inboundJWT(t, map[string]any{"aud": "other-project"})},
		{"wrong issuer", inboundJWT(t, map[string]any{"iss": "attacker@example.com"})},
		{"expired", inboundJWT(t, map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postEvent(t, handler, tc.token, messageEvent)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status %d, want 401", rec.Code)
			}
		})
	}
	if published != 0 {
		t.Fatalf("published %d messages from unauthenticated requests", published)
	}
}

func TestWebhookIgnoresNonMessageAndBotEvents(t *testing.T) {
	env := newTestEnv(t)
	published := 0
	handler := env.adapter.Webhook(func(chat.Message) error {
		published++
		return nil
	})
	events := []string{
		`{"type": "ADDED_TO_SPACE", "space": {"name": "spaces/AAAA"}}`,
		`{"type": "REMOVED_FROM_SPACE", "space": {"name": "spaces/AAAA"}}`,
		`{"type": "CARD_CLICKED"}`,
		// Bot senders are dropped before publish: the app must never answer
		// itself or another bot.
		`{"type": "MESSAGE", "message": {
		   "name": "spaces/AAAA/messages/BOT1",
		   "sender": {"name": "users/app", "type": "BOT"},
		   "text": "echo",
		   "space": {"name": "spaces/AAAA", "spaceType": "SPACE"}}}`,
		// Nothing deliverable.
		`{"type": "MESSAGE", "message": {
		   "name": "spaces/AAAA/messages/EMPTY",
		   "sender": {"name": "users/1", "type": "HUMAN"},
		   "space": {"name": "spaces/AAAA", "spaceType": "SPACE"}}}`,
	}
	for _, event := range events {
		if rec := postEvent(t, handler, inboundJWT(t, nil), event); rec.Code != http.StatusOK {
			t.Fatalf("status %d for %s, want 200", rec.Code, event)
		}
	}
	if published != 0 {
		t.Fatalf("published %d messages, want 0", published)
	}
}

func TestWebhookPublishFailureAnswers500(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(chat.Message) error {
		return io.ErrUnexpectedEOF
	})
	rec := postEvent(t, handler, inboundJWT(t, nil), messageEvent)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 so Google redelivers", rec.Code)
	}
}

func TestWebhookRejectsNonPost(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(chat.Message) error { return nil })
	req := httptest.NewRequest(http.MethodGet, "/chat/events", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}
