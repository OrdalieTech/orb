package whatsapp

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

func signBody(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func noPublish(t *testing.T) func(chat.Message) error {
	return func(m chat.Message) error {
		t.Errorf("unexpected publish: %+v", m)
		return nil
	}
}

func TestWebhookVerifyHandshake(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	handler := adapter.Webhook(noPublish(t))

	get := func(mode, token, challenge string) *httptest.ResponseRecorder {
		query := url.Values{}
		query.Set("hub.mode", mode)
		query.Set("hub.verify_token", token)
		query.Set("hub.challenge", challenge)
		req := httptest.NewRequest(http.MethodGet, "/webhook?"+query.Encode(), nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	t.Run("good handshake echoes raw challenge", func(t *testing.T) {
		rec := get("subscribe", "verify-token", "1158201444")
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
		if rec.Body.String() != "1158201444" {
			t.Fatalf("body = %q, want raw challenge", rec.Body.String())
		}
	})
	t.Run("wrong verify_token is 403", func(t *testing.T) {
		if rec := get("subscribe", "wrong", "123"); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("wrong mode is 403", func(t *testing.T) {
		if rec := get("unsubscribe", "verify-token", "123"); rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

func postEvent(handler http.Handler, body []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestWebhookSignatureRawBodyFidelity(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)

	// Unicode plus insignificant whitespace: any re-marshaling would change
	// the bytes and break the signature, so acceptance proves the HMAC runs
	// over the raw body.
	body := []byte("{\n  \"object\":   \"whatsapp_business_account\",\n\t\"note\": \"héllo — ça va? 🦉\",\n  \"entry\": [ ]\n}")

	t.Run("valid signature over raw bytes accepted", func(t *testing.T) {
		rec := postEvent(adapter.Webhook(noPublish(t)), body, signBody("app-secret", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("signature of normalized body rejected", func(t *testing.T) {
		normalized := []byte(`{"object":"whatsapp_business_account","note":"héllo — ça va? 🦉","entry":[]}`)
		rec := postEvent(adapter.Webhook(noPublish(t)), body, signBody("app-secret", normalized))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("wrong secret rejected", func(t *testing.T) {
		rec := postEvent(adapter.Webhook(noPublish(t)), body, signBody("other-secret", body))
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("missing header rejected", func(t *testing.T) {
		rec := postEvent(adapter.Webhook(noPublish(t)), body, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
	t.Run("rejected before parsing", func(t *testing.T) {
		// Invalid JSON with a bad signature must yield the signature 403,
		// not a parse 400: verification runs first.
		garbage := []byte("{not json")
		rec := postEvent(adapter.Webhook(noPublish(t)), garbage, "sha256=deadbeef")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

const inboundEventBody = `{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"messages","value":{
  "messaging_product":"whatsapp",
  "metadata":{"display_phone_number":"15550783881","phone_number_id":"106540352242922"},
  "contacts":[{"profile":{"name":"Sheena Nelson"},"wa_id":"16505551234"}],
  "messages":[
    {"from":"16505551234","id":"wamid.IN1","timestamp":"1749416383","type":"text",
     "text":{"body":"Does it come in another color?"},
     "context":{"from":"15550783881","id":"wamid.PREV"}},
    {"from":"16505551234","id":"wamid.IN2","timestamp":"1749416400","type":"image",
     "image":{"id":"MEDIA123","mime_type":"image/jpeg","sha256":"abc","caption":"look at this"}},
    {"from":"16505551234","id":"wamid.IN3","timestamp":"1749416410","type":"document",
     "document":{"id":"MEDIA456","mime_type":"application/pdf","filename":"report.pdf"}},
    {"from":"16505551234","id":"wamid.IN4","timestamp":"1749416420","type":"audio",
     "audio":{"id":"MEDIA789","mime_type":"audio/ogg","voice":true}},
    {"from":"16505551234","id":"wamid.IN5","timestamp":"1749416430","type":"unsupported"}
  ]
}}]},{"id":"WABA1","changes":[{"field":"account_update","value":{}}]}]}`

func TestWebhookMessageParsing(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	var published []chat.Message
	handler := adapter.Webhook(func(m chat.Message) error {
		published = append(published, m)
		return nil
	})

	body := []byte(inboundEventBody)
	rec := postEvent(handler, body, signBody("app-secret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(published) != 4 {
		t.Fatalf("published %d messages, want 4 (unsupported skipped): %+v", len(published), published)
	}

	text := published[0]
	if text.EventID != "wamid.IN1" {
		t.Errorf("EventID = %q, want the wamid", text.EventID)
	}
	if text.Platform != "whatsapp" || text.Account != "106540352242922" || text.ChatID != "16505551234" {
		t.Errorf("routing fields = %q/%q/%q", text.Platform, text.Account, text.ChatID)
	}
	if text.SenderID != "16505551234" || text.SenderName != "Sheena Nelson" {
		t.Errorf("sender = %q/%q", text.SenderID, text.SenderName)
	}
	if text.ChatType != "dm" {
		t.Errorf("ChatType = %q, want dm", text.ChatType)
	}
	if text.Text != "Does it come in another color?" {
		t.Errorf("Text = %q", text.Text)
	}
	if text.ReplyToID != "wamid.PREV" {
		t.Errorf("ReplyToID = %q", text.ReplyToID)
	}
	if want := time.Unix(1749416383, 0).UTC(); !text.SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", text.SentAt, want)
	}

	image := published[1]
	if len(image.Attachments) != 1 || image.Attachments[0].Kind != "photo" || image.Attachments[0].ID != "MEDIA123" || image.Attachments[0].MIME != "image/jpeg" {
		t.Errorf("image attachment = %+v", image.Attachments)
	}
	if image.Text != "look at this" {
		t.Errorf("caption not promoted to Text: %q", image.Text)
	}

	document := published[2]
	if len(document.Attachments) != 1 || document.Attachments[0].Kind != "document" || document.Attachments[0].Name != "report.pdf" {
		t.Errorf("document attachment = %+v", document.Attachments)
	}

	voice := published[3]
	if len(voice.Attachments) != 1 || voice.Attachments[0].Kind != "voice" {
		t.Errorf("voice attachment = %+v", voice.Attachments)
	}
}

func TestWebhookPublishErrorYields500(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	handler := adapter.Webhook(func(chat.Message) error { return errors.New("spool full") })
	body := []byte(inboundEventBody)
	rec := postEvent(handler, body, signBody("app-secret", body))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so Meta redelivers", rec.Code)
	}
}

func TestWebhookStatuses(t *testing.T) {
	var statuses []Status
	adapter := newTestAdapter(t, "http://unused.invalid", func(s Status) { statuses = append(statuses, s) })
	handler := adapter.Webhook(noPublish(t))

	body := []byte(`{"object":"whatsapp_business_account","entry":[{"id":"WABA1","changes":[{"field":"messages","value":{
	  "metadata":{"phone_number_id":"106540352242922"},
	  "statuses":[
	    {"id":"wamid.OUT1","status":"read","timestamp":"1750263780","recipient_id":"16505551234"},
	    {"id":"wamid.OUT1","status":"delivered","timestamp":"1750263773","recipient_id":"16505551234"},
	    {"id":"wamid.OUT2","status":"failed","timestamp":"1750263790","recipient_id":"16505551234",
	     "errors":[{"code":131047,"title":"Re-engagement message","message":"Re-engagement message","error_data":{"details":"Message failed to send because more than 24 hours have passed."}}]}
	  ]}}]}]}`)
	rec := postEvent(handler, body, signBody("app-secret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(statuses) != 3 {
		t.Fatalf("OnStatus called %d times, want 3", len(statuses))
	}
	if statuses[0].MessageID != "wamid.OUT1" || statuses[0].Status != "read" || statuses[0].Timestamp != "1750263780" {
		t.Errorf("first status = %+v", statuses[0])
	}
	// Out-of-order arrival: "read" came before "delivered" — the rank
	// reduction keeps read as the furthest-progressed state.
	if StatusRank(statuses[1].Status) >= StatusRank(statuses[0].Status) {
		t.Errorf("rank(delivered) must be below rank(read)")
	}
	failed := statuses[2]
	if failed.Status != "failed" || len(failed.Errors) != 1 || failed.Errors[0].Code != 131047 {
		t.Errorf("failed status = %+v", failed)
	}
	if failed.Errors[0].ErrorData.Details == "" {
		t.Errorf("error_data.details not decoded")
	}
}

func TestStatusRankOrdering(t *testing.T) {
	if !(StatusRank("read") > StatusRank("delivered") && StatusRank("delivered") > StatusRank("sent")) {
		t.Fatal("rank must order read > delivered > sent")
	}
	if StatusRank("sent") <= StatusRank("bogus") {
		t.Fatal("unknown statuses must rank below sent")
	}
	if StatusRank("failed") <= StatusRank("read") {
		t.Fatal("failed is terminal and ranks above read")
	}
}

func TestWebhookRejectsOtherMethods(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	req := httptest.NewRequest(http.MethodPut, "/webhook", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	adapter.Webhook(noPublish(t)).ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}
