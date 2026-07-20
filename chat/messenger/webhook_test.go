package messenger

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

func postEvent(handler http.Handler, body []byte, signature string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(string(body)))
	if signature != "" {
		req.Header.Set("X-Hub-Signature-256", signature)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
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

func TestWebhookSignatureRawBodyFidelity(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)

	// Unicode plus insignificant whitespace: any re-marshaling would change
	// the bytes and break the signature, so acceptance proves the HMAC runs
	// over the raw body (Meta signs the escaped-unicode payload it sends).
	body := []byte("{\n  \"object\":   \"page\",\n\t\"note\": \"héllo — ça va? 🦉\",\n  \"entry\": [ ]\n}")

	t.Run("valid signature over raw bytes accepted", func(t *testing.T) {
		rec := postEvent(adapter.Webhook(noPublish(t)), body, signBody("app-secret", body))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	})
	t.Run("signature of normalized body rejected", func(t *testing.T) {
		normalized := []byte(`{"object":"page","note":"héllo — ça va? 🦉","entry":[]}`)
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
		garbage := []byte("{not json")
		rec := postEvent(adapter.Webhook(noPublish(t)), garbage, "sha256=deadbeef")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rec.Code)
		}
	})
}

const inboundEventBody = `{"object":"page","entry":[{"id":"1906385232743851","time":1458692752478,"messaging":[
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692752478,
   "message":{"mid":"m_TEXT","text":"hello, world!","reply_to":{"mid":"m_PREV","is_self_reply":false}}},
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692753000,
   "message":{"mid":"m_QR","text":"Red","quick_reply":{"payload":"COLOR_RED"}}},
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692754000,
   "message":{"mid":"m_IMG","text":"look at this","attachments":[{"type":"image","payload":{"url":"https://cdn.example.com/img.jpg?exp=1"}}]}},
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692755000,
   "message":{"mid":"m_STICKER","attachments":[{"type":"sticker","payload":{"sticker_id":369239263222822}}]}},
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692755500,
   "message":{"mid":"m_LINK","attachments":[{"type":"fallback","payload":{"url":"https://shared.example.com/post"}}]}},
  {"sender":{"id":"1906385232743851"},"recipient":{"id":"PSID1"},"timestamp":1458692756000,
   "message":{"mid":"m_ECHO","is_echo":true,"app_id":1517776481860111,"text":"the bot said this"}},
  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692757000,
   "postback":{"mid":"m_PB","title":"Get Started","payload":"GET_STARTED"}}
]}]}`

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
	// text, quick-reply, image, postback published; sticker-only,
	// fallback-only, and the echo dropped.
	if len(published) != 4 {
		t.Fatalf("published %d messages, want 4: %+v", len(published), published)
	}
	for _, m := range published {
		if m.EventID == "m_ECHO" || m.Text == "the bot said this" {
			t.Fatalf("echo was published: %+v", m)
		}
	}

	text := published[0]
	if text.EventID != "m_TEXT" {
		t.Errorf("EventID = %q, want the mid", text.EventID)
	}
	if text.Platform != "messenger" || text.Account != "1906385232743851" || text.ChatID != "PSID1" {
		t.Errorf("routing fields = %q/%q/%q", text.Platform, text.Account, text.ChatID)
	}
	if text.ChatType != "dm" {
		t.Errorf("ChatType = %q, want dm (Messenger is 1:1 only)", text.ChatType)
	}
	if text.SenderID != "PSID1" {
		t.Errorf("SenderID = %q", text.SenderID)
	}
	if text.Text != "hello, world!" {
		t.Errorf("Text = %q", text.Text)
	}
	if text.ReplyToID != "m_PREV" {
		t.Errorf("ReplyToID = %q", text.ReplyToID)
	}
	if want := time.UnixMilli(1458692752478).UTC(); !text.SentAt.Equal(want) {
		t.Errorf("SentAt = %v, want %v", text.SentAt, want)
	}

	quick := published[1]
	if quick.EventID != "m_QR" {
		t.Errorf("quick-reply EventID = %q", quick.EventID)
	}
	if quick.Text != "COLOR_RED" {
		t.Errorf("quick-reply Text = %q, want the payload to win over the label", quick.Text)
	}

	image := published[2]
	if len(image.Attachments) != 1 {
		t.Fatalf("image attachments = %+v", image.Attachments)
	}
	if image.Attachments[0].Kind != "photo" || image.Attachments[0].ID != "https://cdn.example.com/img.jpg?exp=1" {
		t.Errorf("image attachment = %+v, want the direct CDN url as the ref id", image.Attachments[0])
	}
	if image.Text != "look at this" {
		t.Errorf("image Text = %q", image.Text)
	}

	postback := published[3]
	if postback.EventID != "m_PB" {
		t.Errorf("postback EventID = %q", postback.EventID)
	}
	if postback.Text != "GET_STARTED" {
		t.Errorf("postback Text = %q, want the payload", postback.Text)
	}
}

func TestWebhookPostbackWithoutMidGetsStableEventID(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	var published []chat.Message
	handler := adapter.Webhook(func(m chat.Message) error {
		published = append(published, m)
		return nil
	})
	body := []byte(`{"object":"page","entry":[{"id":"1906385232743851","messaging":[
	  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458692760000,
	   "postback":{"title":"Help","payload":"HELP"}}]}]}`)
	// Delivered twice: the synthesized EventID must be identical so the
	// ledger dedupes the redelivery.
	for i := 0; i < 2; i++ {
		if rec := postEvent(handler, body, signBody("app-secret", body)); rec.Code != http.StatusOK {
			t.Fatalf("status = %d", rec.Code)
		}
	}
	if len(published) != 2 {
		t.Fatalf("published %d messages", len(published))
	}
	if published[0].EventID == "" || published[0].EventID != published[1].EventID {
		t.Fatalf("EventIDs %q vs %q, want equal and non-empty", published[0].EventID, published[1].EventID)
	}
}

func TestWebhookWatermarks(t *testing.T) {
	var marks []Watermark
	adapter := newTestAdapter(t, "http://unused.invalid", func(wm Watermark) { marks = append(marks, wm) })
	handler := adapter.Webhook(noPublish(t))

	body := []byte(`{"object":"page","entry":[{"id":"1906385232743851","messaging":[
	  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458668856463,
	   "delivery":{"mids":["m_OUT1","m_OUT2"],"watermark":1458668856253}},
	  {"sender":{"id":"PSID1"},"recipient":{"id":"1906385232743851"},"timestamp":1458668857000,
	   "read":{"watermark":1458668856800}}]}]}`)
	rec := postEvent(handler, body, signBody("app-secret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(marks) != 2 {
		t.Fatalf("OnWatermark called %d times, want 2", len(marks))
	}
	delivery := marks[0]
	if delivery.Kind != "delivery" || delivery.PageID != "1906385232743851" || delivery.PSID != "PSID1" {
		t.Errorf("delivery watermark = %+v", delivery)
	}
	if delivery.Watermark != 1458668856253 || len(delivery.MIDs) != 2 || delivery.MIDs[0] != "m_OUT1" {
		t.Errorf("delivery watermark payload = %+v", delivery)
	}
	read := marks[1]
	if read.Kind != "read" || read.Watermark != 1458668856800 || read.MIDs != nil {
		t.Errorf("read watermark = %+v", read)
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

func TestWebhookIgnoresNonPageObjects(t *testing.T) {
	adapter := newTestAdapter(t, "http://unused.invalid", nil)
	handler := adapter.Webhook(noPublish(t))
	body := []byte(`{"object":"instagram","entry":[{"id":"X","messaging":[{"sender":{"id":"A"},"message":{"mid":"m_1","text":"hi"}}]}]}`)
	rec := postEvent(handler, body, signBody("app-secret", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (acknowledged, ignored)", rec.Code)
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
