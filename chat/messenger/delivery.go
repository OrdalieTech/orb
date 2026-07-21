package messenger

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

type delivery struct {
	adapter *Adapter
	psid    string // recipient PSID (the conversation ChatID)

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // message ids of the chunks counted in sent

	mu         sync.Mutex
	seen       bool // mark_seen fired
	typingStop chan struct{}
}

var _ chat.Delivery = (*delivery)(nil)

// NewDelivery implements chat.Adapter. replyTo and resumePreviewID are
// ignored: the Send API has no quoted-reply parameter and no message
// editing, so the adapter never creates previews and crash recovery simply
// resends the final text.
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo, resumePreviewID string) chat.Delivery {
	_, _ = replyTo, resumePreviewID
	return &delivery{adapter: a, psid: key.ChatID}
}

// Typing implements chat.Delivery: the first call fires mark_seen (the
// inbound message shows as read) then typing_on, and starts a refresher
// re-firing typing_on every TypingInterval (the indicator auto-expires after
// ~20s) until Finalize or Notify. Sender actions are their own request body,
// never combined with a message. Failures are best-effort: logged, never
// surfaced past the initial call.
func (d *delivery) Typing(ctx context.Context) error {
	d.mu.Lock()
	first := !d.seen
	d.seen = true
	if d.typingStop == nil {
		stop := make(chan struct{})
		d.typingStop = stop
		go d.typingLoop(ctx, stop)
	}
	d.mu.Unlock()
	if first {
		if err := d.adapter.senderAction(ctx, d.psid, "mark_seen"); err != nil {
			d.adapter.logger.Debug("messenger: mark_seen failed", "error", err)
		}
	}
	return d.adapter.senderAction(ctx, d.psid, "typing_on")
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
			err := d.adapter.senderAction(ctx, d.psid, "typing_on")
			if err == nil {
				continue
			}
			d.adapter.logger.Debug("messenger: typing refresh failed", "error", err)
			var graphErr *GraphError
			if errors.As(err, &graphErr) && retryable(graphErr.Code) {
				return
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

// Preview is a no-op: the Send API is append-only (no message editing), so
// there is no streaming preview — the sustained typing indicator is the
// activity signal and only the final reply is delivered.
func (d *delivery) Preview(ctx context.Context, text string) error { return nil }

// PreviewID always returns "" (no preview ever exists).
func (d *delivery) PreviewID() string { return "" }

// Finalize implements chat.Delivery: the reply is chunked at chunkLimit
// runes (the Send API caps message.text at 2000 UTF-8 characters) and sent
// sequentially as messaging_type RESPONSE, each response's message_id
// captured as the receipt. An empty or whitespace-only reply is delivered as
// "(empty reply)" so the turn is never silently swallowed. Chunks already
// sent are skipped on retry (the processor re-invokes Finalize with the same
// text), so a failure mid-way never duplicates earlier chunks.
//
// ponytail: model markdown passes through untouched — Messenger renders
// plain text only; strip-to-plain if operators complain about literal **.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	d.stopTyping()
	chunks := chunkText(text, chunkLimit)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}
	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		id, err := d.adapter.sendText(ctx, d.psid, chunk, "RESPONSE")
		if err != nil {
			return chat.Receipt{At: time.Now().UTC()}, err
		}
		d.sent = i + 1
		if id != "" {
			d.sentIDs = append(d.sentIDs, id)
		}
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

// Notify sends a small out-of-band notice as messaging_type UPDATE
// (proactive content). Like every Messenger send it is subject to the
// 24-hour window: outside it the Send API rejects with code 10 subcode
// 2018278 and the error surfaces loudly — the gateway stays silent until
// the user messages again, by design.
func (d *delivery) Notify(ctx context.Context, text string) error {
	d.stopTyping()
	for _, chunk := range chunkText(text, chunkLimit) {
		if _, err := d.adapter.sendText(ctx, d.psid, chunk, "UPDATE"); err != nil {
			return err
		}
	}
	return nil
}

func (a *Adapter) senderAction(ctx context.Context, psid, action string) error {
	payload := map[string]any{
		"recipient":     map[string]string{"id": psid},
		"sender_action": action,
	}
	return a.do(ctx, http.MethodPost, a.sendPath(), payload, nil)
}

type sendResponse struct {
	MessageID string `json:"message_id"`
}

func (a *Adapter) sendText(ctx context.Context, psid, body, messagingType string) (string, error) {
	payload := map[string]any{
		"recipient":      map[string]string{"id": psid},
		"messaging_type": messagingType,
		"message":        map[string]string{"text": body},
	}
	for attempt := 0; ; attempt++ {
		var out sendResponse
		err := a.do(ctx, http.MethodPost, a.sendPath(), payload, &out)
		if err == nil {
			return out.MessageID, nil
		}
		var graphErr *GraphError
		if errors.As(err, &graphErr) && retryable(graphErr.Code) && attempt+1 < a.maxAttempts {
			wait := a.backoff(attempt)
			if graphErr.RegainAfter > 0 {
				wait = graphErr.RegainAfter
			}
			// ponytail: a regain hint beyond maxRetryWait surfaces now
			// instead of sleeping minutes inside a turn — the processor's
			// delivery retry loop and queue redelivery are the safety net,
			// as for 5xx/transport errors.
			if wait <= a.maxRetryWait {
				select {
				case <-time.After(wait):
					continue
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}
		}
		return "", err
	}
}
