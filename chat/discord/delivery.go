package discord

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// NewDelivery implements [chat.Adapter]. replyTo is the inbound event id
// ("dc:<channel_id>:<message_id>"); a non-empty resumePreviewID makes
// Finalize edit that message instead of sending a new one (crash recovery).
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo string, resumePreviewID string) chat.Delivery {
	return &delivery{
		adapter:   a,
		channelID: key.ChatID,
		replyTo:   eventMessageID(replyTo),
		previewID: resumePreviewID,
	}
}

type delivery struct {
	adapter   *Adapter
	channelID string
	replyTo   string // inbound message id, "" when unknown

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // message ids of the chunks counted in sent

	mu            sync.Mutex
	previewID     string
	previewText   string
	lastPreviewAt time.Time
	typingStop    chan struct{}
}

var _ chat.Delivery = (*delivery)(nil)

// Typing implements [chat.Delivery]: an immediate POST /typing plus a
// refresher ticking every TypingInterval (the indicator expires after ~10s)
// until Finalize or Notify.
func (d *delivery) Typing(ctx context.Context) error {
	d.mu.Lock()
	if d.typingStop == nil {
		stop := make(chan struct{})
		d.typingStop = stop
		go d.typingLoop(ctx, stop)
	}
	d.mu.Unlock()
	return d.adapter.client.triggerTyping(ctx, d.channelID)
}

func (d *delivery) typingLoop(ctx context.Context, stop chan struct{}) {
	ticker := time.NewTicker(d.adapter.typingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.adapter.client.triggerTyping(ctx, d.channelID); err != nil {
				d.adapter.logger.Debug("discord: typing refresh failed", "error", err)
			}
		}
	}
}

func (d *delivery) stopTyping() {
	d.mu.Lock()
	if d.typingStop != nil {
		close(d.typingStop)
		d.typingStop = nil
	}
	d.mu.Unlock()
}

// An error keeps the coalescer snapshot dirty for the next tick.
var errPreviewThrottled = errors.New("discord: preview edit rate limited")

// Preview implements [chat.Delivery]: the first call posts a message, later
// calls PATCH it. Unchanged text is skipped, and edits are rate-limited to
// one per PreviewMinInterval — a throttled edit returns
// [errPreviewThrottled] so the caller retries it. A preview deleted out
// from under us (10008 Unknown Message) is recreated. Every payload carries
// allowed_mentions {"parse":[]}.
func (d *delivery) Preview(ctx context.Context, text string) error {
	text = truncateRunes(text, messageLimit)
	d.mu.Lock()
	defer d.mu.Unlock()
	if text == "" || text == d.previewText {
		return nil
	}
	now := time.Now()
	if d.previewID != "" && now.Sub(d.lastPreviewAt) < d.adapter.previewMinInterval {
		return errPreviewThrottled
	}
	if d.previewID == "" {
		id, err := d.createChunk(ctx, text, true)
		if err != nil {
			return err
		}
		d.previewID = id
	} else {
		err := d.adapter.client.editMessage(ctx, d.channelID, d.previewID, editMessageParams{
			Content:         text,
			AllowedMentions: noMentions(),
		})
		if isUnknownMessage(err) {
			id, createErr := d.createChunk(ctx, text, true)
			if createErr != nil {
				return createErr
			}
			d.previewID = id
		} else if err != nil {
			return err
		}
	}
	d.previewText = text
	d.lastPreviewAt = now
	return nil
}

// PreviewID implements [chat.Delivery].
func (d *delivery) PreviewID() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.previewID
}

// Finalize implements [chat.Delivery]: the reply is chunked at the 2000-rune
// content cap (paragraph → line → word boundaries); the first chunk edits
// the preview when one exists (else it is posted as a reply — the
// message_reference rides the first chunk only), remaining chunks follow as
// bare messages. Chunks already sent are skipped on retry (the processor
// re-invokes Finalize with the same text), so a failure mid-way never
// duplicates earlier chunks. Every payload carries allowed_mentions
// {"parse":[]}.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	d.stopTyping()
	if strings.TrimSpace(text) == "" {
		text = "(empty reply)"
	}
	chunks := chunkText(text, messageLimit)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}
	d.mu.Lock()
	previewID := d.previewID
	d.mu.Unlock()
	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		if i == 0 && previewID != "" {
			err := d.adapter.client.editMessage(ctx, d.channelID, previewID, editMessageParams{
				Content:         chunk,
				AllowedMentions: noMentions(),
			})
			if isUnknownMessage(err) {
				// The preview was deleted: fall back to a fresh send.
				id, sendErr := d.createChunk(ctx, chunk, true)
				if sendErr != nil {
					return chat.Receipt{}, sendErr
				}
				d.sent = 1
				d.sentIDs = append(d.sentIDs, id)
				continue
			}
			if err != nil {
				return chat.Receipt{}, err
			}
			d.sent = 1
			d.sentIDs = append(d.sentIDs, previewID)
			continue
		}
		id, err := d.createChunk(ctx, chunk, i == 0)
		if err != nil {
			return chat.Receipt{}, err
		}
		d.sent = i + 1
		d.sentIDs = append(d.sentIDs, id)
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

// Notify implements [chat.Delivery]: plain chunked sends, no reply
// threading.
func (d *delivery) Notify(ctx context.Context, text string) error {
	d.stopTyping()
	for _, chunk := range chunkText(text, messageLimit) {
		if _, err := d.createChunk(ctx, chunk, false); err != nil {
			return err
		}
	}
	return nil
}

func (d *delivery) createChunk(ctx context.Context, chunk string, first bool) (string, error) {
	params := createMessageParams{Content: chunk, AllowedMentions: noMentions()}
	if first && d.replyTo != "" {
		params.MessageReference = &messageReference{MessageID: d.replyTo, FailIfNotExists: false}
	}
	return d.adapter.client.createMessage(ctx, d.channelID, params)
}

func isUnknownMessage(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == 10008
}

func eventMessageID(eventID string) string {
	if eventID == "" {
		return ""
	}
	parts := strings.Split(eventID, ":")
	if len(parts) < 3 {
		return ""
	}
	return parts[len(parts)-1]
}
