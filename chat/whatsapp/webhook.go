package whatsapp

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

// maxWebhookBody bounds inbound webhook bodies.
const maxWebhookBody = 5 << 20

// Status is one entry of the statuses[] webhook array. The lifecycle per
// message id is sent → delivered → read (or the terminal failed), but
// callbacks arrive out of order: consumers must keep the highest
// [StatusRank] seen per MessageID instead of trusting arrival order.
type Status struct {
	// MessageID is the wamid of the outbound message the status refers to.
	MessageID string `json:"id"`
	// Status is "sent", "delivered", "read", or "failed".
	Status string `json:"status"`
	// Timestamp is epoch seconds, as a string (all WhatsApp timestamps are).
	Timestamp string `json:"timestamp"`
	// RecipientID is the recipient wa_id.
	RecipientID string `json:"recipient_id"`
	// Errors is populated on "failed" statuses.
	Errors []GraphError `json:"errors,omitempty"`
}

// StatusRank orders status values so out-of-order callbacks can be reduced
// to the furthest-progressed state: read > delivered > sent, with the
// terminal "failed" above all and unknown values at 0.
func StatusRank(status string) int {
	switch status {
	case "sent":
		return 1
	case "delivered":
		return 2
	case "read":
		return 3
	case "failed":
		return 4
	}
	return 0
}

// webhookPayload is the Cloud API webhook envelope subset the gateway reads.
type webhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID      string `json:"id"`
		Changes []struct {
			Field string      `json:"field"`
			Value changeValue `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type changeValue struct {
	Metadata struct {
		DisplayPhoneNumber string `json:"display_phone_number"`
		PhoneNumberID      string `json:"phone_number_id"`
	} `json:"metadata"`
	Contacts []struct {
		Profile struct {
			Name string `json:"name"`
		} `json:"profile"`
		WaID string `json:"wa_id"`
	} `json:"contacts"`
	Messages []inboundMessage `json:"messages"`
	Statuses []Status         `json:"statuses"`
}

// inboundMedia covers every media message variant; unused fields stay zero.
type inboundMedia struct {
	ID       string `json:"id"`
	MimeType string `json:"mime_type"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
	Voice    bool   `json:"voice"`
}

type inboundMessage struct {
	From      string `json:"from"`
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
	Text      *struct {
		Body string `json:"body"`
	} `json:"text"`
	Image    *inboundMedia `json:"image"`
	Video    *inboundMedia `json:"video"`
	Sticker  *inboundMedia `json:"sticker"`
	Document *inboundMedia `json:"document"`
	Audio    *inboundMedia `json:"audio"`
	Context  *struct {
		From string `json:"from"`
		ID   string `json:"id"`
	} `json:"context"`
}

// Webhook returns the HTTP handler for the Cloud API webhook endpoint. GET
// answers the one-time subscribe handshake; POST verifies the
// X-Hub-Signature-256 HMAC over the raw body BEFORE parsing, publishes every
// normalized message, and forwards statuses[] to Options.OnStatus. A publish
// error yields a 500 so Meta redelivers (the wamid EventID dedupes
// downstream).
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			a.handleVerify(w, r)
		case http.MethodPost:
			a.handleEvent(w, r, publish)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// handleVerify answers the subscribe handshake: echo the raw hub.challenge
// iff hub.mode is "subscribe" and hub.verify_token matches (constant time).
func (a *Adapter) handleVerify(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	mode := query.Get("hub.mode")
	token := query.Get("hub.verify_token")
	if mode != "subscribe" || !hmac.Equal([]byte(token), []byte(a.opts.VerifyToken)) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(query.Get("hub.challenge")))
}

// validSignature checks X-Hub-Signature-256 = "sha256=" + hex(HMAC-SHA256(
// raw body, app secret)) in constant time.
func (a *Adapter) validSignature(header string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(a.opts.AppSecret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(header[len(prefix):]))
}

func (a *Adapter) handleEvent(w http.ResponseWriter, r *http.Request, publish func(chat.Message) error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if !a.validSignature(r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			for _, status := range change.Value.Statuses {
				if a.opts.OnStatus != nil {
					a.opts.OnStatus(status)
				}
			}
			for i := range change.Value.Messages {
				msg, ok := a.normalize(&change.Value, &change.Value.Messages[i])
				if !ok {
					continue
				}
				if err := publish(msg); err != nil {
					http.Error(w, "publish failed", http.StatusInternalServerError)
					return
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

// normalize converts one inbound Cloud API message to a chat.Message. The
// EventID is the wamid — Meta redelivers, so it is the dedupe key. It
// returns ok=false for messages with nothing to deliver (e.g.
// type "unsupported").
func (a *Adapter) normalize(value *changeValue, m *inboundMessage) (chat.Message, bool) {
	msg := chat.Message{
		EventID:  m.ID,
		Platform: platformName,
		Account:  value.Metadata.PhoneNumberID,
		ChatID:   m.From,
		ChatType: "dm", // v1: only direct messages are supported
		SenderID: m.From,
	}
	if msg.Account == "" {
		msg.Account = a.opts.PhoneNumberID
	}
	for _, contact := range value.Contacts {
		if contact.WaID == m.From {
			msg.SenderName = contact.Profile.Name
			break
		}
	}
	if seconds, err := strconv.ParseInt(m.Timestamp, 10, 64); err == nil {
		msg.SentAt = time.Unix(seconds, 0).UTC()
	}
	if m.Context != nil {
		msg.ReplyToID = m.Context.ID
	}

	attach := func(kind string, media *inboundMedia) {
		msg.Attachments = append(msg.Attachments, chat.AttachmentRef{
			Kind: kind,
			ID:   media.ID,
			Name: media.Filename,
			MIME: media.MimeType,
		})
		if media.Caption != "" {
			msg.Text = media.Caption
		}
	}
	switch {
	case m.Text != nil:
		msg.Text = m.Text.Body
	case m.Image != nil:
		attach("photo", m.Image)
	case m.Sticker != nil:
		attach("photo", m.Sticker)
	case m.Video != nil:
		attach("video", m.Video)
	case m.Document != nil:
		attach("document", m.Document)
	case m.Audio != nil:
		if m.Audio.Voice {
			attach("voice", m.Audio)
		} else {
			attach("audio", m.Audio)
		}
	default:
		// "unsupported" and unknown types carry nothing deliverable.
		return chat.Message{}, false
	}
	return msg, true
}
