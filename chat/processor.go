package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

// AllowAll is an explicit opt-out authorizer accepting every message.
func AllowAll(Message) error { return nil }

// ErrRejected marks a permanently unprocessable message (unauthorized sender,
// unknown platform): redelivery can never succeed, so durable runners must ack
// and drop instead of retrying. Matched with [errors.Is].
var ErrRejected = errors.New("chat: message rejected")

// Options configures [New].
type Options struct {
	// Sessions provides exclusive conversation ownership. Required.
	Sessions SessionProvider
	// Adapters lists one adapter per platform. Required, unique platforms.
	Adapters []Adapter
	// Authorize gates every inbound message. Required; pass [AllowAll] to
	// opt out explicitly.
	Authorize func(Message) error
	// MaxConcurrent bounds concurrent turns globally. Default 64.
	MaxConcurrent int
	// PreviewInterval is the preview edit cadence. Default 1s.
	PreviewInterval time.Duration
	// TurnTimeout guards each turn's context. Default 10m.
	TurnTimeout time.Duration
	// Logger receives non-fatal diagnostics. Optional.
	Logger *slog.Logger
}

// Processor drives synchronous, per-conversation-serialized turns. Handle
// returns only once the turn is delivered (or failed), so the external queue
// acks after it returns and the queue itself is the buffer.
//
// ponytail: FIFO turns per key; Steer-into-active-run if users demand
// mid-turn injection.
type Processor struct {
	sessions        SessionProvider
	adapters        map[adapterID]Adapter
	authorize       func(Message) error
	previewInterval time.Duration
	turnTimeout     time.Duration
	logger          *slog.Logger

	semaphore chan struct{}
	locks     *keyedMutex

	activeMu sync.Mutex
	active   map[string]func()

	closeMu sync.Mutex
	closed  bool
	wg      sync.WaitGroup
}

type adapterID struct{ platform, account string }

// New validates opts and creates a Processor.
func New(opts Options) (*Processor, error) {
	if opts.Sessions == nil {
		return nil, errors.New("chat: Options.Sessions is required")
	}
	if len(opts.Adapters) == 0 {
		return nil, errors.New("chat: Options.Adapters requires at least one adapter")
	}
	if opts.Authorize == nil {
		return nil, errors.New("chat: Options.Authorize is required; pass chat.AllowAll to opt out")
	}
	if opts.MaxConcurrent <= 0 {
		opts.MaxConcurrent = 64
	}
	if opts.PreviewInterval <= 0 {
		opts.PreviewInterval = time.Second
	}
	if opts.TurnTimeout <= 0 {
		opts.TurnTimeout = 10 * time.Minute
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	adapters := make(map[adapterID]Adapter, len(opts.Adapters))
	for _, adapter := range opts.Adapters {
		key := adapterID{adapter.Platform(), adapter.Account()}
		if _, exists := adapters[key]; exists {
			return nil, fmt.Errorf("chat: duplicate adapter for platform %q account %q",
				adapter.Platform(), adapter.Account())
		}
		adapters[key] = adapter
	}
	return &Processor{
		sessions:        opts.Sessions,
		adapters:        adapters,
		authorize:       opts.Authorize,
		previewInterval: opts.PreviewInterval,
		turnTimeout:     opts.TurnTimeout,
		logger:          opts.Logger,
		semaphore:       make(chan struct{}, opts.MaxConcurrent),
		locks:           newKeyedMutex(),
		active:          map[string]func(){},
	}, nil
}

// adapterFor resolves the adapter for a message: an exact
// (platform, account) registration wins, then the platform's wildcard
// (empty-account) adapter.
func (p *Processor) adapterFor(m Message) (Adapter, bool) {
	if adapter, ok := p.adapters[adapterID{m.Platform, m.Account}]; ok {
		return adapter, true
	}
	adapter, ok := p.adapters[adapterID{m.Platform, ""}]
	return adapter, ok
}

// Handle processes one inbound message synchronously: it returns only when
// the turn is delivered, deduplicated, or failed. Redelivering the same
// EventID is safe — the turn ledger short-circuits duplicate work.
func (p *Processor) Handle(ctx context.Context, m Message) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if m.EventID == "" {
		return errors.New("chat: message requires an EventID")
	}
	p.closeMu.Lock()
	if p.closed {
		p.closeMu.Unlock()
		return errors.New("chat: processor is closed")
	}
	p.wg.Add(1)
	p.closeMu.Unlock()
	defer p.wg.Done()

	adapter, ok := p.adapterFor(m)
	if !ok {
		return fmt.Errorf("%w: no adapter for platform %q account %q", ErrRejected, m.Platform, m.Account)
	}
	if err := p.authorize(m); err != nil {
		return fmt.Errorf("%w: unauthorized message %q: %w", ErrRejected, m.EventID, err)
	}
	key := m.Key()

	// /stop preempts out of band: no keyed lock, no ledger write.
	if parseCommand(m.Text) == "/stop" {
		return p.preemptStop(ctx, adapter, key, m)
	}

	// Keyed lock first, semaphore second: waiters queued on one busy
	// conversation must not pin global MaxConcurrent slots and starve every
	// other conversation. Only executing turns count against the semaphore.
	lockKey := key.String()
	if err := p.locks.Lock(ctx, lockKey); err != nil {
		return err
	}
	defer p.locks.Unlock(lockKey)

	select {
	case p.semaphore <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.semaphore }()

	turnCtx, cancel := context.WithTimeout(ctx, p.turnTimeout)
	defer cancel()
	return p.runTurn(turnCtx, adapter, key, lockKey, m)
}

// Close waits for in-flight turns and rejects new ones.
func (p *Processor) Close(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	p.closeMu.Lock()
	p.closed = true
	p.closeMu.Unlock()
	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// preemptStop aborts the key's active run, if any, and notifies the sender.
func (p *Processor) preemptStop(ctx context.Context, adapter Adapter, key ConversationKey, m Message) error {
	p.activeMu.Lock()
	abort := p.active[key.String()]
	p.activeMu.Unlock()
	notice := "nothing to stop"
	if abort != nil {
		abort()
		notice = "stopped"
	}
	delivery := adapter.NewDelivery(key, m.EventID, "")
	if err := delivery.Notify(ctx, notice); err != nil {
		return fmt.Errorf("chat: notify stop: %w", err)
	}
	return nil
}

func (p *Processor) setActive(lockKey string, abort func()) {
	p.activeMu.Lock()
	p.active[lockKey] = abort
	p.activeMu.Unlock()
}

func (p *Processor) clearActive(lockKey string) {
	p.activeMu.Lock()
	delete(p.active, lockKey)
	p.activeMu.Unlock()
}

// runTurn executes the turn protocol inside the keyed lock.
func (p *Processor) runTurn(ctx context.Context, adapter Adapter, key ConversationKey, lockKey string, m Message) (err error) {
	conv, err := p.sessions.Acquire(ctx, key)
	if err != nil {
		return fmt.Errorf("chat: acquire conversation %q: %w", lockKey, err)
	}
	defer func() {
		if closeErr := conv.Close(context.WithoutCancel(ctx)); closeErr != nil && err == nil {
			err = fmt.Errorf("chat: close conversation: %w", closeErr)
		}
	}()

	ledger := scanTurnLedger(conv.Manager, m.EventID)
	switch {
	case ledger.delivered != nil:
		// Duplicate redelivery of a fully completed turn: no-op.
		return nil
	case ledger.settled != nil:
		// Settled but never marked delivered: never re-prompt. Deliver the
		// recorded reply with an honest possible-duplicate marker.
		return p.recoverSettled(ctx, adapter, conv, key, m, ledger)
	}

	var startedID string
	if ledger.started != nil {
		// Crashed mid-turn: re-anchor the leaf on the started marker so the
		// partial turn becomes an orphaned branch, resync the live agent,
		// and continue without a second started marker.
		startedID = ledger.started.entryID
		if branchErr := conv.Manager.Branch(startedID); branchErr != nil {
			return fmt.Errorf("chat: orphan partial turn: %w", branchErr)
		}
		conv.Session.SyncMessagesFromSession()
	} else {
		// The started marker lands before the user message enters the session.
		if startedID, err = appendTurnMarker(conv.Manager, turnMarker{EventID: m.EventID, Phase: phaseStarted}); err != nil {
			return err
		}
	}

	if command := parseCommand(m.Text); command != "" {
		return p.runCommand(ctx, adapter, conv, key, m, command)
	}
	return p.runPromptTurn(ctx, adapter, conv, key, lockKey, m, startedID)
}

// recoveredReplyPrefix marks a recovered (possibly duplicated) reply.
const recoveredReplyPrefix = "♻ recovered reply\n\n"

// recoverSettled replays delivery for a turn that settled before the
// delivered marker was written.
func (p *Processor) recoverSettled(ctx context.Context, adapter Adapter, conv *Conversation, key ConversationKey, m Message, ledger turnLedger) error {
	marker := ledger.settled.marker
	resumePreviewID := ""
	if ledger.preview != nil {
		resumePreviewID = ledger.preview.marker.PreviewID
	}
	delivery := adapter.NewDelivery(key, m.EventID, resumePreviewID)
	receipt := Receipt{At: time.Now().UTC()}
	if marker.Outcome == outcomeOK {
		// RecoveredText is set on markers carried across a /new session
		// switch, whose assistant entries stayed behind in the old session.
		text := marker.RecoveredText
		if text == "" {
			text = assistantText(recoveredAssistant(conv.Manager, marker.AssistantEntryID, ledger.settled.entryID))
		}
		delivered, err := p.finalizeWithRetry(ctx, delivery, recoveredReplyPrefix+text)
		if err != nil {
			return err
		}
		receipt = delivered
	} else {
		notice := recoveredReplyPrefix + outcomeNotice(marker.Outcome, "")
		if err := p.notifyWithRetry(ctx, delivery, notice); err != nil {
			return err
		}
	}
	_, err := appendTurnMarker(conv.Manager, turnMarker{EventID: m.EventID, Phase: phaseDelivered, Receipt: &receipt})
	return err
}

// runPromptTurn prompts the agent and delivers the settled reply.
func (p *Processor) runPromptTurn(ctx context.Context, adapter Adapter, conv *Conversation, key ConversationKey, lockKey string, m Message, startedID string) error {
	delivery := adapter.NewDelivery(key, m.EventID, "")
	if err := delivery.Typing(ctx); err != nil {
		p.logger.Debug("chat: typing failed", "error", err)
	}

	// promptCtx is cancelled by /stop so a preemption landing before the agent
	// run starts (attachment downloads, preflight) still aborts the turn
	// instead of silently running it after the user was told "stopped".
	promptCtx, cancelPrompt := context.WithCancel(ctx)
	defer cancelPrompt()

	co := new(coalescer)
	unsubscribe := conv.Session.Subscribe(co.observe)
	previewRecorded := false // renderer-goroutine only; retried until an append succeeds
	onPreview := func(previewID string) {
		if previewRecorded {
			return
		}
		marker := turnMarker{EventID: m.EventID, Phase: phasePreview, PreviewID: previewID}
		if _, err := appendTurnMarker(conv.Manager, marker); err != nil {
			p.logger.Warn("chat: preview marker append failed", "error", err)
			return
		}
		previewRecorded = true
	}
	stopRenderer := startPreviewRenderer(promptCtx, co, delivery, p.previewInterval, onPreview)

	p.setActive(lockKey, func() {
		cancelPrompt()
		conv.Session.Abort()
	})
	text, images := p.buildPrompt(promptCtx, adapter, m)
	// ExpandPromptTemplates=false: a chat user's leading "/..." must reach the
	// model as plain text, never operator-installed extension commands,
	// skills, or prompt templates from the shared agent dir.
	expand := false
	promptErr := conv.Session.PromptWithOptions(promptCtx, text, &codingagent.PromptOptions{
		ExpandPromptTemplates: &expand,
		Images:                images,
	})
	if promptErr == nil {
		promptErr = conv.Session.WaitForIdle(promptCtx)
	}
	p.clearActive(lockKey)
	unsubscribe()
	stopRenderer()

	outcome, finalText, assistantEntryID, notice := settleTurn(conv.Manager, startedID, promptErr)
	if outcome == outcomeError && promptCtx.Err() != nil && ctx.Err() == nil {
		// /stop cancelled the turn before the agent recorded an aborted run.
		outcome, notice = outcomeAborted, outcomeNotice(outcomeAborted, "")
	}
	if _, err := appendTurnMarker(conv.Manager, turnMarker{
		EventID: m.EventID, Phase: phaseSettled,
		Outcome: outcome, AssistantEntryID: assistantEntryID,
	}); err != nil {
		return err
	}

	receipt := Receipt{At: time.Now().UTC()}
	if outcome == outcomeOK {
		delivered, err := p.finalizeWithRetry(ctx, delivery, finalText)
		if err != nil {
			return err
		}
		receipt = delivered
	} else {
		// The turn context may already be expired (timeout/abort); deliver
		// the notice on a bounded fresh context.
		noticeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		err := p.notifyWithRetry(noticeCtx, delivery, notice)
		cancel()
		if err != nil {
			return err
		}
	}
	_, err := appendTurnMarker(conv.Manager, turnMarker{EventID: m.EventID, Phase: phaseDelivered, Receipt: &receipt})
	return err
}

// settleTurn derives the turn outcome from the assistant entry appended after
// the started marker on the current branch.
func settleTurn(manager *sessionstore.SessionManager, startedID string, promptErr error) (outcome, finalText, assistantEntryID, notice string) {
	entry := assistantEntryAfter(manager, startedID)
	if entry == nil {
		reason := "no assistant reply"
		if promptErr != nil {
			reason = promptErr.Error()
		}
		return outcomeError, "", "", outcomeNotice(outcomeError, reason)
	}
	assistant := decodeAssistantEntry(entry)
	switch assistant.StopReason {
	case ai.StopReasonAborted:
		return outcomeAborted, "", entry.ID, outcomeNotice(outcomeAborted, "")
	case ai.StopReasonError:
		reason := ""
		if assistant.ErrorMessage != nil {
			reason = *assistant.ErrorMessage
		}
		return outcomeError, "", entry.ID, outcomeNotice(outcomeError, reason)
	default:
		return outcomeOK, assistantText(assistant), entry.ID, ""
	}
}

// outcomeNotice renders the concise notice for non-ok outcomes.
func outcomeNotice(outcome, reason string) string {
	if outcome == outcomeAborted {
		return "turn aborted"
	}
	if reason == "" {
		return "turn failed"
	}
	return "turn failed: " + reason
}

// buildPrompt assembles the prompt text and image attachments. Group messages
// (ChatType "group") carry sender attribution; photos become image content via
// the adapter, other attachment kinds become bracketed textual notes.
func (p *Processor) buildPrompt(ctx context.Context, adapter Adapter, m Message) (string, []*ai.ImageContent) {
	text := m.Text
	if m.SenderName != "" && m.ChatType == "group" {
		text = m.SenderName + ": " + text
	}
	var images []*ai.ImageContent
	var notes []string
	for _, ref := range m.Attachments {
		if ref.Kind == "photo" {
			if image := p.downloadImage(ctx, adapter, ref); image != nil {
				images = append(images, image)
				continue
			}
		}
		notes = append(notes, attachmentNote(ref))
	}
	if len(notes) > 0 {
		text = strings.TrimSpace(text + "\n\n" + strings.Join(notes, "\n"))
	}
	return text, images
}

func (p *Processor) downloadImage(ctx context.Context, adapter Adapter, ref AttachmentRef) *ai.ImageContent {
	reader, mime, err := adapter.Download(ctx, ref)
	if err != nil {
		p.logger.Warn("chat: attachment download failed", "id", ref.ID, "error", err)
		return nil
	}
	defer func() { _ = reader.Close() }()
	data, err := io.ReadAll(reader)
	if err != nil {
		p.logger.Warn("chat: attachment read failed", "id", ref.ID, "error", err)
		return nil
	}
	if mime == "" {
		mime = ref.MIME
	}
	return &ai.ImageContent{Data: base64.StdEncoding.EncodeToString(data), MimeType: mime}
}

func attachmentNote(ref AttachmentRef) string {
	name := ref.Name
	if name == "" {
		name = ref.ID
	}
	if ref.MIME != "" {
		return fmt.Sprintf("[attachment: %s %s (%s)]", ref.Kind, name, ref.MIME)
	}
	return fmt.Sprintf("[attachment: %s %s]", ref.Kind, name)
}

// runCommand executes a short-circuiting slash command. Command turns skip
// the settled marker: the delivered marker alone dedupes redelivery, and a
// crash before it re-runs the command (possible duplicate notice, honest).
func (p *Processor) runCommand(ctx context.Context, adapter Adapter, conv *Conversation, key ConversationKey, m Message, command string) error {
	delivery := adapter.NewDelivery(key, m.EventID, "")
	var notice string
	switch command {
	case "/new":
		err := startNewSession(conv)
		switch {
		case errors.Is(err, sessionstore.ErrHarnessStorageReplacement):
			notice = "/new is not available for this session provider"
		case err != nil:
			return fmt.Errorf("chat: /new: %w", err)
		default:
			notice = "started a new session"
		}
	case "/status":
		notice = statusSummary(conv)
	case "/compact":
		result, err := conv.Session.Compact(ctx, "")
		switch {
		case err != nil:
			notice = "compaction failed: " + err.Error()
		case result == nil:
			notice = "compaction did not run"
		default:
			notice = fmt.Sprintf("compacted: %d → ~%d tokens", result.TokensBefore, result.EstimatedTokensAfter)
		}
	default:
		return fmt.Errorf("chat: unknown command %q", command)
	}
	if err := p.notifyWithRetry(ctx, delivery, notice); err != nil {
		return err
	}
	receipt := Receipt{At: time.Now().UTC()}
	_, err := appendTurnMarker(conv.Manager, turnMarker{EventID: m.EventID, Phase: phaseDelivered, Receipt: &receipt})
	return err
}

// startNewSession switches conv to a fresh session and makes the switch
// durable and safe: the session store defers flushing a new file until the
// first assistant message, so without an explicit write the next Acquire's
// FindMostRecentSession would silently resume the OLD conversation. Carried
// ledger markers keep redelivered pre-switch events deduplicated (delivered ⇒
// no-op, settled ⇒ never re-prompt) across the switch.
func startNewSession(conv *Conversation) error {
	carried := carryableMarkers(conv.Manager)
	path, err := conv.Manager.NewSession()
	if err != nil {
		return err
	}
	if path != "" {
		data, jsonErr := conv.Manager.JSONL()
		if jsonErr != nil {
			return jsonErr
		}
		if writeErr := os.WriteFile(path, data, 0o666); writeErr != nil {
			return writeErr
		}
		// Re-opening the freshly written file marks the manager flushed, so
		// every later entry (ledger markers included) is persisted eagerly.
		if setErr := conv.Manager.SetSessionFile(path); setErr != nil {
			return setErr
		}
	}
	for _, marker := range carried {
		if _, markerErr := appendTurnMarker(conv.Manager, marker); markerErr != nil {
			return markerErr
		}
	}
	conv.Session.SyncMessagesFromSession()
	return nil
}

// statusSummary renders /status: model, context usage, and summed cost of the
// assistant messages on the current branch.
func statusSummary(conv *Conversation) string {
	var builder strings.Builder
	state := conv.Session.State()
	if state.Model != nil {
		fmt.Fprintf(&builder, "model: %s/%s\n", state.Model.Provider, state.Model.ID)
	} else {
		builder.WriteString("model: none\n")
	}
	if usage := conv.Session.GetContextUsage(); usage != nil && usage.Tokens != nil {
		fmt.Fprintf(&builder, "context: %d / %.0f tokens", *usage.Tokens, usage.ContextWindow)
		if usage.Percent != nil {
			fmt.Fprintf(&builder, " (%.1f%%)", *usage.Percent)
		}
		builder.WriteString("\n")
	} else {
		builder.WriteString("context: unavailable\n")
	}
	var cost float64
	for _, entry := range conv.Manager.GetBranch() {
		if assistant := decodeAssistantEntry(&entry); assistant != nil {
			cost += assistant.Usage.Cost.Total
		}
	}
	fmt.Fprintf(&builder, "cost: $%.4f", cost)
	return builder.String()
}

// parseCommand returns the recognized slash command leading m's text, or "".
func parseCommand(text string) string {
	text = strings.TrimSpace(text)
	if i := strings.IndexFunc(text, unicode.IsSpace); i >= 0 {
		text = text[:i]
	}
	switch text {
	case "/stop", "/new", "/status", "/compact":
		return text
	}
	return ""
}

// deliveryBackoff bounds the retry schedule for Finalize/Notify failures.
var deliveryBackoff = []time.Duration{0, 50 * time.Millisecond, 200 * time.Millisecond}

func (p *Processor) finalizeWithRetry(ctx context.Context, delivery Delivery, text string) (Receipt, error) {
	var lastErr error
	for _, delay := range deliveryBackoff {
		if err := sleepContext(ctx, delay); err != nil {
			return Receipt{}, err
		}
		receipt, err := delivery.Finalize(ctx, text)
		if err == nil {
			return receipt, nil
		}
		lastErr = err
	}
	return Receipt{}, fmt.Errorf("chat: finalize delivery: %w", lastErr)
}

func (p *Processor) notifyWithRetry(ctx context.Context, delivery Delivery, text string) error {
	var lastErr error
	for _, delay := range deliveryBackoff {
		if err := sleepContext(ctx, delay); err != nil {
			return err
		}
		err := delivery.Notify(ctx, text)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("chat: notify delivery: %w", lastErr)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// keyedMutex is a refcounted per-key mutex: the map entry is removed at
// refcount zero, so an idle conversation retains zero goroutines and zero
// map entries.
type keyedMutex struct {
	mu      sync.Mutex
	entries map[string]*keyedMutexEntry
}

type keyedMutexEntry struct {
	refs int
	sem  chan struct{}
}

func newKeyedMutex() *keyedMutex {
	return &keyedMutex{entries: map[string]*keyedMutexEntry{}}
}

// Lock acquires the key's mutex, honoring ctx cancellation while waiting.
func (m *keyedMutex) Lock(ctx context.Context, key string) error {
	m.mu.Lock()
	entry := m.entries[key]
	if entry == nil {
		entry = &keyedMutexEntry{sem: make(chan struct{}, 1)}
		m.entries[key] = entry
	}
	entry.refs++
	m.mu.Unlock()
	select {
	case entry.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		m.release(key, entry)
		return ctx.Err()
	}
}

// Unlock releases the key's mutex and drops the refcount.
func (m *keyedMutex) Unlock(key string) {
	m.mu.Lock()
	entry := m.entries[key]
	m.mu.Unlock()
	if entry == nil {
		return
	}
	<-entry.sem
	m.release(key, entry)
}

func (m *keyedMutex) release(key string, entry *keyedMutexEntry) {
	m.mu.Lock()
	entry.refs--
	if entry.refs == 0 {
		delete(m.entries, key)
	}
	m.mu.Unlock()
}

// size reports the live entry count (idle-residue checks in tests).
func (m *keyedMutex) size() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}
