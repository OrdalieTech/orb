package whatsapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// delivery is one turn's output surface. chat.Delivery calls are serialized
// (Preview/PreviewID may arrive on the renderer goroutine, but both are
// no-ops here), so plain fields touched only by Typing/Finalize suffice.
type delivery struct {
	adapter *Adapter
	to      string // recipient wa_id (the conversation ChatID)
	inbound string // inbound wamid: mark-as-read target + reply context
	typed   bool
	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // wamids of the chunks counted in sent
}

// NewDelivery implements chat.Adapter. resumePreviewID is ignored: the Cloud
// API cannot edit messages, so this adapter never creates previews and crash
// recovery simply resends the final text.
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo, resumePreviewID string) chat.Delivery {
	_ = resumePreviewID
	return &delivery{adapter: a, to: key.ChatID, inbound: replyTo}
}

// Typing marks the inbound message read and shows the typing indicator (the
// Cloud API has no standalone typing call — it piggybacks on a read receipt
// of a specific inbound wamid, showing for <=25s or until the reply). It
// fires at most once per delivery.
func (d *delivery) Typing(ctx context.Context) error {
	if d.typed || d.inbound == "" {
		return nil
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"status":            "read",
		"message_id":        d.inbound,
		"typing_indicator":  map[string]string{"type": "text"},
	}
	if err := d.adapter.do(ctx, http.MethodPost, d.adapter.messagesPath(), payload, nil); err != nil {
		return err
	}
	d.typed = true
	return nil
}

// Preview is a no-op: the Cloud API cannot edit sent messages, so there is
// no streaming preview — only the final reply is delivered.
func (d *delivery) Preview(ctx context.Context, text string) error { return nil }

// PreviewID always returns "" (no preview ever exists).
func (d *delivery) PreviewID() string { return "" }

// Finalize converts the markdown reply to WhatsApp markup, chunks it at 4096
// characters, threads the first chunk onto the inbound wamid via
// context.message_id, and captures each response's messages[0].id as the
// receipt. An empty or whitespace-only reply is delivered as "(empty reply)"
// (mirroring the Telegram adapter) so the turn is never silently swallowed.
// Chunks already sent are skipped on retry (the processor re-invokes Finalize
// with the same text), so a failure mid-way never duplicates earlier chunks.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	chunks := ChunkText(FormatText(text), maxMessageLen)
	if len(chunks) == 0 {
		chunks = []string{"(empty reply)"}
	}
	for i, chunk := range chunks {
		if i < d.sent {
			continue
		}
		contextID := ""
		if i == 0 {
			contextID = d.inbound
		}
		id, err := d.adapter.sendText(ctx, d.to, chunk, contextID)
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

// Notify sends a small out-of-band notice as plain text (no markdown
// conversion, no reply context).
func (d *delivery) Notify(ctx context.Context, text string) error {
	for _, chunk := range ChunkText(text, maxMessageLen) {
		if _, err := d.adapter.sendText(ctx, d.to, chunk, ""); err != nil {
			return err
		}
	}
	return nil
}

// sendResponse is the subset of the send-message response the gateway reads.
type sendResponse struct {
	Messages []struct {
		ID string `json:"id"`
	} `json:"messages"`
}

// sendText sends one text message and returns the wamid from
// messages[0].id. Retryable Graph errors (130429 throughput, 131048
// spam/pair rate limits) get bounded backoff retries; 131047 (24h window)
// and 190 (bad token) surface immediately and are never retried;
// 100/131026/131051 fail fast with a clear error.
func (a *Adapter) sendText(ctx context.Context, to, body, contextID string) (string, error) {
	if strings.TrimSpace(body) == "" {
		return "", nil
	}
	payload := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "text",
		"text":              map[string]any{"preview_url": false, "body": body},
	}
	if contextID != "" {
		payload["context"] = map[string]string{"message_id": contextID}
	}
	for attempt := 0; ; attempt++ {
		var out sendResponse
		err := a.do(ctx, http.MethodPost, a.messagesPath(), payload, &out)
		if err == nil {
			if len(out.Messages) == 0 {
				return "", nil
			}
			return out.Messages[0].ID, nil
		}
		var graphErr *GraphError
		if errors.As(err, &graphErr) && retryable(graphErr.Code) && attempt+1 < a.maxAttempts {
			select {
			case <-time.After(a.backoff(attempt)):
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		// ponytail: no retry on 5xx/transport errors — the processor's
		// delivery retry loop and queue redelivery are the safety net.
		return "", err
	}
}
