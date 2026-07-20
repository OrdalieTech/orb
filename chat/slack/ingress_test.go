package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// fixedNow pins the adapter clock so signature timestamps are deterministic.
var fixedNow = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

func pinClock(adapter *Adapter) {
	adapter.now = func() time.Time { return fixedNow }
}

func TestWebhookRejectsBadSignatures(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	body := messageEvent(t, "message", map[string]any{"channel_type": "im", "channel": "D0DM"})
	goodTS := strconv.FormatInt(fixedNow.Unix(), 10)

	cases := []struct {
		name     string
		body     string
		mutate   func(r *http.Request)
		wantCode int
	}{
		{"valid", body, nil, http.StatusOK},
		{"missing headers", body, func(r *http.Request) {
			r.Header.Del(timestampHeader)
			r.Header.Del(signatureHeader)
		}, http.StatusUnauthorized},
		{"wrong signature", body, func(r *http.Request) {
			r.Header.Set(signatureHeader, "v0=deadbeef")
		}, http.StatusUnauthorized},
		{"tampered body signed for other content", body + " ", nil, http.StatusUnauthorized},
		{"non-numeric timestamp", body, func(r *http.Request) {
			r.Header.Set(timestampHeader, "yesterday")
		}, http.StatusUnauthorized},
		{"get method", body, func(r *http.Request) {
			r.Method = http.MethodGet
		}, http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(tc.body))
			ts, sig := sign([]byte(body), goodTS) // always sign the original body
			request.Header.Set(timestampHeader, ts)
			request.Header.Set(signatureHeader, sig)
			if tc.mutate != nil {
				tc.mutate(request)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantCode)
			}
		})
	}
	if len(*published) != 1 {
		t.Fatalf("published %d messages, want 1 (only the valid request)", len(*published))
	}
}

func TestWebhookReplayWindow(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, _ := capturePublish()
	handler := adapter.Webhook(publish)
	body := messageEvent(t, "message", map[string]any{"channel_type": "im", "channel": "D0DM"})

	cases := []struct {
		name     string
		offset   time.Duration
		wantCode int
	}{
		{"fresh", 0, http.StatusOK},
		{"old but inside window", -299 * time.Second, http.StatusOK},
		{"replayed past the window", -301 * time.Second, http.StatusUnauthorized},
		{"future past the window", 301 * time.Second, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
			ts, sig := sign([]byte(body), strconv.FormatInt(fixedNow.Add(tc.offset).Unix(), 10))
			request.Header.Set(timestampHeader, ts)
			request.Header.Set(signatureHeader, sig)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.wantCode)
			}
		})
	}
}

func TestWebhookChallenge(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	body := `{"type":"url_verification","challenge":"3eZbrw1aB1RIXpFjcW","token":"legacy"}`

	recorder := postEvent(t, adapter, handler, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if got := recorder.Body.String(); got != "3eZbrw1aB1RIXpFjcW" {
		t.Fatalf("challenge echo = %q", got)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("content-type = %q, want text/plain", got)
	}
	if len(*published) != 0 {
		t.Fatalf("challenge published %d messages", len(*published))
	}

	// An unsigned challenge must not be echoed: signature comes first.
	request := httptest.NewRequest(http.MethodPost, "/slack/events", strings.NewReader(body))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned challenge status = %d, want 401", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "3eZbrw1aB1RIXpFjcW") {
		t.Fatal("unsigned challenge was echoed")
	}
}

func TestWebhookEchoFiltering(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)

	cases := []struct {
		name      string
		overrides map[string]any
		want      bool
	}{
		{"normal user message", map[string]any{"channel_type": "im", "channel": "D0DM"}, true},
		{"bot_id set", map[string]any{"channel_type": "im", "channel": "D0DM", "bot_id": "B0BOT"}, false},
		{"subtype bot_message", map[string]any{"channel_type": "im", "channel": "D0DM", "subtype": "bot_message"}, false},
		{"own bot user", map[string]any{"channel_type": "im", "channel": "D0DM", "user": testBotUser}, false},
		{"no user", map[string]any{"channel_type": "im", "channel": "D0DM", "user": nil}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publish, published := capturePublish()
			handler := adapter.Webhook(publish)
			recorder := postEvent(t, adapter, handler, messageEvent(t, "message", tc.overrides))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if got := len(*published) == 1; got != tc.want {
				t.Fatalf("published = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWebhookAuthorizationsEchoFiltering(t *testing.T) {
	// A workspace where the cached botUserID differs from the event's
	// authorization: the authorizations[] bot user must still be dropped.
	adapter := newTestAdapter(t, newFakeAPI(t), func(o *Options) { o.BotUserID = "U0OTHER" })
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	body := messageEvent(t, "message", map[string]any{"channel_type": "im", "channel": "D0DM", "user": testBotUser})
	if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(*published) != 0 {
		t.Fatal("authorization-matched bot user was published")
	}
}

func TestWebhookSubtypePolicy(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)

	cases := []struct {
		subtype string
		want    bool
	}{
		{"", true},
		{"file_share", true},
		{"thread_broadcast", true},
		{"message_changed", false},
		{"message_deleted", false},
		{"channel_join", false},
	}
	for _, tc := range cases {
		name := tc.subtype
		if name == "" {
			name = "none"
		}
		t.Run(name, func(t *testing.T) {
			overrides := map[string]any{"channel_type": "im", "channel": "D0DM"}
			if tc.subtype != "" {
				overrides["subtype"] = tc.subtype
			}
			publish, published := capturePublish()
			handler := adapter.Webhook(publish)
			recorder := postEvent(t, adapter, handler, messageEvent(t, "message", overrides))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if got := len(*published) == 1; got != tc.want {
				t.Fatalf("published = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWebhookNormalizesDM(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	body := messageEvent(t, "message", map[string]any{
		"channel_type": "im",
		"channel":      "D0DM",
		"text":         "if a &lt; b &amp;&amp; b &gt; c",
	})
	if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(*published) != 1 {
		t.Fatalf("published %d messages, want 1", len(*published))
	}
	got := (*published)[0]
	want := chat.Message{
		EventID:  "sl:D0DM:1700000000.000100",
		Platform: "slack",
		Account:  testBotUser,
		ChatID:   "D0DM",
		ThreadID: "", // DMs reply top-level
		ChatType: "dm",
		SenderID: "U0USER",
		Text:     "if a < b && b > c",
		SentAt:   time.Unix(1700000000, 100000).UTC(),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("message = %+v\nwant      %+v", got, want)
	}
}

func TestWebhookChannelMentionGating(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)

	t.Run("unmentioned channel message dropped", func(t *testing.T) {
		publish, published := capturePublish()
		handler := adapter.Webhook(publish)
		body := messageEvent(t, "message", map[string]any{"channel_type": "channel", "text": "just chatting"})
		if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		if len(*published) != 0 {
			t.Fatal("unmentioned channel message was published")
		}
	})

	t.Run("mentioned channel message published and stripped", func(t *testing.T) {
		publish, published := capturePublish()
		handler := adapter.Webhook(publish)
		body := messageEvent(t, "message", map[string]any{
			"channel_type": "channel",
			"text":         fmt.Sprintf("<@%s> run the report", testBotUser),
		})
		if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		if len(*published) != 1 {
			t.Fatalf("published %d messages, want 1", len(*published))
		}
		got := (*published)[0]
		if got.Text != "run the report" {
			t.Fatalf("text = %q, want mention stripped", got.Text)
		}
		if got.ChatType != "group" {
			t.Fatalf("chatType = %q, want group", got.ChatType)
		}
		if got.ThreadID != "1700000000.000100" {
			t.Fatalf("threadID = %q, want the message's own ts (reply in-thread)", got.ThreadID)
		}
	})

	t.Run("app_mention shares the message event id", func(t *testing.T) {
		publish, published := capturePublish()
		handler := adapter.Webhook(publish)
		// app_mention has no channel_type and keeps the raw mention text.
		body := messageEvent(t, "app_mention", map[string]any{
			"channel_type": nil,
			"text":         fmt.Sprintf("<@%s> run the report", testBotUser),
		})
		if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		if len(*published) != 1 {
			t.Fatalf("published %d messages, want 1", len(*published))
		}
		got := (*published)[0]
		if got.EventID != "sl:C0CHAN:1700000000.000100" {
			t.Fatalf("eventID = %q — must dedupe against the message.channels delivery", got.EventID)
		}
		if got.Text != "run the report" {
			t.Fatalf("text = %q, want mention stripped", got.Text)
		}
	})

	t.Run("thread reply threads on thread_ts", func(t *testing.T) {
		publish, published := capturePublish()
		handler := adapter.Webhook(publish)
		body := messageEvent(t, "app_mention", map[string]any{
			"channel_type": nil,
			"text":         fmt.Sprintf("<@%s> and then?", testBotUser),
			"ts":           "1700000009.000900",
			"thread_ts":    "1700000000.000100",
		})
		if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
			t.Fatalf("status = %d", recorder.Code)
		}
		got := (*published)[0]
		if got.ThreadID != "1700000000.000100" {
			t.Fatalf("threadID = %q, want parent thread_ts", got.ThreadID)
		}
		if got.ReplyToID != "sl:C0CHAN:1700000000.000100" {
			t.Fatalf("replyToID = %q", got.ReplyToID)
		}
		if got.EventID != "sl:C0CHAN:1700000009.000900" {
			t.Fatalf("eventID = %q", got.EventID)
		}
	})
}

func TestWebhookFileShare(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	files := []map[string]any{
		{
			"id":                   "F0FILE",
			"name":                 "report.pdf",
			"mimetype":             "application/pdf",
			"size":                 1234,
			"url_private_download": "https://files.slack.com/files-pri/T0-F0/download/report.pdf",
		},
		{
			"id":                   "F0IMG",
			"name":                 "chart.png",
			"mimetype":             "image/png",
			"size":                 99,
			"url_private_download": "https://files.slack.com/files-pri/T0-F1/download/chart.png",
		},
	}
	body := messageEvent(t, "message", map[string]any{
		"channel_type": "im",
		"channel":      "D0DM",
		"subtype":      "file_share",
		"text":         "here you go",
		"files":        files,
	})
	if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if len(*published) != 1 {
		t.Fatalf("published %d messages, want 1", len(*published))
	}
	got := (*published)[0].Attachments
	want := []chat.AttachmentRef{
		{Kind: "document", ID: "https://files.slack.com/files-pri/T0-F0/download/report.pdf", Name: "report.pdf", MIME: "application/pdf", Size: 1234},
		{Kind: "photo", ID: "https://files.slack.com/files-pri/T0-F1/download/chart.png", Name: "chart.png", MIME: "image/png", Size: 99},
	}
	if len(got) != len(want) {
		t.Fatalf("attachments = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attachment %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWebhookPublishFailureAnswers500(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	handler := adapter.Webhook(func(chat.Message) error { return errors.New("spool full") })
	body := messageEvent(t, "message", map[string]any{"channel_type": "im", "channel": "D0DM"})
	if recorder := postEvent(t, adapter, handler, body); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 so Slack redelivers", recorder.Code)
	}
}

func TestWebhookIgnoresOtherEnvelopes(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	pinClock(adapter)
	publish, published := capturePublish()
	handler := adapter.Webhook(publish)
	if recorder := postEvent(t, adapter, handler, `{"type":"app_rate_limited","minute_rate_limited":1700000000}`); recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if len(*published) != 0 {
		t.Fatal("app_rate_limited published a message")
	}
}

func TestTSTime(t *testing.T) {
	if got := tsTime("1700000000.123456"); !got.Equal(time.Unix(1700000000, 123456000)) {
		t.Fatalf("tsTime = %v", got)
	}
	if got := tsTime("nonsense"); !got.IsZero() {
		t.Fatalf("tsTime(nonsense) = %v, want zero", got)
	}
}

// TestNormalizeDropsUnknownEventTypes exercises normalize directly for event
// types the webhook should never publish.
func TestNormalizeDropsUnknownEventTypes(t *testing.T) {
	adapter := newTestAdapter(t, newFakeAPI(t))
	var envelope eventEnvelope
	if err := json.Unmarshal([]byte(messageEvent(t, "reaction_added", nil)), &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := adapter.normalize(&envelope); ok {
		t.Fatal("reaction_added was normalized")
	}
}
