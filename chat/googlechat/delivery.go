package googlechat

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// NewDelivery implements [chat.Adapter]. replyTo is the inbound
// message.name; the first final-message id is derived from it, so a crashed
// turn's retry addresses the same client-assigned message. Google Chat is
// final-only, so resumePreviewID is ignored.
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo string, resumePreviewID string) chat.Delivery {
	_ = resumePreviewID
	return &delivery{
		adapter:  a,
		space:    key.ChatID,
		thread:   key.ThreadID,
		clientID: turnMessageID(replyTo, key),
	}
}

type delivery struct {
	adapter  *Adapter
	space    string // "spaces/AAAA"
	thread   string // "spaces/AAAA/threads/DDDD", "" when unthreaded
	clientID string // derived client-assigned first final-message id

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // message names of the chunks counted in sent

}

var _ chat.Delivery = (*delivery)(nil)

// Typing implements [chat.Delivery]: a no-op — the Chat API has no typing
// or presence endpoint for apps.
func (d *delivery) Typing(ctx context.Context) error { return nil }

// Preview implements [chat.Delivery]. D28 keeps Google Chat final-only.
func (d *delivery) Preview(context.Context, string) error { return nil }

// PreviewID implements [chat.Delivery]. Final-only delivery has no preview
// message to resume after a crash.
func (d *delivery) PreviewID() string { return "" }

// Finalize implements [chat.Delivery]: markdown is converted to the Chat
// dialect and chunked at 4000 characters without splitting code fences.
// Every chunk is created under a deterministic client-assigned id, so both
// same-process retries (tracked via sent) and cross-crash retries
// (ALREADY_EXISTS degrading to an edit) never duplicate messages.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	if strings.TrimSpace(text) == "" {
		text = "(empty reply)"
	}
	chunks := ChunkText(FormatText(text), chunkLimit)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}
	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		if err := d.adapter.waitSpaceWrite(ctx, d.space, time.Second); err != nil {
			return chat.Receipt{}, err
		}
		name, err := d.adapter.createOrUpdate(ctx, d.space, d.chunkID(i), chunk, d.thread)
		if err != nil {
			return chat.Receipt{}, err
		}
		d.sent = i + 1
		d.sentIDs = append(d.sentIDs, name)
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

func (d *delivery) chunkID(i int) string {
	if i == 0 {
		return d.clientID
	}
	return d.clientID + "-" + strconv.Itoa(i)
}

// Notify implements [chat.Delivery]: plain messages.create calls (no
// markdown conversion, no client-assigned id — notices may repeat).
func (d *delivery) Notify(ctx context.Context, text string) error {
	for _, chunk := range ChunkText(text, chunkLimit) {
		if err := d.adapter.waitSpaceWrite(ctx, d.space, time.Second); err != nil {
			return err
		}
		if _, err := d.adapter.createMessage(ctx, d.space, "", chunk, d.thread); err != nil {
			return err
		}
	}
	return nil
}
