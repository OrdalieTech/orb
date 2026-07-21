package messenger

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
	"github.com/OrdalieTech/pi-go/chat/internal/graphhook"
)

const maxWebhookBody = 5 << 20

// Watermark is one delivery/read receipt from the message_deliveries or
// message_reads webhook fields. Messenger receipts are watermarks, not
// per-message state: every message the page sent before the watermark
// timestamp is delivered (or read). Treat them as telemetry only.
type Watermark struct {
	// Kind is "delivery" or "read".
	Kind string
	// PageID is the page the receipt belongs to.
	PageID string
	// PSID is the page-scoped id of the user who received/read.
	PSID string
	// Watermark is the epoch-millisecond cutoff: everything sent before it
	// is delivered/read.
	Watermark int64
	// MIDs lists the delivered message ids; delivery-only and often absent.
	MIDs []string
}

type webhookPayload struct {
	Object string `json:"object"`
	Entry  []struct {
		ID        string           `json:"id"`
		Messaging []messagingEvent `json:"messaging"`
	} `json:"entry"`
}

type messagingEvent struct {
	Sender struct {
		ID string `json:"id"`
	} `json:"sender"`
	Timestamp int64           `json:"timestamp"` // epoch milliseconds
	Message   *inboundMessage `json:"message"`
	Postback  *struct {
		MID     string `json:"mid"`
		Title   string `json:"title"`
		Payload string `json:"payload"`
	} `json:"postback"`
	Delivery *struct {
		MIDs      []string `json:"mids"`
		Watermark int64    `json:"watermark"`
	} `json:"delivery"`
	Read *struct {
		Watermark int64 `json:"watermark"`
	} `json:"read"`
}

type inboundMessage struct {
	MID        string `json:"mid"`
	Text       string `json:"text"`
	IsEcho     bool   `json:"is_echo"`
	QuickReply *struct {
		Payload string `json:"payload"`
	} `json:"quick_reply"`
	ReplyTo *struct {
		MID string `json:"mid"`
	} `json:"reply_to"`
	Attachments []struct {
		Type    string `json:"type"`
		Payload struct {
			URL string `json:"url"`
		} `json:"payload"`
	} `json:"attachments"`
}

// Webhook returns the HTTP handler for the Messenger webhook endpoint. GET
// answers the one-time subscribe handshake; POST verifies the
// X-Hub-Signature-256 HMAC over the raw body BEFORE parsing (Meta signs the
// exact bytes sent — never a re-serialization), walks entry[].messaging[],
// publishes every normalized message/postback, and forwards delivery/read
// watermarks to Options.OnWatermark. Echoes (message.is_echo — every message
// the page itself sends comes back as an event) are dropped before publish
// so the bot never answers itself. A publish error yields a 500 so Meta
// redelivers (the mid EventID dedupes downstream).
//
// The handler never waits on turn processing: publish must enqueue and
// return (Meta requires a 200 within ~5s and disables webhooks that keep
// failing).
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			graphhook.HandleVerify(w, r, a.opts.VerifyToken)
		case http.MethodPost:
			a.handleEvent(w, r, publish)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

func (a *Adapter) handleEvent(w http.ResponseWriter, r *http.Request, publish func(chat.Message) error) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	if !graphhook.ValidSignature(r.Header.Get("X-Hub-Signature-256"), body, a.opts.AppSecret) {
		http.Error(w, "invalid signature", http.StatusForbidden)
		return
	}
	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "malformed payload", http.StatusBadRequest)
		return
	}
	if payload.Object != "page" {
		w.WriteHeader(http.StatusOK)
		return
	}
	for _, entry := range payload.Entry {
		pageID := entry.ID
		if pageID == "" {
			pageID = a.opts.PageID
		}
		for i := range entry.Messaging {
			event := &entry.Messaging[i]
			if watermark, ok := watermarkFrom(pageID, event); ok {
				if a.opts.OnWatermark != nil {
					a.opts.OnWatermark(watermark)
				}
				continue
			}
			msg, ok := a.normalize(pageID, event)
			if !ok {
				continue
			}
			if err := publish(msg); err != nil {
				http.Error(w, "publish failed", http.StatusInternalServerError)
				return
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

func watermarkFrom(pageID string, event *messagingEvent) (Watermark, bool) {
	switch {
	case event.Delivery != nil:
		return Watermark{
			Kind:      "delivery",
			PageID:    pageID,
			PSID:      event.Sender.ID,
			Watermark: event.Delivery.Watermark,
			MIDs:      event.Delivery.MIDs,
		}, true
	case event.Read != nil:
		return Watermark{
			Kind:      "read",
			PageID:    pageID,
			PSID:      event.Sender.ID,
			Watermark: event.Read.Watermark,
		}, true
	}
	return Watermark{}, false
}

func (a *Adapter) normalize(pageID string, event *messagingEvent) (chat.Message, bool) {
	msg := chat.Message{
		Platform: platformName,
		Account:  pageID,
		ChatID:   event.Sender.ID,
		ChatType: "dm", // Messenger pages messaging is 1:1 only
		SenderID: event.Sender.ID,
		SentAt:   time.UnixMilli(event.Timestamp).UTC(),
	}
	switch {
	case event.Message != nil:
		m := event.Message
		if m.IsEcho {
			// Echo of a page-sent message (by this app, another app, or a
			// human in Page Inbox): dropping it here — before dedupe and
			// publish — is what prevents the bot from answering itself.
			return chat.Message{}, false
		}
		msg.EventID = m.MID
		msg.Text = m.Text
		if m.QuickReply != nil && m.QuickReply.Payload != "" {
			// A quick-reply tap carries both the button label as text and
			// the developer payload; the payload is the meaningful value.
			msg.Text = m.QuickReply.Payload
		}
		if m.ReplyTo != nil {
			msg.ReplyToID = m.ReplyTo.MID
		}
		for _, attachment := range m.Attachments {
			kind, ok := attachmentKind(attachment.Type)
			if !ok || attachment.Payload.URL == "" {
				// sticker/fallback/reel/... are non-file events; skip them.
				continue
			}
			msg.Attachments = append(msg.Attachments, chat.AttachmentRef{
				// payload.url is a direct, unauthenticated CDN URL that
				// expires: Download fetches it promptly, plain GET.
				Kind: kind,
				ID:   attachment.Payload.URL,
			})
		}
		if msg.Text == "" && len(msg.Attachments) == 0 {
			return chat.Message{}, false
		}
	case event.Postback != nil:
		pb := event.Postback
		msg.EventID = pb.MID
		if msg.EventID == "" {
			// Some postback variants omit the mid; synthesize a stable id so
			// redelivery still dedupes.
			msg.EventID = fmt.Sprintf("pb:%s:%d", event.Sender.ID, event.Timestamp)
		}
		msg.Text = pb.Payload
		if msg.Text == "" {
			msg.Text = pb.Title
		}
		if msg.Text == "" {
			return chat.Message{}, false
		}
	default:
		return chat.Message{}, false
	}
	return msg, true
}

func attachmentKind(attachmentType string) (string, bool) {
	switch attachmentType {
	case "image":
		return "photo", true
	case "video":
		return "video", true
	case "audio":
		return "audio", true
	case "file":
		return "document", true
	}
	return "", false
}
