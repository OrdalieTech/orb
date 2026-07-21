package teams

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

func TestIngressNormalizesPersonalMessage(t *testing.T) {
	env := newTestEnv(t)
	var got []chat.Message
	handler := env.adapter.Webhook(func(m chat.Message) error { got = append(got, m); return nil })
	serviceURL := env.connector.server.URL

	activity := personalActivity(serviceURL)
	activity["attachments"] = []any{
		map[string]any{"contentType": "text/html", "content": "<div>hello bot</div>"},
		map[string]any{"contentType": "image/png", "contentUrl": serviceURL + "/file/pic.png", "name": "pic.png"},
		map[string]any{"contentType": "application/pdf", "contentUrl": serviceURL + "/file/doc.pdf", "name": "doc.pdf"},
		map[string]any{"contentType": "application/vnd.microsoft.teams.file.download.info", "content": map[string]any{"downloadUrl": "https://sharepoint.example/x"}},
	}
	if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	want := chat.Message{
		EventID:    "1481567603816",
		Tenant:     "tenant-1",
		Platform:   "teams",
		Account:    testAppID,
		ChatID:     "a:1personal",
		ChatType:   "dm",
		SenderID:   "29:1abc",
		SenderName: "Jane Smith",
		Text:       "hello bot",
		Attachments: []chat.AttachmentRef{
			{Kind: "photo", ID: serviceURL + "/file/pic.png", Name: "pic.png", MIME: "image/png"},
			{Kind: "document", ID: serviceURL + "/file/doc.pdf", Name: "doc.pdf", MIME: "application/pdf"},
		},
		SentAt: time.Date(2026, 7, 19, 10, 0, 0, 0, time.UTC),
	}
	if len(got) != 1 {
		t.Fatalf("published %d messages, want 1", len(got))
	}
	if !got[0].SentAt.Equal(want.SentAt) {
		t.Fatalf("SentAt = %v, want %v", got[0].SentAt, want.SentAt)
	}
	got[0].SentAt = want.SentAt // normalized: internal representations differ
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("message mismatch\n got: %+v\nwant: %+v", got[0], want)
	}
	// The conversation store learned the validated serviceUrl.
	info, ok := env.adapter.conversation("a:1personal")
	if !ok || info.serviceURL != serviceURL {
		t.Fatalf("conversation store = %+v, %v", info, ok)
	}
}

func TestIngressNormalizesChannelMention(t *testing.T) {
	env := newTestEnv(t)
	var got []chat.Message
	handler := env.adapter.Webhook(func(m chat.Message) error { got = append(got, m); return nil })
	serviceURL := env.connector.server.URL

	if recorder := postActivity(t, handler, channelActivity(serviceURL), env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	want := chat.Message{
		EventID:  "1481567603999",
		Tenant:   "tenant-1",
		Platform: "teams",
		Account:  testAppID,
		// Verbatim, ;messageid= suffix included: that suffix IS the thread.
		ChatID:     "19:chan@thread.tacv2;messageid=1481567603816",
		ThreadID:   "1481567603816",
		ChatType:   "group",
		SenderID:   "29:2def",
		SenderName: "Bob",
		Text:       "do the thing",
		ReplyToID:  "1481567603816",
		SentAt:     time.Date(2026, 7, 19, 10, 5, 0, 0, time.UTC),
	}
	if len(got) != 1 {
		t.Fatalf("published %d messages, want 1", len(got))
	}
	if !got[0].SentAt.Equal(want.SentAt) {
		t.Fatalf("SentAt = %v, want %v", got[0].SentAt, want.SentAt)
	}
	got[0].SentAt = want.SentAt // normalized: internal representations differ
	if !reflect.DeepEqual(got[0], want) {
		t.Fatalf("message mismatch\n got: %+v\nwant: %+v", got[0], want)
	}
	if info, ok := env.adapter.conversation(want.ChatID); !ok || info.serviceURL != serviceURL {
		t.Fatalf("conversation store = %+v, %v", info, ok)
	}
}

func TestIngressMentionFallbackMarkup(t *testing.T) {
	env := newTestEnv(t)
	var got []chat.Message
	handler := env.adapter.Webhook(func(m chat.Message) error { got = append(got, m); return nil })
	serviceURL := env.connector.server.URL

	activity := channelActivity(serviceURL)
	// Entity identifies the bot but carries no text: the leading <at>
	// markup is stripped by the fallback.
	activity["entities"] = []any{map[string]any{
		"type":      "mention",
		"mentioned": map[string]any{"id": "28:" + testAppID, "name": "botname"},
	}}
	activity["text"] = "<at>renamed bot</at> run it"
	if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(got) != 1 || got[0].Text != "run it" {
		t.Fatalf("got %+v, want one message with text %q", got, "run it")
	}
}

func TestIngressDrops(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(m chat.Message) error {
		t.Errorf("unexpected publish: %+v", m)
		return nil
	})
	serviceURL := env.connector.server.URL

	t.Run("unaddressed group message", func(t *testing.T) {
		activity := channelActivity(serviceURL)
		activity["text"] = "no mention here"
		delete(activity, "entities")
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
	t.Run("mention of another user only", func(t *testing.T) {
		activity := channelActivity(serviceURL)
		activity["entities"] = []any{map[string]any{
			"type":      "mention",
			"text":      "<at>Alice</at>",
			"mentioned": map[string]any{"id": "29:alice", "name": "Alice"},
		}}
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
	t.Run("bot echo", func(t *testing.T) {
		activity := personalActivity(serviceURL)
		activity["from"] = map[string]any{"id": "28:" + testAppID, "name": "botname"}
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
	t.Run("other bot", func(t *testing.T) {
		activity := personalActivity(serviceURL)
		activity["from"] = map[string]any{"id": "28:other-bot", "name": "otherbot"}
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
	t.Run("empty message", func(t *testing.T) {
		activity := personalActivity(serviceURL)
		activity["text"] = "   "
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
	t.Run("typing activity", func(t *testing.T) {
		activity := personalActivity(serviceURL)
		activity["type"] = "typing"
		if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
}

func TestIngressConversationUpdateStoresServiceURL(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(m chat.Message) error {
		t.Errorf("unexpected publish: %+v", m)
		return nil
	})
	serviceURL := env.connector.server.URL
	activity := personalActivity(serviceURL)
	activity["type"] = "conversationUpdate"
	delete(activity, "text")
	activity["membersAdded"] = []any{map[string]any{"id": "28:" + testAppID}}
	if recorder := postActivity(t, handler, activity, env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if info, ok := env.adapter.conversation("a:1personal"); !ok || info.serviceURL != serviceURL {
		t.Fatalf("conversation store = %+v, %v", info, ok)
	}
}

func TestIngressPublishFailureAnswers500(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(chat.Message) error { return errors.New("spool full") })
	serviceURL := env.connector.server.URL
	recorder := postActivity(t, handler, personalActivity(serviceURL), env.bearer(t, serviceURL, nil))
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", recorder.Code)
	}
}

func TestIngressRejectsNonPOST(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(chat.Message) error { return nil })
	request := httptest.NewRequest(http.MethodGet, "/api/messages", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", recorder.Code)
	}
}

func TestIngressDuplicateEventIDStable(t *testing.T) {
	// Redelivered activities keep the same EventID — the ledger's dedupe
	// key — across deliveries.
	env := newTestEnv(t)
	var ids []string
	handler := env.adapter.Webhook(func(m chat.Message) error { ids = append(ids, m.EventID); return nil })
	serviceURL := env.connector.server.URL
	for range 2 {
		if recorder := postActivity(t, handler, personalActivity(serviceURL), env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != "1481567603816" {
		t.Fatalf("event ids = %v, want two identical activity ids", ids)
	}
}
