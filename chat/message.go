// Package chat implements the core of the pigo chat gateway (D27):
// platform-agnostic messages, the synchronous turn processor with its durable
// turn ledger, a preview coalescer, and a local single-process runner with a
// durable spool.
//
// Platform adapters (Telegram, WhatsApp) live in subpackages and plug in via
// the [Adapter] and [Delivery] interfaces; agent sessions are supplied by a
// [SessionProvider] such as [NewLocalProvider].
package chat

import (
	"strings"
	"time"
)

// ConversationKey identifies one conversation across platforms. All fields
// are optional except Platform, Account, and ChatID.
type ConversationKey struct {
	Tenant   string
	Platform string
	Account  string
	ChatID   string
	ThreadID string
}

// String returns a stable, filesystem- and partition-safe join of the key
// segments. The encoding is injective: distinct keys never collide.
func (k ConversationKey) String() string {
	segments := [...]string{k.Tenant, k.Platform, k.Account, k.ChatID, k.ThreadID}
	size := len(segments) - 1
	for _, segment := range segments {
		size += len(segment)
		for i := range len(segment) {
			if !safeKeyByte(segment[i]) {
				size += 2
			}
		}
	}
	var builder strings.Builder
	builder.Grow(size)
	for i, segment := range segments {
		if i > 0 {
			builder.WriteByte('~')
		}
		for j := range len(segment) {
			c := segment[j]
			if safeKeyByte(c) {
				builder.WriteByte(c)
				continue
			}
			const hex = "0123456789ABCDEF"
			builder.WriteByte('%')
			builder.WriteByte(hex[c>>4])
			builder.WriteByte(hex[c&0x0F])
		}
	}
	return builder.String()
}

func safeKeyByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '.' || c == '_' || c == '-'
}

// AttachmentRef is an opaque platform reference to an inbound media object.
// It never carries credentials or open handles; adapters resolve it on demand
// via [Adapter.Download].
type AttachmentRef struct {
	// Kind is one of "photo", "document", "audio", "video", "voice".
	Kind string `json:"kind"`
	// ID is the platform handle (telegram file_id / whatsapp media id).
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	MIME string `json:"mime,omitempty"`
	Size int64  `json:"size,omitempty"`
}

// Message is one normalized inbound platform message. It is fully
// JSON-serializable so it can cross a durable queue.
type Message struct {
	// EventID is platform-unique and stable across redelivery; it is the
	// dedupe key for the turn ledger.
	EventID string `json:"eventId"`

	Tenant   string `json:"tenant,omitempty"`
	Platform string `json:"platform"`
	Account  string `json:"account"`
	ChatID   string `json:"chatId"`
	ThreadID string `json:"threadId,omitempty"`

	// ChatType classifies the conversation: "dm" (one-to-one), "group"
	// (multi-party), or "channel" (broadcast). Group messages carry sender
	// attribution in the prompt.
	ChatType string `json:"chatType,omitempty"`

	SenderID   string `json:"senderId,omitempty"`
	SenderName string `json:"senderName,omitempty"`

	Text        string          `json:"text,omitempty"`
	ReplyToID   string          `json:"replyToId,omitempty"`
	Attachments []AttachmentRef `json:"attachments,omitempty"`
	SentAt      time.Time       `json:"sentAt,omitzero"`
}

// Key returns the conversation key this message belongs to.
func (m Message) Key() ConversationKey {
	return ConversationKey{
		Tenant:   m.Tenant,
		Platform: m.Platform,
		Account:  m.Account,
		ChatID:   m.ChatID,
		ThreadID: m.ThreadID,
	}
}
