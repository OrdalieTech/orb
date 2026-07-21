package telegram

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/OrdalieTech/pigo/chat"
)

// secretTokenHeader carries the webhook secret set via setWebhook.
const secretTokenHeader = "X-Telegram-Bot-Api-Secret-Token"

// Webhook returns the webhook ingress handler: it authenticates the secret
// token (constant-time, 403 on mismatch), normalizes the update, and hands
// the message to publish. A publish failure answers non-2xx so Telegram
// redelivers; album parts hold their response until the buffered group has
// flushed so a failed flush is redelivered too. Options.Secret is required:
// with an empty secret every POST would authenticate.
func (a *Adapter) Webhook(publish func(chat.Message) error) http.Handler {
	if a.secret == "" {
		// ConstantTimeCompare("", "") matches, so an empty secret would let
		// anyone who finds the URL forge updates with any sender id. Refuse
		// to serve, mirroring the WhatsApp AppSecret stance.
		a.logger.Error("telegram: webhook ingress requires Options.Secret; rejecting all requests")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "webhook secret not configured", http.StatusForbidden)
		})
	}
	groups := newMediaGroups(a.mediaGroupDelay, func(parts []*apiMessage) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return a.publishParts(ctx, parts, publish)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		provided := r.Header.Get(secretTokenHeader)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(a.secret)) != 1 {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var update apiUpdate
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		entry, err := a.ingest(r.Context(), &update, groups, publish)
		if err == nil && entry != nil {
			// Hold the response until the album flushes: a 200 before the
			// publish would lose the group with no redelivery on failure.
			select {
			case <-entry.done:
				err = entry.err
			case <-r.Context().Done():
				err = r.Context().Err()
			}
		}
		if err != nil {
			a.logger.Warn("telegram: webhook ingest failed", "error", err)
			http.Error(w, "ingest failed", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Poll runs the long-poll ingress loop until ctx ends or publishing fails.
// It deletes any webhook first (the two ingress modes are mutually
// exclusive), resolves the bot identity, and advances the update offset only
// after every message of a batch has been published — a crash before that
// leaves the batch unacknowledged for redelivery. Run one Poll per bot
// account.
func (a *Adapter) Poll(ctx context.Context, publish func(chat.Message) error) error {
	if err := a.client.deleteWebhook(ctx); err != nil {
		return err
	}
	if err := a.ensureIdentity(ctx); err != nil {
		return err
	}
	var flushMu sync.Mutex
	var flushErr error
	groups := newMediaGroups(a.mediaGroupDelay, func(parts []*apiMessage) error {
		err := a.publishParts(ctx, parts, publish)
		if err != nil {
			flushMu.Lock()
			if flushErr == nil {
				flushErr = err
			}
			flushMu.Unlock()
		}
		return err
	})
	offset := int64(0)
	timeoutSeconds := int(a.pollTimeout / time.Second)
	for {
		if ctx.Err() != nil {
			groups.settle()
			return ctx.Err()
		}
		updates, err := a.client.getUpdates(ctx, offset, timeoutSeconds)
		if err != nil {
			if ctx.Err() != nil {
				groups.settle()
				return ctx.Err()
			}
			a.logger.Warn("telegram: getUpdates failed", "error", err)
			if sleepErr := sleepContext(ctx, time.Second); sleepErr != nil {
				groups.settle()
				return sleepErr
			}
			continue
		}
		var batchErr error
		for _, update := range updates {
			if _, err := a.ingest(ctx, &update, groups, publish); err != nil {
				batchErr = err
				break
			}
		}
		// Albums buffered in this batch must publish before the offset moves.
		groups.settle()
		flushMu.Lock()
		if batchErr == nil {
			batchErr = flushErr
		}
		flushMu.Unlock()
		if batchErr != nil {
			return batchErr
		}
		if len(updates) > 0 {
			offset = updates[len(updates)-1].UpdateID + 1
		}
	}
}

// ingest routes one update: album parts go to the media-group buffer (the
// returned entry reports the group's eventual flush), everything else is
// normalized and published immediately.
func (a *Adapter) ingest(ctx context.Context, update *apiUpdate, groups *mediaGroups, publish func(chat.Message) error) (*mediaGroupEntry, error) {
	msg := update.Message
	if msg == nil {
		return nil, nil
	}
	if msg.MediaGroupID != "" {
		return groups.add(msg), nil
	}
	// A pending album of this chat must publish before any later message of
	// the same chat, or the user's turns arrive out of order.
	groups.flushChat(msg.Chat.ID)
	m, ok, err := a.normalizeMessage(ctx, msg, attachmentsOf(msg))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return nil, publish(m)
}

// publishParts merges buffered album parts into one message and publishes it.
func (a *Adapter) publishParts(ctx context.Context, parts []*apiMessage, publish func(chat.Message) error) error {
	merged, attachments := mergeParts(parts)
	m, ok, err := a.normalizeMessage(ctx, merged, attachments)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return publish(m)
}

// mergeParts collapses album parts into one message carrying every
// attachment and the first non-empty caption.
func mergeParts(parts []*apiMessage) (*apiMessage, []chat.AttachmentRef) {
	merged := *parts[0]
	var attachments []chat.AttachmentRef
	for _, part := range parts {
		attachments = append(attachments, attachmentsOf(part)...)
		if merged.Caption == "" && part.Caption != "" {
			merged.Caption = part.Caption
			merged.CaptionEntities = part.CaptionEntities
		}
	}
	return &merged, attachments
}

// normalizeMessage converts a platform message into a [chat.Message]. ok is
// false when the message should be dropped: bot senders, unsupported chat
// types, empty content, or group messages that neither mention the bot nor
// reply to it.
func (a *Adapter) normalizeMessage(ctx context.Context, msg *apiMessage, attachments []chat.AttachmentRef) (chat.Message, bool, error) {
	if msg.From == nil || msg.From.IsBot {
		return chat.Message{}, false, nil
	}
	chatType := chatTypeOf(msg.Chat.Type)
	switch chatType {
	case "dm", "group":
	default: // channel posts and unknown chat types are unsupported
		return chat.Message{}, false, nil
	}
	group := chatType == "group"

	text := msg.Text
	entities := msg.Entities
	if text == "" && msg.Caption != "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}
	// Mention gating and /cmd@botname normalization need the bot username.
	if group || strings.Contains(text, "@") {
		if err := a.ensureIdentity(ctx); err != nil {
			return chat.Message{}, false, err
		}
	}
	text, addressed := stripAddressing(text, entities, a.username())
	if group && !addressed && !a.isReplyToBot(msg.ReplyTo) {
		return chat.Message{}, false, nil
	}
	if text == "" && len(attachments) == 0 {
		return chat.Message{}, false, nil
	}

	senderName := strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
	if senderName == "" {
		senderName = msg.From.Username
	}
	threadID := ""
	if msg.IsTopicMessage && msg.ThreadID != 0 {
		threadID = strconv.FormatInt(msg.ThreadID, 10)
	}
	replyToID := ""
	if msg.ReplyTo != nil {
		replyToID = eventID(msg.Chat.ID, msg.ReplyTo.MessageID)
	}
	return chat.Message{
		EventID:     eventID(msg.Chat.ID, msg.MessageID),
		Platform:    a.Platform(),
		Account:     a.account,
		ChatID:      strconv.FormatInt(msg.Chat.ID, 10),
		ThreadID:    threadID,
		ChatType:    chatType,
		SenderID:    strconv.FormatInt(msg.From.ID, 10),
		SenderName:  senderName,
		Text:        text,
		ReplyToID:   replyToID,
		Attachments: attachments,
		SentAt:      time.Unix(msg.Date, 0).UTC(),
	}, true, nil
}

// chatTypeOf maps the Telegram chat.type to the normalized [chat.Message]
// ChatType: private→dm, group/supergroup→group, channel→channel. Unknown
// types map to "".
func chatTypeOf(apiType string) string {
	switch apiType {
	case "private":
		return "dm"
	case "group", "supergroup":
		return "group"
	case "channel":
		return "channel"
	}
	return ""
}

// eventID builds the stable dedupe key: message identity, not update_id
// (which is delivery-scoped).
func eventID(chatID, messageID int64) string {
	return fmt.Sprintf("tg:%d:%d", chatID, messageID)
}

// isReplyToBot reports whether reply targets one of this bot's messages.
func (a *Adapter) isReplyToBot(reply *apiMessage) bool {
	return reply != nil && reply.From != nil && reply.From.IsBot &&
		strings.EqualFold(reply.From.Username, a.username())
}

// attachmentsOf extracts attachment refs; for photos, the sizes array is
// ordered smallest to largest and only the largest is kept. Telegram
// re-encodes every photo as JPEG and getFile reports no MIME, so the ref
// carries image/jpeg itself.
func attachmentsOf(msg *apiMessage) []chat.AttachmentRef {
	var refs []chat.AttachmentRef
	if len(msg.Photo) > 0 {
		largest := msg.Photo[len(msg.Photo)-1]
		refs = append(refs, chat.AttachmentRef{Kind: "photo", ID: largest.FileID, MIME: "image/jpeg", Size: largest.FileSize})
	}
	for _, entry := range []struct {
		kind string
		file *apiFile
	}{
		{"document", msg.Document},
		{"audio", msg.Audio},
		{"video", msg.Video},
		{"voice", msg.Voice},
	} {
		if entry.file != nil {
			refs = append(refs, chat.AttachmentRef{
				Kind: entry.kind,
				ID:   entry.file.FileID,
				Name: entry.file.FileName,
				MIME: entry.file.MimeType,
				Size: entry.file.FileSize,
			})
		}
	}
	return refs
}

// stripAddressing removes bot-directed addressing from text using entity
// offsets (UTF-16 code units, per the Bot API) and reports whether the bot
// was explicitly addressed: an @botname mention (removed) or a /cmd@botname
// command (rewritten to /cmd).
func stripAddressing(text string, entities []apiEntity, botUsername string) (string, bool) {
	if text == "" || botUsername == "" || !strings.Contains(text, "@") {
		return text, false
	}
	units := utf16.Encode([]rune(text))
	addressed := false
	var out []uint16
	pos := 0
	for _, entity := range entities {
		if entity.Offset < pos || entity.Offset+entity.Length > len(units) {
			continue
		}
		segment := string(utf16.Decode(units[entity.Offset : entity.Offset+entity.Length]))
		switch entity.Type {
		case "mention":
			if !strings.EqualFold(segment, "@"+botUsername) {
				continue
			}
			out = append(out, units[pos:entity.Offset]...)
			pos = entity.Offset + entity.Length
			// Swallow one adjacent space so "a @bot b" becomes "a b".
			if pos < len(units) && units[pos] == ' ' {
				pos++
			} else if len(out) > 0 && out[len(out)-1] == ' ' {
				out = out[:len(out)-1]
			}
			addressed = true
		case "bot_command":
			at := strings.IndexByte(segment, '@')
			if at < 0 || !strings.EqualFold(segment[at+1:], botUsername) {
				continue
			}
			out = append(out, units[pos:entity.Offset]...)
			out = append(out, utf16.Encode([]rune(segment[:at]))...)
			pos = entity.Offset + entity.Length
			addressed = true
		}
	}
	if !addressed {
		return text, false
	}
	out = append(out, units[pos:]...)
	return strings.TrimSpace(string(utf16.Decode(out))), true
}

// mediaGroups buffers album parts per chat and media_group_id and flushes
// each group after a fixed window from its first part.
//
// ponytail: fixed window from the first part, not a rolling debounce; parts
// straddling the window surface as a second message.
type mediaGroups struct {
	delay time.Duration
	flush func([]*apiMessage) error

	mu      sync.Mutex
	pending map[string]*mediaGroupEntry
	wg      sync.WaitGroup
}

// mediaGroupEntry is one buffered album: done closes once the group has
// flushed, with err carrying the flush outcome.
type mediaGroupEntry struct {
	parts []*apiMessage
	done  chan struct{}
	err   error
}

func newMediaGroups(delay time.Duration, flush func([]*apiMessage) error) *mediaGroups {
	return &mediaGroups{delay: delay, flush: flush, pending: map[string]*mediaGroupEntry{}}
}

// add buffers one album part, arming the group's flush timer on first sight,
// and returns the part's group entry.
func (g *mediaGroups) add(msg *apiMessage) *mediaGroupEntry {
	key := fmt.Sprintf("%d:%s", msg.Chat.ID, msg.MediaGroupID)
	g.mu.Lock()
	entry := g.pending[key]
	if entry == nil {
		entry = &mediaGroupEntry{done: make(chan struct{})}
		g.pending[key] = entry
		g.wg.Add(1)
		time.AfterFunc(g.delay, func() { g.fire(key) })
	}
	entry.parts = append(entry.parts, msg)
	g.mu.Unlock()
	return entry
}

// fire flushes and releases one pending group; a duplicate fire (an early
// flushChat then the timer) is a no-op.
func (g *mediaGroups) fire(key string) {
	g.mu.Lock()
	entry := g.pending[key]
	delete(g.pending, key)
	g.mu.Unlock()
	if entry == nil {
		return
	}
	entry.err = g.flush(entry.parts)
	close(entry.done)
	g.wg.Done()
}

// flushChat immediately fires every pending group of one chat so a later
// message of that chat cannot overtake its album.
func (g *mediaGroups) flushChat(chatID int64) {
	prefix := fmt.Sprintf("%d:", chatID)
	g.mu.Lock()
	var keys []string
	for key := range g.pending {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	g.mu.Unlock()
	for _, key := range keys {
		g.fire(key)
	}
}

// settle blocks until every pending group has flushed.
func (g *mediaGroups) settle() { g.wg.Wait() }
