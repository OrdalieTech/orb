package slack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

const (
	timestampHeader = "X-Slack-Request-Timestamp"
	signatureHeader = "X-Slack-Signature"
)

const maxEventBody = 1 << 20

type eventEnvelope struct {
	Type           string       `json:"type"`
	Challenge      string       `json:"challenge"`
	EventID        string       `json:"event_id"`
	Event          eventPayload `json:"event"`
	Authorizations []struct {
		UserID string `json:"user_id"`
		IsBot  bool   `json:"is_bot"`
	} `json:"authorizations"`
}

type eventPayload struct {
	Type        string      `json:"type"`
	Subtype     string      `json:"subtype"`
	Channel     string      `json:"channel"`
	ChannelType string      `json:"channel_type"`
	User        string      `json:"user"`
	BotID       string      `json:"bot_id"`
	Text        string      `json:"text"`
	TS          string      `json:"ts"`
	ThreadTS    string      `json:"thread_ts"`
	Files       []eventFile `json:"files"`
}

type eventFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Mimetype           string `json:"mimetype"`
	Size               int64  `json:"size"`
	URLPrivateDownload string `json:"url_private_download"`
}

// Webhook returns the Events API ingress handler. Every request — including
// url_verification — is authenticated by the v0 signature over the raw body
// (constant-time compare, replay window enforced), 401 on failure. Events
// are normalized and published synchronously, then acked: publish is a
// durable-enqueue seam and must never wait on turn processing (Slack
// requires a 2xx within 3 seconds). A publish failure answers 500 so Slack
// redelivers; the "sl:<channel>:<ts>" EventID dedupes downstream — the same
// key also collapses the app_mention/message.channels double delivery of one
// mention (their event_ids differ, channel+ts do not).
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		if !a.validSignature(r.Header.Get(timestampHeader), r.Header.Get(signatureHeader), body) {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		var envelope eventEnvelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		switch envelope.Type {
		case "url_verification":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte(envelope.Challenge))
		case "event_callback":
			msg, ok := a.normalize(&envelope)
			if ok {
				if err := publish(msg); err != nil {
					a.logger.Warn("slack: publish failed", "error", err)
					http.Error(w, "publish failed", http.StatusInternalServerError)
					return
				}
			}
			w.WriteHeader(http.StatusOK)
		default:
			// app_rate_limited and future envelope types: ack and move on.
			w.WriteHeader(http.StatusOK)
		}
	})
}

func (a *Adapter) validSignature(timestamp, signature string, body []byte) bool {
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return false
	}
	if age := a.now().Unix() - seconds; age > int64(a.replayWindow/time.Second) || -age > int64(a.replayWindow/time.Second) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.signingSecret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (a *Adapter) normalize(envelope *eventEnvelope) (chat.Message, bool) {
	event := &envelope.Event
	switch event.Type {
	case "message", "app_mention":
	default:
		return chat.Message{}, false
	}
	// Echo filtering: the bot's own chat.postMessage IS echoed back; answer
	// it and the conversation loops forever. Drop anything bot-authored.
	if event.BotID != "" || event.Subtype == "bot_message" || event.User == "" || event.User == a.botUserID {
		return chat.Message{}, false
	}
	for _, authorization := range envelope.Authorizations {
		if authorization.IsBot && event.User == authorization.UserID {
			return chat.Message{}, false
		}
	}
	switch event.Subtype {
	case "", "file_share", "thread_broadcast":
	default:
		// message_changed, message_deleted, channel_join, and every other
		// subtype carry no fresh user turn.
		return chat.Message{}, false
	}

	chatType := "group"
	if event.ChannelType == "im" {
		chatType = "dm"
	}
	// Group gating: DMs always trigger; channels (public, private, mpim)
	// only on an explicit mention. app_mention is a mention by definition;
	// a plain message event must carry the bot's mention token.
	// ponytail: no unmentioned thread-follow — every channel turn needs a
	// mention, even inside a thread the bot already answered in.
	if chatType == "group" && event.Type == "message" && !a.mentionRe.MatchString(event.Text) {
		return chat.Message{}, false
	}
	text := strings.TrimSpace(unescapeText(a.mentionRe.ReplaceAllString(event.Text, "")))

	var attachments []chat.AttachmentRef
	for _, file := range event.Files {
		if file.URLPrivateDownload == "" {
			continue
		}
		attachments = append(attachments, chat.AttachmentRef{
			Kind: fileKind(file.Mimetype),
			ID:   file.URLPrivateDownload,
			Name: file.Name,
			MIME: file.Mimetype,
			Size: file.Size,
		})
	}
	if text == "" && len(attachments) == 0 {
		return chat.Message{}, false
	}

	// Threading: DMs reply top-level (ThreadID only when the user is already
	// in a thread); channel turns are threaded onto the triggering message,
	// so a top-level mention starts a thread under its own ts.
	threadTS := event.ThreadTS
	if chatType == "group" && threadTS == "" {
		threadTS = event.TS
	}
	replyTo := ""
	if event.ThreadTS != "" && event.ThreadTS != event.TS {
		replyTo = eventID(event.Channel, event.ThreadTS)
	}
	return chat.Message{
		EventID:     eventID(event.Channel, event.TS),
		Platform:    platformName,
		Account:     a.botUserID,
		ChatID:      event.Channel,
		ThreadID:    threadTS,
		ChatType:    chatType,
		SenderID:    event.User,
		Text:        text,
		ReplyToID:   replyTo,
		Attachments: attachments,
		SentAt:      tsTime(event.TS),
	}, true
}

func eventID(channel, ts string) string {
	return "sl:" + channel + ":" + ts
}

func fileKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	default:
		return "document"
	}
}

var inboundUnescaper = strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&")

func unescapeText(text string) string {
	if !strings.Contains(text, "&") {
		return text
	}
	return inboundUnescaper.Replace(text)
}

func tsTime(ts string) time.Time {
	secondsPart, fraction, _ := strings.Cut(ts, ".")
	seconds, err := strconv.ParseInt(secondsPart, 10, 64)
	if err != nil {
		return time.Time{}
	}
	var nanos int64
	if fraction != "" {
		if micros, err := strconv.ParseInt((fraction + "000000")[:6], 10, 64); err == nil {
			nanos = micros * 1000
		}
	}
	return time.Unix(seconds, nanos).UTC()
}
