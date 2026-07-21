package slack

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// NewDelivery implements [chat.Adapter]. The conversation ThreadID (thread_ts
// or "" for top-level DM replies) threads every outbound message; a non-empty
// resumePreviewID makes Finalize edit that message instead of posting a new
// one (crash recovery).
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo, resumePreviewID string) chat.Delivery {
	_ = replyTo // reply threading rides on thread_ts, not an explicit reply id
	return &delivery{
		adapter:   a,
		channel:   key.ChatID,
		threadTS:  key.ThreadID,
		previewTS: resumePreviewID,
	}
}

type delivery struct {
	adapter  *Adapter
	channel  string
	threadTS string

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // ts values of the chunks counted in sent

	mu            sync.Mutex
	previewTS     string
	previewText   string
	lastPreviewAt time.Time
	// editsDead is set once chat.update reports edit_window_closed or
	// cant_update_message: previews stop and Finalize posts new messages.
	editsDead bool
}

var _ chat.Delivery = (*delivery)(nil)

// Typing implements [chat.Delivery] as a no-op: Slack has no typing
// indicator for bot messages (RTM is dead for new apps and
// assistant.threads.setStatus only works in AI-assistant threads). The
// streamed preview message is the activity signal.
func (d *delivery) Typing(ctx context.Context) error { return nil }

// An error keeps the coalescer snapshot dirty for the next tick.
var errPreviewThrottled = errors.New("slack: preview edit rate limited")

// Preview implements [chat.Delivery]: the first call posts the preview
// message, later calls edit it via chat.update at most once per
// PreviewMinInterval (chat.update is Tier 3, ~1/sec sustained). Unchanged
// text is skipped. Once the platform refuses edits (edit_window_closed,
// cant_update_message) previews stop for the turn and Finalize posts new
// messages instead.
func (d *delivery) Preview(ctx context.Context, text string) error {
	text = truncateRunes(escapeText(text), textLimit)
	d.mu.Lock()
	defer d.mu.Unlock()
	if text == "" || text == d.previewText || d.editsDead {
		return nil
	}
	now := time.Now()
	if d.previewTS != "" && now.Sub(d.lastPreviewAt) < d.adapter.previewMinInterval {
		return errPreviewThrottled
	}
	if d.previewTS == "" {
		ts, err := d.adapter.client.postMessage(ctx, postMessageParams{
			Channel:  d.channel,
			Text:     text,
			ThreadTS: d.threadTS,
		})
		if err != nil {
			return err
		}
		d.previewTS = ts
	} else {
		err := d.adapter.client.updateMessage(ctx, updateMessageParams{
			Channel: d.channel,
			TS:      d.previewTS,
			Text:    text,
		})
		if isEditExpired(err) {
			d.editsDead = true
			return nil
		}
		if err != nil {
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
	return d.previewTS
}

// Finalize implements [chat.Delivery]: markdown is transcoded to mrkdwn and
// chunked at the 4,000-char limit; the first chunk edits the preview when
// one exists (falling back to a new post when the edit window has closed),
// remaining chunks are posted to the same thread. Chunks already sent are
// skipped on retry (the processor re-invokes Finalize with the same text),
// so a failure mid-way never duplicates earlier chunks.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	if strings.TrimSpace(text) == "" {
		text = "(empty reply)"
	}
	chunks := ChunkText(FormatText(text), textLimit)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}
	d.mu.Lock()
	previewTS := d.previewTS
	editsDead := d.editsDead
	d.mu.Unlock()
	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		if i == 0 && previewTS != "" && !editsDead {
			err := d.adapter.client.updateMessage(ctx, updateMessageParams{
				Channel: d.channel,
				TS:      previewTS,
				Text:    chunk,
			})
			if err == nil {
				d.sent = 1
				d.sentIDs = append(d.sentIDs, previewTS)
				continue
			}
			if !isEditExpired(err) {
				return chat.Receipt{}, err
			}
			// Edit window closed under us: leave the preview as-is and
			// deliver the final text as new posts.
			d.mu.Lock()
			d.editsDead = true
			d.mu.Unlock()
			editsDead = true
		}
		ts, err := d.adapter.client.postMessage(ctx, postMessageParams{
			Channel:  d.channel,
			Text:     chunk,
			ThreadTS: d.threadTS,
		})
		if err != nil {
			return chat.Receipt{}, err
		}
		d.sent = i + 1
		d.sentIDs = append(d.sentIDs, ts)
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

// Notify implements [chat.Delivery]: plain chunked posts with Slack control
// sequences escaped, but no markdown transcoding.
func (d *delivery) Notify(ctx context.Context, text string) error {
	for _, chunk := range ChunkText(escapeText(text), textLimit) {
		if _, err := d.adapter.client.postMessage(ctx, postMessageParams{
			Channel:  d.channel,
			Text:     chunk,
			ThreadTS: d.threadTS,
		}); err != nil {
			return err
		}
	}
	return nil
}
