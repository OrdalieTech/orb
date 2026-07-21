package googlechat

// ingress.go handles the HTTPS interaction-event endpoint: bearer JWT
// verification, MESSAGE-event normalization, and publish-then-ack. Replies
// are never returned synchronously in the 200 body — a sync reply can never
// be edited afterwards, so the turn processor always answers via
// spaces.messages.create.

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

const maxEventBody = 5 << 20

type event struct {
	Type      string        `json:"type"`
	EventTime string        `json:"eventTime"`
	Message   *eventMessage `json:"message"`
	Space     *eventSpace   `json:"space"`
}

type eventMessage struct {
	Name         string       `json:"name"`
	Sender       *eventUser   `json:"sender"`
	CreateTime   string       `json:"createTime"`
	Text         string       `json:"text"`
	ArgumentText string       `json:"argumentText"`
	Thread       *apiThread   `json:"thread"`
	Space        *eventSpace  `json:"space"`
	Attachment   []attachment `json:"attachment"`
}

type eventUser struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Type        string `json:"type"`
}

type eventSpace struct {
	Name      string `json:"name"`
	SpaceType string `json:"spaceType"`
}

type attachment struct {
	ContentName       string `json:"contentName"`
	ContentType       string `json:"contentType"`
	Source            string `json:"source"`
	AttachmentDataRef *struct {
		ResourceName string `json:"resourceName"`
	} `json:"attachmentDataRef"`
}

// Webhook returns the interaction-event endpoint handler. Every POST is
// authenticated by its bearer JWT (401 on failure) before the body is
// trusted. MESSAGE events are normalized and published; every other event
// type is acknowledged and ignored. The success response is always an empty
// 200 — replies happen asynchronously. A publish failure answers 500 so
// Google redelivers (the message.name EventID dedupes downstream).
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := a.verifier.verify(r.Context(), token); err != nil {
			a.logger.Warn("googlechat: rejected inbound event", "error", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxEventBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		var ev event
		if err := json.Unmarshal(body, &ev); err != nil {
			http.Error(w, "malformed payload", http.StatusBadRequest)
			return
		}
		if ev.Type != "MESSAGE" || ev.Message == nil {
			// ADDED_TO_SPACE, REMOVED_FROM_SPACE, card clicks, ...:
			// acknowledge and ignore.
			w.WriteHeader(http.StatusOK)
			return
		}
		msg, ok := a.normalize(&ev)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := publish(msg); err != nil {
			a.logger.Warn("googlechat: publish failed", "error", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (a *Adapter) normalize(ev *event) (chat.Message, bool) {
	m := ev.Message
	if m.Name == "" {
		return chat.Message{}, false
	}
	if m.Sender != nil && m.Sender.Type != "" && m.Sender.Type != "HUMAN" {
		return chat.Message{}, false
	}
	space := m.Space
	if space == nil {
		space = ev.Space
	}
	if space == nil || space.Name == "" {
		return chat.Message{}, false
	}
	msg := chat.Message{
		EventID:  m.Name,
		Platform: platformName,
		Account:  a.projectNumber,
		ChatID:   space.Name,
		ChatType: chatType(space),
	}
	if m.Thread != nil {
		msg.ThreadID = m.Thread.Name
	}
	if m.Sender != nil {
		msg.SenderID = m.Sender.Name
		msg.SenderName = m.Sender.DisplayName
	}
	// argumentText is the body with the app mention stripped; prefer it
	// whenever present. In a SPACE the app only receives messages that
	// @mention it, so group gating is enforced by the platform itself; in a
	// DIRECT_MESSAGE everything arrives and text carries no mention.
	msg.Text = strings.TrimSpace(m.ArgumentText)
	if msg.Text == "" {
		msg.Text = strings.TrimSpace(m.Text)
	}
	msg.SentAt = eventSentAt(m.CreateTime, ev.EventTime)
	for _, att := range m.Attachment {
		if att.Source == "UPLOADED_CONTENT" && att.AttachmentDataRef != nil && att.AttachmentDataRef.ResourceName != "" {
			msg.Attachments = append(msg.Attachments, chat.AttachmentRef{
				Kind: attachmentKind(att.ContentType),
				ID:   att.AttachmentDataRef.ResourceName,
				Name: att.ContentName,
				MIME: att.ContentType,
			})
			continue
		}
		// Drive-sourced files are not downloadable with the chat.bot scope;
		// surface them as an honest textual note instead of a dead ref.
		if att.ContentName != "" {
			note := "[Drive attachment: " + att.ContentName + "]"
			if msg.Text != "" {
				msg.Text += "\n"
			}
			msg.Text += note
		}
	}
	if msg.Text == "" && len(msg.Attachments) == 0 {
		return chat.Message{}, false
	}
	return msg, true
}

func chatType(space *eventSpace) string {
	if space.SpaceType == "DIRECT_MESSAGE" {
		return "dm"
	}
	return "group"
}

func eventSentAt(createTime, eventTime string) time.Time {
	for _, stamp := range []string{createTime, eventTime} {
		if stamp == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339Nano, stamp); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func attachmentKind(mime string) string {
	switch {
	case strings.HasPrefix(mime, "image/"):
		return "photo"
	case strings.HasPrefix(mime, "video/"):
		return "video"
	case strings.HasPrefix(mime, "audio/"):
		return "audio"
	}
	return "document"
}
