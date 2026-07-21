package discord

import (
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

type gwUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

type gwMember struct {
	Nick string `json:"nick"`
}

type gwAttachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
}

type gwMessage struct {
	ID                string         `json:"id"`
	ChannelID         string         `json:"channel_id"`
	GuildID           string         `json:"guild_id"`
	Content           string         `json:"content"`
	Timestamp         string         `json:"timestamp"`
	Author            *gwUser        `json:"author"`
	Member            *gwMember      `json:"member"`
	Mentions          []gwUser       `json:"mentions"`
	ReferencedMessage *gwMessage     `json:"referenced_message"`
	Attachments       []gwAttachment `json:"attachments"`
}

func (a *Adapter) normalize(msg *gwMessage) (chat.Message, bool) {
	botID := a.identity()
	if msg.Author == nil || msg.Author.Bot || (botID != "" && msg.Author.ID == botID) {
		return chat.Message{}, false
	}
	chatType := "group"
	if msg.GuildID == "" {
		chatType = "dm"
	}
	text, addressed := stripBotMention(msg.Content, botID)
	if !addressed && botID != "" {
		for _, mention := range msg.Mentions {
			if mention.ID == botID {
				addressed = true
				break
			}
		}
		// A reply to the bot addresses it even when the ping is suppressed.
		if !addressed && msg.ReferencedMessage != nil &&
			msg.ReferencedMessage.Author != nil && msg.ReferencedMessage.Author.ID == botID {
			addressed = true
		}
	}
	if chatType == "group" && !addressed {
		return chat.Message{}, false
	}
	attachments := attachmentsOf(msg)
	if text == "" && len(attachments) == 0 {
		return chat.Message{}, false
	}

	senderName := msg.Author.Username
	if msg.Author.GlobalName != "" {
		senderName = msg.Author.GlobalName
	}
	if msg.Member != nil && msg.Member.Nick != "" {
		senderName = msg.Member.Nick
	}
	replyToID := ""
	if msg.ReferencedMessage != nil && msg.ReferencedMessage.ID != "" {
		replyToID = eventID(msg.ChannelID, msg.ReferencedMessage.ID)
	}
	sentAt, err := time.Parse(time.RFC3339, msg.Timestamp)
	if err != nil {
		sentAt = time.Now()
	}
	// ponytail: ThreadID stays empty — MESSAGE_CREATE carries no channel
	// type, and a thread's id IS its channel_id, so keying on ChatID alone
	// already isolates thread conversations.
	return chat.Message{
		EventID:     eventID(msg.ChannelID, msg.ID),
		Platform:    platformName,
		Account:     a.account,
		ChatID:      msg.ChannelID,
		ChatType:    chatType,
		SenderID:    msg.Author.ID,
		SenderName:  senderName,
		Text:        text,
		ReplyToID:   replyToID,
		Attachments: attachments,
		SentAt:      sentAt.UTC(),
	}, true
}

func eventID(channelID, messageID string) string {
	return "dc:" + channelID + ":" + messageID
}

func stripBotMention(text, botID string) (string, bool) {
	addressed := false
	if botID != "" {
		for _, token := range []string{"<@" + botID + ">", "<@!" + botID + ">"} {
			for {
				i := strings.Index(text, token)
				if i < 0 {
					break
				}
				text = cutAt(text, i, len(token))
				addressed = true
			}
		}
	}
	return strings.TrimSpace(text), addressed
}

func cutAt(s string, i, n int) string {
	left, right := s[:i], s[i+n:]
	if strings.HasPrefix(right, " ") {
		right = right[1:]
	} else if strings.HasSuffix(left, " ") {
		left = left[:len(left)-1]
	}
	return left + right
}

func attachmentsOf(msg *gwMessage) []chat.AttachmentRef {
	var refs []chat.AttachmentRef
	for _, att := range msg.Attachments {
		if att.URL == "" {
			continue
		}
		refs = append(refs, chat.AttachmentRef{
			Kind: attachmentKind(att.ContentType),
			ID:   att.URL,
			Name: att.Filename,
			MIME: att.ContentType,
			Size: att.Size,
		})
	}
	return refs
}

func attachmentKind(contentType string) string {
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "photo"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	}
	return "document"
}
