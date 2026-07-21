package teams

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

const maxWebhookBody = 5 << 20

const messageIDMarker = ";messageid="

// Markup is removable only after the entity identity matches recipient.id.
var reAtMention = regexp.MustCompile(`(?i)^\s*<at>.*?</at>`)

type inboundEntity struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Mentioned *channelAccount `json:"mentioned"`
}

type inboundAttachment struct {
	ContentType string `json:"contentType"`
	ContentURL  string `json:"contentUrl"`
	Name        string `json:"name"`
}

type inboundActivity struct {
	Type         string         `json:"type"`
	ID           string         `json:"id"`
	Timestamp    string         `json:"timestamp"`
	ServiceURL   string         `json:"serviceUrl"`
	ChannelID    string         `json:"channelId"`
	From         channelAccount `json:"from"`
	Recipient    channelAccount `json:"recipient"`
	Conversation struct {
		ID               string `json:"id"`
		ConversationType string `json:"conversationType"`
		TenantID         string `json:"tenantId"`
	} `json:"conversation"`
	Text        string              `json:"text"`
	ReplyToID   string              `json:"replyToId"`
	Entities    []inboundEntity     `json:"entities"`
	Attachments []inboundAttachment `json:"attachments"`
	ChannelData struct {
		Tenant struct {
			ID string `json:"id"`
		} `json:"tenant"`
	} `json:"channelData"`
}

// Webhook returns the Bot Framework ingress handler. Every POST is
// authenticated by full inbound JWT validation (403 on any failure) before
// its body is trusted; message activities are normalized and handed to
// publish, then the request is acknowledged — ingress never waits on turn
// processing (publish is the durable enqueue). typing activities are
// ignored; conversationUpdate and every other type just refresh the stored
// per-conversation serviceUrl. A publish failure answers 500 so the
// connector redelivers (the activity id EventID dedupes downstream).
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
		if err != nil {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}
		var activity inboundActivity
		if err := json.Unmarshal(body, &activity); err != nil {
			http.Error(w, "malformed activity", http.StatusBadRequest)
			return
		}
		if err := a.validator.validate(r.Context(), r.Header.Get("Authorization"), activity.ServiceURL, activity.ChannelID); err != nil {
			// The reason names only the failed check, never token material.
			a.logger.Warn("teams: inbound token rejected", "reason", err)
			w.WriteHeader(http.StatusForbidden)
			return
		}
		a.rememberConversation(activity.Conversation.ID, activity.ServiceURL)
		if activity.Type != "message" {
			// typing, conversationUpdate, messageReaction, invoke, ...:
			// nothing to publish; the serviceUrl refresh above is the value.
			w.WriteHeader(http.StatusOK)
			return
		}
		message, ok := a.normalize(&activity)
		if !ok {
			w.WriteHeader(http.StatusOK)
			return
		}
		if err := publish(message); err != nil {
			a.logger.Warn("teams: publish failed", "error", err)
			http.Error(w, "publish failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

func (a *Adapter) normalize(activity *inboundActivity) (chat.Message, bool) {
	from := activity.From.ID
	if from == "" || from == activity.Recipient.ID || strings.HasPrefix(from, "28:") {
		// The bot must never answer itself (or another bot: 28: ids).
		return chat.Message{}, false
	}
	chatType := "dm"
	group := false
	switch activity.Conversation.ConversationType {
	case "personal", "":
	default: // groupChat, channel
		chatType = "group"
		group = true
	}
	text, mentioned := stripMentions(activity.Text, activity.Entities, activity.Recipient.ID)
	if group && !mentioned {
		return chat.Message{}, false
	}
	attachments := attachmentsOf(activity.Attachments)
	if text == "" && len(attachments) == 0 {
		return chat.Message{}, false
	}
	tenant := activity.Conversation.TenantID
	if tenant == "" {
		tenant = activity.ChannelData.Tenant.ID
	}
	threadID := ""
	if i := strings.Index(activity.Conversation.ID, messageIDMarker); i >= 0 {
		threadID = activity.Conversation.ID[i+len(messageIDMarker):]
	}
	sentAt := time.Time{}
	if parsed, err := time.Parse(time.RFC3339, activity.Timestamp); err == nil {
		sentAt = parsed.UTC()
	}
	return chat.Message{
		EventID:  activity.ID,
		Tenant:   tenant,
		Platform: platformName,
		Account:  a.appID,
		// The conversation id is kept verbatim, ;messageid= suffix
		// included: in channels that suffix IS the thread.
		ChatID:      activity.Conversation.ID,
		ThreadID:    threadID,
		ChatType:    chatType,
		SenderID:    activity.From.ID,
		SenderName:  activity.From.Name,
		Text:        text,
		ReplyToID:   activity.ReplyToID,
		Attachments: attachments,
		SentAt:      sentAt,
	}, true
}

func stripMentions(text string, entities []inboundEntity, recipientID string) (string, bool) {
	mentioned := false
	needFallback := false
	for _, entity := range entities {
		if entity.Type != "mention" || entity.Mentioned == nil || entity.Mentioned.ID != recipientID {
			continue
		}
		mentioned = true
		if entity.Text != "" {
			text = strings.Replace(text, entity.Text, "", 1)
		} else {
			needFallback = true
		}
	}
	if needFallback {
		text = reAtMention.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text), mentioned
}

// attachmentsOf extracts attachment refs. The text/html attachment
// mirroring the message body is skipped, as is anything without a
// contentUrl.
//
// ponytail: attachments whose payload hides inside "content" (Teams file
// download info cards) are dropped; only direct contentUrls are carried.
func attachmentsOf(attachments []inboundAttachment) []chat.AttachmentRef {
	var refs []chat.AttachmentRef
	for _, attachment := range attachments {
		if attachment.ContentURL == "" || strings.HasPrefix(attachment.ContentType, "text/html") {
			continue
		}
		kind := "document"
		switch {
		case strings.HasPrefix(attachment.ContentType, "image/"):
			kind = "photo"
		case strings.HasPrefix(attachment.ContentType, "audio/"):
			kind = "audio"
		case strings.HasPrefix(attachment.ContentType, "video/"):
			kind = "video"
		}
		refs = append(refs, chat.AttachmentRef{
			Kind: kind,
			ID:   attachment.ContentURL,
			Name: attachment.Name,
			MIME: attachment.ContentType,
		})
	}
	return refs
}
