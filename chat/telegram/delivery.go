package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/chat"
)

// NewDelivery implements [chat.Adapter]. replyTo is the inbound event id
// ("tg:<chat_id>:<message_id>"); a non-empty resumePreviewID makes Finalize
// edit that message instead of sending a new one (crash recovery).
func (a *Adapter) NewDelivery(key chat.ConversationKey, replyTo string, resumePreviewID string) chat.Delivery {
	chatID, _ := strconv.ParseInt(key.ChatID, 10, 64)
	threadID, _ := strconv.ParseInt(key.ThreadID, 10, 64)
	previewID, _ := strconv.ParseInt(resumePreviewID, 10, 64)
	return &delivery{
		adapter:   a,
		chatID:    chatID,
		threadID:  threadID,
		replyTo:   eventMessageID(replyTo),
		previewID: previewID,
	}
}

// delivery is one turn's Telegram output surface. chat.Delivery calls are
// serialized but Preview/PreviewID arrive on the preview renderer goroutine;
// the mutex shields the preview state and the typing refresher.
type delivery struct {
	adapter  *Adapter
	chatID   int64
	threadID int64
	replyTo  int64

	sent    int      // chunks already delivered: Finalize retries resume here
	sentIDs []string // message ids of the chunks counted in sent

	mu            sync.Mutex
	previewID     int64
	previewText   string
	lastPreviewAt time.Time
	typingStop    chan struct{}
}

var _ chat.Delivery = (*delivery)(nil)

// Typing implements [chat.Delivery]: an immediate sendChatAction plus a
// refresher ticking every TypingInterval (the indicator shows at most ~5s per
// call) until Finalize or Notify.
func (d *delivery) Typing(ctx context.Context) error {
	d.mu.Lock()
	if d.typingStop == nil {
		stop := make(chan struct{})
		d.typingStop = stop
		go d.typingLoop(ctx, stop)
	}
	d.mu.Unlock()
	return d.adapter.client.sendChatAction(ctx, d.chatID, d.threadID, "typing")
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
			if err := d.adapter.client.sendChatAction(ctx, d.chatID, d.threadID, "typing"); err != nil {
				d.adapter.logger.Debug("telegram: typing refresh failed", "error", err)
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

// errPreviewThrottled reports a preview edit skipped by the per-chat rate
// limit. Returning an error (not nil) keeps the snapshot dirty in the
// coalescer so the text is retried on a later tick instead of dropped.
var errPreviewThrottled = errors.New("telegram: preview edit rate limited")

// Preview implements [chat.Delivery]: the first call sends a plain-text
// message, later calls edit it. Unchanged text is skipped, and edits are
// rate-limited to one per PreviewMinInterval per chat — a throttled edit
// returns [errPreviewThrottled] so the caller retries it.
func (d *delivery) Preview(ctx context.Context, text string) error {
	text = utf16Truncate(text, textLimit)
	d.mu.Lock()
	defer d.mu.Unlock()
	if text == "" || text == d.previewText {
		return nil
	}
	now := time.Now()
	if d.previewID != 0 && now.Sub(d.lastPreviewAt) < d.adapter.previewMinInterval {
		return errPreviewThrottled
	}
	if d.previewID == 0 {
		params := sendMessageParams{
			ChatID:             d.chatID,
			Text:               text,
			MessageThreadID:    d.threadID,
			LinkPreviewOptions: &linkPreviewOptions{IsDisabled: true},
		}
		if d.replyTo != 0 {
			params.ReplyParameters = &replyParameters{MessageID: d.replyTo, AllowSendingWithoutReply: true}
		}
		message, err := d.adapter.client.sendMessage(ctx, params)
		if err != nil {
			return err
		}
		d.previewID = message.MessageID
	} else {
		err := d.adapter.client.editMessageText(ctx, editMessageParams{
			ChatID:             d.chatID,
			MessageID:          d.previewID,
			Text:               text,
			LinkPreviewOptions: &linkPreviewOptions{IsDisabled: true},
		})
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
	if d.previewID == 0 {
		return ""
	}
	return strconv.FormatInt(d.previewID, 10)
}

// Finalize implements [chat.Delivery]: markdown is rendered to Telegram HTML
// and chunked; the first chunk edits the preview when one exists (else it is
// sent as a reply), remaining chunks follow as separate messages. A 400
// "can't parse entities" resends that chunk with no parse mode. Chunks
// already sent are skipped on retry (the processor re-invokes Finalize with
// the same text), so a failure mid-way never duplicates earlier chunks.
func (d *delivery) Finalize(ctx context.Context, text string) (chat.Receipt, error) {
	d.stopTyping()
	if strings.TrimSpace(text) == "" {
		text = "(empty reply)"
	}
	chunks := formatHTML(text, textLimit)
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
		if i == 0 && previewID != 0 {
			if err := d.editChunk(ctx, previewID, chunk); err != nil {
				return chat.Receipt{}, err
			}
			d.sent = 1
			d.sentIDs = append(d.sentIDs, strconv.FormatInt(previewID, 10))
			continue
		}
		var reply *replyParameters
		if i == 0 && d.replyTo != 0 {
			reply = &replyParameters{MessageID: d.replyTo, AllowSendingWithoutReply: true}
		}
		id, err := d.sendChunk(ctx, chunk, reply)
		if err != nil {
			return chat.Receipt{}, err
		}
		d.sent = i + 1
		d.sentIDs = append(d.sentIDs, strconv.FormatInt(id, 10))
	}
	return chat.Receipt{MessageIDs: d.sentIDs, At: time.Now().UTC()}, nil
}

// Notify implements [chat.Delivery]: one plain sendMessage.
func (d *delivery) Notify(ctx context.Context, text string) error {
	d.stopTyping()
	_, err := d.adapter.client.sendMessage(ctx, sendMessageParams{
		ChatID:             d.chatID,
		Text:               utf16Truncate(text, textLimit),
		MessageThreadID:    d.threadID,
		LinkPreviewOptions: &linkPreviewOptions{IsDisabled: true},
	})
	return err
}

// editChunk edits messageID with HTML, falling back to no parse mode on a
// parse-entities rejection.
func (d *delivery) editChunk(ctx context.Context, messageID int64, chunk string) error {
	params := editMessageParams{
		ChatID:             d.chatID,
		MessageID:          messageID,
		Text:               chunk,
		ParseMode:          "HTML",
		LinkPreviewOptions: &linkPreviewOptions{IsDisabled: true},
	}
	err := d.adapter.client.editMessageText(ctx, params)
	if isParseEntitiesError(err) {
		params.ParseMode = ""
		err = d.adapter.client.editMessageText(ctx, params)
	}
	return err
}

// sendChunk sends one HTML chunk, falling back to no parse mode on a
// parse-entities rejection.
func (d *delivery) sendChunk(ctx context.Context, chunk string, reply *replyParameters) (int64, error) {
	params := sendMessageParams{
		ChatID:             d.chatID,
		Text:               chunk,
		ParseMode:          "HTML",
		MessageThreadID:    d.threadID,
		LinkPreviewOptions: &linkPreviewOptions{IsDisabled: true},
		ReplyParameters:    reply,
	}
	message, err := d.adapter.client.sendMessage(ctx, params)
	if isParseEntitiesError(err) {
		params.ParseMode = ""
		message, err = d.adapter.client.sendMessage(ctx, params)
	}
	if err != nil {
		return 0, err
	}
	return message.MessageID, nil
}

// isParseEntitiesError matches the 400 returned for invalid HTML entities.
func isParseEntitiesError(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == 400 &&
		strings.Contains(apiErr.Description, "can't parse entities")
}

// eventMessageID extracts the trailing message id from a "tg:<chat>:<msg>"
// event id; 0 when absent or malformed.
func eventMessageID(eventID string) int64 {
	if eventID == "" {
		return 0
	}
	if i := strings.LastIndexByte(eventID, ':'); i >= 0 {
		eventID = eventID[i+1:]
	}
	id, err := strconv.ParseInt(eventID, 10, 64)
	if err != nil {
		return 0
	}
	return id
}
