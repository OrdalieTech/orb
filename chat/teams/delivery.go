package teams

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// NewDelivery implements [chat.Adapter]. replyTo is the inbound activity id
// (the EventID). Teams is final-only, so a recovered preview id is ignored.
// The serviceUrl comes from the per-conversation store fed by validated
// inbound activities.
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo string, resumePreviewID string) chat.Delivery {
	_ = resumePreviewID
	info, _ := a.conversation(key.ChatID)
	return &delivery{
		adapter:    a,
		convID:     key.ChatID,
		serviceURL: info.serviceURL,
		replyTo:    replyTo,
	}
}

type delivery struct {
	adapter    *Adapter
	convID     string
	serviceURL string
	replyTo    string

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // activity ids of the chunks counted in sent

	mu         sync.Mutex
	typingStop chan struct{}
}

var _ chat.Delivery = (*delivery)(nil)

func (d *delivery) botAccount() *channelAccount {
	return &channelAccount{ID: "28:" + d.adapter.appID}
}

func (d *delivery) outbound(activityType string) outboundActivity {
	return outboundActivity{
		Type:         activityType,
		From:         d.botAccount(),
		Conversation: &conversationAccount{ID: d.convID},
	}
}

// Typing implements [chat.Delivery]: an immediate typing activity plus a
// refresher ticking every TypingInterval (the indicator is transient) until
// Finalize or Notify.
func (d *delivery) Typing(ctx context.Context) error {
	if d.adapter.isDead(d.convID) {
		return nil
	}
	if d.serviceURL == "" {
		return fmt.Errorf("teams: no serviceUrl known for conversation %q", d.convID)
	}
	d.mu.Lock()
	if d.typingStop == nil {
		stop := make(chan struct{})
		d.typingStop = stop
		go d.typingLoop(ctx, stop)
	}
	d.mu.Unlock()
	return d.sendTyping(ctx)
}

func (d *delivery) sendTyping(ctx context.Context) error {
	_, err := d.adapter.client.createActivity(ctx, d.serviceURL, d.convID, d.outbound("typing"))
	if d.markIfDead(err) {
		return nil
	}
	return err
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
			if err := d.sendTyping(ctx); err != nil {
				d.adapter.logger.Debug("teams: typing refresh failed", "error", err)
			}
		}
	}
}

func (d *delivery) stopTyping() {
	d.mu.Lock()
	d.stopTypingLocked()
	d.mu.Unlock()
}

func (d *delivery) stopTypingLocked() {
	if d.typingStop != nil {
		close(d.typingStop)
		d.typingStop = nil
	}
}

// Preview implements [chat.Delivery]. D28 makes Teams final-only; typing is
// the activity signal while the turn runs.
func (d *delivery) Preview(context.Context, string) error { return nil }

// PreviewID implements [chat.Delivery]. Final-only delivery has no preview
// message to resume after a crash.
func (d *delivery) PreviewID() string { return "" }

// Finalize implements [chat.Delivery]: markdown is downgraded to the Teams
// subset and chunked at ~28K UTF-16 code units (never mid-fence). Plain
// message sends are used, the first one threaded via replyToId. Chunks
// already sent are skipped on retry (the processor re-invokes Finalize with
// the same text), so a failure mid-way never duplicates earlier chunks. A
// writes-blocked conversation is marked dead and the rest is dropped
// silently.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	d.stopTyping()
	if d.adapter.isDead(d.convID) {
		return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
	}
	if d.serviceURL == "" {
		return chat.Receipt{}, fmt.Errorf("teams: no serviceUrl known for conversation %q", d.convID)
	}
	if strings.TrimSpace(text) == "" {
		text = "(empty reply)"
	}
	chunks := chunkText(formatText(text), d.adapter.chunkLimit)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}

	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		ids, err := d.sendChunk(ctx, chunk, i == 0, d.adapter.chunkLimit)
		d.sentIDs = append(d.sentIDs, ids...)
		if err != nil {
			if d.markIfDead(err) {
				return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
			}
			return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, err
		}
		d.sent = i + 1
		if i+1 < len(chunks) {
			if err := d.adapter.client.sleep(ctx, d.adapter.chunkDelay); err != nil {
				return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, err
			}
		}
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

// sendChunk sends one final chunk. The first chunk threads onto the inbound
// activity via replyToId. A 413 MessageSizeTooBig re-chunks at half the limit
// and sends the pieces sequentially.
//
// ponytail: a failure between 413 sub-pieces re-sends the whole chunk on
// the next Finalize retry — accepted duplication on that double edge.
func (d *delivery) sendChunk(ctx context.Context, chunk string, first bool, limit int) ([]string, error) {
	activity := d.outbound("message")
	activity.Text = chunk
	activity.TextFormat = "markdown"
	if first {
		activity.ReplyToID = d.replyTo
	}
	id, err := d.adapter.client.createActivity(ctx, d.serviceURL, d.convID, activity)
	if err == nil {
		return []string{id}, nil
	}
	if apiErr, ok := asAPIError(err); ok && apiErr.Status == http.StatusRequestEntityTooLarge && limit >= 2*minChunkLimit {
		var ids []string
		for _, piece := range chunkText(chunk, limit/2) {
			sub, err := d.sendChunk(ctx, piece, first, limit/2)
			first = false
			ids = append(ids, sub...)
			if err != nil {
				return ids, err
			}
		}
		return ids, nil
	}
	return nil, err
}

// Notify implements [chat.Delivery]: one plain-text message (no reply
// threading, no markdown rendering). Dead conversations swallow the notice.
func (d *delivery) Notify(ctx context.Context, text string) error {
	d.stopTyping()
	if d.adapter.isDead(d.convID) {
		return nil
	}
	if d.serviceURL == "" {
		return fmt.Errorf("teams: no serviceUrl known for conversation %q", d.convID)
	}
	activity := d.outbound("message")
	activity.Text = utf16Truncate(text, d.adapter.chunkLimit)
	activity.TextFormat = "plain"
	_, err := d.adapter.client.createActivity(ctx, d.serviceURL, d.convID, activity)
	if d.markIfDead(err) {
		return nil
	}
	return err
}

func (d *delivery) markIfDead(err error) bool {
	if apiErr, ok := asAPIError(err); ok && apiErr.writesBlocked() {
		d.adapter.markDead(d.convID)
		d.adapter.logger.Warn("teams: message writes blocked; conversation marked dead", "conversation", d.convID)
		return true
	}
	return false
}
