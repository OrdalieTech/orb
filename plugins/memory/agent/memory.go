// Package agentmemory attaches Orb's OptChat timeline memory to an engine.Agent.
// The bundled memory extension delegates to the same Runtime.
package agentmemory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
)

const (
	maxWorkSummaryBytes = 480
	maxCommandPreview   = 80
	memoryHintLine      = "Use the `memory` tool (search, zoom, date, profile) to inspect older timeline entries or update the user profile."
	changesHeader       = "Memory updated by another session since last turn:"
)

var memoryToolSchema = ai.JSONSchema(`{
  "type": "object",
  "required": ["action"],
  "properties": {
    "action": {
      "type": "string",
      "enum": ["search", "zoom", "date", "profile"],
      "description": "Operation to perform: search timeline/summaries, zoom into an entry or summary node, inspect entry date/metadata, or edit USER PROFILE."
    },
    "query": {
      "type": "string",
      "description": "Search query for action=search, or fallback substring for action=profile remove."
    },
    "limit": {
      "type": "integer",
      "minimum": 1,
      "maximum": 50,
      "description": "Maximum search hits to return (default 20)."
    },
    "id": {
      "type": "integer",
      "minimum": 0,
      "description": "Entry ID for action=zoom or action=date."
    },
    "n": {
      "type": "integer",
      "minimum": 1,
      "description": "Span for action=zoom (1 for verbatim entry, power of 2 for summary node; default 1)."
    },
    "depth": {
      "type": "integer",
      "minimum": 1,
      "maximum": 3,
      "description": "Expansion depth for action=zoom on a summary node (default 1, max 3)."
    },
    "offset": {
      "type": "integer",
      "minimum": 0,
      "description": "Byte offset for paging a long verbatim entry in action=zoom (default 0)."
    },
    "op": {
      "type": "string",
      "enum": ["set", "add", "remove"],
      "description": "Profile mutation operation for action=profile."
    },
    "content": {
      "type": "string",
      "description": "Profile fact text for action=profile (add or set)."
    },
    "old_text": {
      "type": "string",
      "description": "Unique substring or profile entry ID to target for action=profile (set or remove)."
    }
  }
}`)

// Status summarizes the current state of the memory store and view.
type Status struct {
	Entries      int
	Nodes        int
	ProfileItems int
	ViewBytes    int
	HighBytes    int
	LowBytes     int
	Peer         string
	Session      string
}

// Option configures a Runtime.
type Option func(*Runtime)

// WithViewBytes sets the high-water byte budget for the rendered memory view.
func WithViewBytes(n int) Option {
	return func(r *Runtime) {
		r.viewBytes = n
	}
}

// WithSummarizer sets the optional LLM summarizer used by the background compactor.
func WithSummarizer(fn memorysdk.Summarizer) Option {
	return func(r *Runtime) {
		r.summarizer = fn
	}
}

// WithPeer sets the peer identifier attached to recorded timeline entries.
func WithPeer(peer string) Option {
	return func(r *Runtime) {
		r.peer = strings.TrimSpace(peer)
	}
}

// WithSession sets the session identifier attached to recorded timeline entries.
func WithSession(session string) Option {
	return func(r *Runtime) {
		r.session = strings.TrimSpace(session)
	}
}

// WithOnWrite sets an optional callback invoked after durable writes.
func WithOnWrite(fn func()) Option {
	return func(r *Runtime) {
		r.onWrite = fn
	}
}

// Runtime owns one session's frozen OptChat memory view, cross-session delta
// tracking, turn recording, background compaction, and the unified memory tool.
type Runtime struct {
	store      memorysdk.Store
	viewBytes  int
	summarizer memorysdk.Summarizer
	onWrite    func()

	metaMu  sync.RWMutex
	peer    string
	session string

	storeMu sync.Mutex

	stateMu      sync.RWMutex
	frozenPrompt string
	view         *memorysdk.View
	seenEntries  int
	seenProfile  map[string]string

	compMu    sync.Mutex
	compactor *memorysdk.Compactor
	compCtx   context.Context

	asyncMu sync.Mutex
	closed  bool
	wg      sync.WaitGroup
}

// New creates a memory Runtime over store.
func New(store memorysdk.Store, opts ...Option) (*Runtime, error) {
	if store == nil {
		return nil, fmt.Errorf("memory: store is required")
	}
	rt := &Runtime{
		store:       store,
		viewBytes:   memorysdk.DefaultViewBytes,
		seenProfile: make(map[string]string),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(rt)
		}
	}
	rt.compactor = memorysdk.NewCompactor(store, rt.summarizer, rt.onWrite)
	return rt, nil
}

// SetSession updates the session identifier attached to newly recorded entries.
func (rt *Runtime) SetSession(sessionID string) {
	rt.metaMu.Lock()
	rt.session = strings.TrimSpace(sessionID)
	rt.metaMu.Unlock()
}

// SetSummarizer replaces the compactor's LLM summarizer. If the compactor was
// already started, it is restarted cleanly with the new summarizer.
func (rt *Runtime) SetSummarizer(fn memorysdk.Summarizer) {
	rt.compMu.Lock()
	old := rt.compactor
	ctx := rt.compCtx
	rt.summarizer = fn
	rt.compactor = memorysdk.NewCompactor(rt.store, fn, rt.onWrite)
	next := rt.compactor
	rt.compMu.Unlock()

	if old != nil {
		old.Stop()
	}
	if ctx != nil && next != nil {
		next.Start(ctx)
	}
}

// StartCompactor starts the non-blocking background compaction worker.
func (rt *Runtime) StartCompactor(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	rt.compMu.Lock()
	rt.compCtx = ctx
	c := rt.compactor
	rt.compMu.Unlock()
	if c != nil {
		c.Start(ctx)
	}
}

// CompactNow runs a synchronous compaction pass and returns the number of nodes written.
func (rt *Runtime) CompactNow(ctx context.Context) (int, error) {
	rt.compMu.Lock()
	c := rt.compactor
	rt.compMu.Unlock()
	if c == nil {
		return 0, nil
	}
	return c.CompactOnce(ctx, 32)
}

// Load builds and freezes USER PROFILE + <memory_view> for the session so that
// SystemPrompt stays 100% prompt-cache stable across turns.
func (rt *Runtime) Load(ctx context.Context) error {
	rt.storeMu.Lock()
	profile, err := rt.store.GetProfile(ctx)
	if err != nil {
		rt.storeMu.Unlock()
		return err
	}
	view, err := memorysdk.BuildView(ctx, rt.store, nil, rt.viewBytes)
	rt.storeMu.Unlock()
	if err != nil {
		return err
	}

	frozen := renderFrozenSection(profile, view)
	seenProf := profileSnapshot(profile)

	rt.stateMu.Lock()
	rt.frozenPrompt = frozen
	rt.view = view
	rt.seenEntries = view.EntryCount
	rt.seenProfile = seenProf
	rt.stateMu.Unlock()
	return nil
}

// SystemPrompt appends the frozen USER PROFILE and <memory_view> to base.
func (rt *Runtime) SystemPrompt(base string) string {
	rt.stateMu.RLock()
	frozen := rt.frozenPrompt
	rt.stateMu.RUnlock()
	if frozen == "" {
		return base
	}
	if base == "" {
		return frozen
	}
	return base + "\n\n" + frozen
}

// Changes returns a compact delta of entries or profile edits made by other
// sessions since this runtime last observed the store, and advances seen state.
func (rt *Runtime) Changes(ctx context.Context) (string, error) {
	rt.stateMu.RLock()
	lastEntries := rt.seenEntries
	rt.stateMu.RUnlock()

	count, err := rt.store.EntryCount(ctx)
	if err != nil {
		return "", err
	}
	var newEntries []memorysdk.Entry
	if count > lastEntries {
		newEntries, err = rt.store.ListEntries(ctx, lastEntries, -1)
		if err != nil {
			return "", err
		}
	}
	profile, err := rt.store.GetProfile(ctx)
	if err != nil {
		return "", err
	}
	currentProf := profileSnapshot(profile)

	rt.stateMu.Lock()
	var lines []string
	for _, e := range newEntries {
		if e.ID >= rt.seenEntries {
			lines = append(lines, fmt.Sprintf("+ %d+1: %s", e.ID, truncateUTF8(memorysdk.FormatEntryLine(e), 240)))
		}
	}
	var profLines []string
	for id, content := range currentProf {
		if rt.seenProfile[id] != content {
			profLines = append(profLines, "+ USER PROFILE: "+content)
		}
	}
	for id, content := range rt.seenProfile {
		if _, ok := currentProf[id]; !ok {
			profLines = append(profLines, "- USER PROFILE: "+content)
		}
	}
	slices.Sort(profLines)
	lines = append(lines, profLines...)

	if count > rt.seenEntries {
		rt.seenEntries = count
	}
	rt.seenProfile = currentProf
	rt.stateMu.Unlock()

	if len(lines) == 0 {
		return "", nil
	}
	return changesHeader + "\n" + strings.Join(lines, "\n"), nil
}

// RecordTurn extracts user prompts, at most one compact work summary (<= 480 B),
// and the final assistant talk reply from messages, appends them to the store,
// advances seen state, fires onWrite, and triggers background compaction.
func (rt *Runtime) RecordTurn(ctx context.Context, messages []ai.Message) error {
	if len(messages) == 0 {
		return nil
	}
	rt.metaMu.RLock()
	peer := rt.peer
	session := rt.session
	rt.metaMu.RUnlock()

	toAppend := extractTurnEntries(messages, peer, session)
	if len(toAppend) == 0 {
		return nil
	}

	rt.storeMu.Lock()
	lastID := -1
	for _, e := range toAppend {
		saved, err := rt.store.AppendEntry(ctx, e)
		if err != nil {
			rt.storeMu.Unlock()
			return err
		}
		lastID = saved.ID
	}
	rt.storeMu.Unlock()

	if lastID >= 0 {
		rt.stateMu.Lock()
		if lastID+1 > rt.seenEntries {
			rt.seenEntries = lastID + 1
		}
		rt.stateMu.Unlock()
	}

	if rt.onWrite != nil {
		rt.onWrite()
	}
	rt.compMu.Lock()
	c := rt.compactor
	rt.compMu.Unlock()
	if c != nil {
		c.Trigger()
	}
	return nil
}

// RecordTurnAsync records a turn on a tracked background goroutine without
// blocking the caller. Call Close() or Wait() to drain cleanly.
func (rt *Runtime) RecordTurnAsync(messages []ai.Message) {
	if len(messages) == 0 {
		return
	}
	cp := make([]ai.Message, len(messages))
	copy(cp, messages)

	rt.asyncMu.Lock()
	if rt.closed {
		rt.asyncMu.Unlock()
		return
	}
	rt.wg.Add(1)
	rt.asyncMu.Unlock()

	go func() {
		defer rt.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = rt.RecordTurn(ctx, cp)
	}()
}

// Wait blocks until all in-flight RecordTurnAsync goroutines complete.
func (rt *Runtime) Wait() {
	rt.wg.Wait()
}

// Close stops accepting new async turn recordings, drains in-flight recordings,
// and stops the background compactor. Safe to call multiple times.
func (rt *Runtime) Close() {
	rt.asyncMu.Lock()
	rt.closed = true
	rt.asyncMu.Unlock()

	rt.wg.Wait()

	rt.compMu.Lock()
	c := rt.compactor
	rt.compMu.Unlock()
	if c != nil {
		c.Stop()
	}
}

// Status returns a snapshot of store and view metrics.
func (rt *Runtime) Status(ctx context.Context) (Status, error) {
	entries, err := rt.store.EntryCount(ctx)
	if err != nil {
		return Status{}, err
	}
	nodes, err := rt.store.ListNodes(ctx)
	if err != nil {
		return Status{}, err
	}
	profile, err := rt.store.GetProfile(ctx)
	if err != nil {
		return Status{}, err
	}
	view, err := memorysdk.BuildView(ctx, rt.store, nil, rt.viewBytes)
	if err != nil {
		return Status{}, err
	}
	rt.metaMu.RLock()
	peer := rt.peer
	session := rt.session
	rt.metaMu.RUnlock()

	return Status{
		Entries:      entries,
		Nodes:        len(nodes),
		ProfileItems: len(profile),
		ViewBytes:    view.Bytes,
		HighBytes:    view.HighBytes,
		LowBytes:     view.LowBytes,
		Peer:         peer,
		Session:      session,
	}, nil
}

// ViewText renders the current USER PROFILE + <memory_view> directly from the store.
func (rt *Runtime) ViewText(ctx context.Context) (string, error) {
	profile, err := rt.store.GetProfile(ctx)
	if err != nil {
		return "", err
	}
	view, err := memorysdk.BuildView(ctx, rt.store, nil, rt.viewBytes)
	if err != nil {
		return "", err
	}
	return renderFrozenSection(profile, view), nil
}

// Tools returns the single unified `memory` tool bound to this Runtime.
func (rt *Runtime) Tools() []engine.AgentTool {
	return []engine.AgentTool{
		engine.AgentToolFunc{
			AgentToolSpec: engine.AgentToolSpec{
				Name:          "memory",
				Label:         "Memory",
				Description:   "Search, zoom, inspect dates in the OptChat memory timeline, or update the curated USER PROFILE.",
				Parameters:    memoryToolSchema,
				ExecutionMode: engine.ToolExecutionSequential,
			},
			Run: rt.runMemoryTool,
		},
	}
}

func (rt *Runtime) runMemoryTool(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
	var input struct {
		Action  string `json:"action"`
		Query   string `json:"query"`
		Limit   int    `json:"limit"`
		ID      int    `json:"id"`
		N       int    `json:"n"`
		Depth   int    `json:"depth"`
		Offset  int    `json:"offset"`
		Op      string `json:"op"`
		Content string `json:"content"`
		OldText string `json:"old_text"`
	}
	if err := decode(raw, &input); err != nil {
		return engine.AgentToolResult{}, err
	}
	switch strings.ToLower(strings.TrimSpace(input.Action)) {
	case "search":
		q := strings.TrimSpace(input.Query)
		if q == "" {
			return engine.AgentToolResult{}, fmt.Errorf("memory search: query is required")
		}
		hits, err := memorysdk.Search(ctx, rt.store, q, input.Limit)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		if len(hits) == 0 {
			return textResult("No memory matches found."), nil
		}
		lines := make([]string, len(hits))
		for i, h := range hits {
			ts := ""
			if !h.TS.IsZero() {
				ts = h.TS.UTC().Format("2006-01-02T15:04Z") + " "
			}
			lines[i] = fmt.Sprintf("%s %s%s", h.Ref, ts, h.Snippet)
		}
		return textResult(strings.Join(lines, "\n")), nil

	case "zoom":
		n := input.N
		if n <= 0 {
			n = 1
		}
		depth := input.Depth
		if depth <= 0 {
			depth = 1
		}
		offset := input.Offset
		if offset < 0 {
			offset = 0
		}
		out, err := memorysdk.Zoom(ctx, rt.store, input.ID, n, depth, offset)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		return textResult(out), nil

	case "date":
		out, err := memorysdk.Date(ctx, rt.store, input.ID)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		return textResult(out), nil

	case "profile":
		return rt.runProfileAction(ctx, input.Op, input.Content, input.OldText, input.Query)

	default:
		return engine.AgentToolResult{}, fmt.Errorf("memory: unsupported action %q (expected search, zoom, date, or profile)", input.Action)
	}
}

func (rt *Runtime) runProfileAction(ctx context.Context, op, content, oldText, query string) (engine.AgentToolResult, error) {
	op = strings.ToLower(strings.TrimSpace(op))
	content = strings.TrimSpace(content)
	oldText = strings.TrimSpace(oldText)
	if oldText == "" {
		oldText = strings.TrimSpace(query)
	}

	rt.storeMu.Lock()
	defer rt.storeMu.Unlock()

	entries, err := rt.store.GetProfile(ctx)
	if err != nil {
		return engine.AgentToolResult{}, err
	}

	now := time.Now().UTC()
	var next []memorysdk.ProfileEntry
	var msg string

	switch op {
	case "add":
		added, entry, err := memorysdk.ProfileAdd(entries, content, now)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		next = added
		msg = fmt.Sprintf("Profile entry saved (%s).", entry.ID)

	case "set":
		if content == "" {
			return engine.AgentToolResult{}, fmt.Errorf("memory profile set: content is required")
		}
		if oldText == "" {
			added, entry, err := memorysdk.ProfileAdd(entries, content, now)
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			next = added
			msg = fmt.Sprintf("Profile entry saved (%s).", entry.ID)
		} else {
			targetID, err := findProfileTargetID(entries, oldText)
			if err != nil {
				return engine.AgentToolResult{}, fmt.Errorf("memory profile set: %w", err)
			}
			updated, entry, err := memorysdk.ProfileSet(entries, targetID, content, now)
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			next = updated
			msg = fmt.Sprintf("Profile entry updated (%s).", entry.ID)
		}

	case "remove":
		if oldText == "" {
			return engine.AgentToolResult{}, fmt.Errorf("memory profile remove: old_text is required")
		}
		targetID, err := findProfileTargetID(entries, oldText)
		if err != nil {
			return engine.AgentToolResult{}, fmt.Errorf("memory profile remove: %w", err)
		}
		removed, ok := memorysdk.ProfileRemove(entries, targetID)
		if !ok {
			return engine.AgentToolResult{}, fmt.Errorf("memory profile remove: entry %q not found", targetID)
		}
		next = removed
		msg = fmt.Sprintf("Profile entry removed (%s).", targetID)

	default:
		return engine.AgentToolResult{}, fmt.Errorf("memory profile: unsupported op %q (expected add, set, or remove)", op)
	}

	if err := rt.store.SaveProfile(ctx, next); err != nil {
		return engine.AgentToolResult{}, err
	}

	rt.stateMu.Lock()
	rt.seenProfile = profileSnapshot(next)
	rt.stateMu.Unlock()

	if rt.onWrite != nil {
		rt.onWrite()
	}
	return textResult(msg), nil
}

func findProfileTargetID(entries []memorysdk.ProfileEntry, selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return "", fmt.Errorf("selector is required")
	}
	// Exact ID match first.
	for _, e := range entries {
		if e.ID == selector {
			return e.ID, nil
		}
	}
	// Unique case-insensitive substring match.
	lower := strings.ToLower(selector)
	var matches []string
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Content), lower) {
			matches = append(matches, e.ID)
		}
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no profile entry matches %q", selector)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple profile entries match %q; use a more specific substring", selector)
	}
	return matches[0], nil
}

// Attach loads a frozen snapshot and attaches the unified memory tool to a plain Agent.
func Attach(ctx context.Context, target *engine.Agent, store memorysdk.Store, opts ...Option) error {
	if target == nil {
		return fmt.Errorf("memory: agent is required")
	}
	rt, err := New(store, opts...)
	if err != nil {
		return err
	}
	if err := rt.Load(ctx); err != nil {
		return err
	}
	state := target.State()
	tools := rt.Tools()
	for _, existing := range state.Tools {
		for _, added := range tools {
			if existing.Spec().Name == added.Spec().Name {
				return fmt.Errorf("memory: tool %q is already registered", added.Spec().Name)
			}
		}
	}
	if prompt := rt.SystemPrompt(""); prompt != "" {
		target.SetMessages(append(state.Messages, &ai.SystemMessage{
			Sections:  ai.SystemPromptSections{{Name: "memory", Text: &prompt}},
			Timestamp: time.Now().UnixMilli(),
		}))
	}
	target.SetTools(append(state.Tools, tools...))
	return nil
}

func renderFrozenSection(profile []memorysdk.ProfileEntry, view *memorysdk.View) string {
	var sections []string
	if profText := memorysdk.RenderProfile(profile); profText != "" {
		sections = append(sections, fmt.Sprintf("USER PROFILE [%d/%d chars]\n%s",
			utf8.RuneCountInString(profText), memorysdk.ProfileMaxChars, profText))
	}
	if view != nil && len(view.Lines) > 0 {
		sections = append(sections, "<memory_view>\n"+view.Render()+"\n</memory_view>")
	} else {
		sections = append(sections, "<memory_view>\n"+memoryHintLine+"\n</memory_view>")
	}
	return strings.Join(sections, "\n\n")
}

func profileSnapshot(entries []memorysdk.ProfileEntry) map[string]string {
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if c := strings.TrimSpace(e.Content); c != "" {
			out[e.ID] = c
		}
	}
	return out
}

// extractTurnEntries extracts user prompt(s), at most 1 compact work summary
// (<= maxWorkSummaryBytes), and the final assistant talk reply from a turn.
func extractTurnEntries(messages []ai.Message, peer, session string) []memorysdk.Entry {
	now := time.Now().UTC()
	var userTexts []string
	var workParts []string
	var lastTalk string

	// Map toolCallID -> error flag from tool results.
	toolErrors := make(map[string]bool)
	for _, raw := range messages {
		if tr, ok := asToolResultMessage(raw); ok && tr.IsError {
			toolErrors[tr.ToolCallID] = true
		}
	}

	for _, raw := range messages {
		switch msg := raw.(type) {
		case *ai.UserMessage:
			if msg != nil {
				if txt := extractUserText(msg.Content); txt != "" {
					userTexts = append(userTexts, txt)
				}
			}
		case *ai.AssistantMessage:
			if msg != nil {
				txt, calls := extractAssistantParts(msg.Content, toolErrors)
				workParts = append(workParts, calls...)
				if txt != "" {
					lastTalk = txt
				}
			}
		}
	}

	var entries []memorysdk.Entry
	for _, u := range userTexts {
		entries = append(entries, memorysdk.Entry{
			Kind:    memorysdk.KindUser,
			Text:    u,
			Peer:    peer,
			Session: session,
			TS:      now,
		})
	}

	if len(workParts) > 0 {
		workText := formatCompactWork(workParts, peer, session)
		if workText != "" {
			entries = append(entries, memorysdk.Entry{
				Kind:    memorysdk.KindWork,
				Text:    workText,
				Peer:    peer,
				Session: session,
				TS:      now,
			})
		}
	}

	if lastTalk != "" {
		entries = append(entries, memorysdk.Entry{
			Kind:    memorysdk.KindTalk,
			Text:    lastTalk,
			Peer:    peer,
			Session: session,
			TS:      now,
		})
	}

	return entries
}

func asToolResultMessage(raw ai.Message) (ai.ToolResultMessage, bool) {
	if m, ok := raw.(*ai.ToolResultMessage); ok && m != nil {
		return *m, true
	}
	return ai.ToolResultMessage{}, false
}

func extractUserText(content ai.UserContent) string {
	if content.Text != nil {
		return strings.TrimSpace(*content.Text)
	}
	var parts []string
	for _, block := range content.Blocks {
		if tb, ok := block.(*ai.TextContent); ok && tb != nil {
			if t := strings.TrimSpace(tb.Text); t != "" {
				parts = append(parts, t)
			}
		}
	}
	return strings.Join(parts, "\n")
}

func extractAssistantParts(content ai.AssistantContent, toolErrors map[string]bool) (string, []string) {
	var textParts []string
	var workParts []string
	for _, block := range content {
		switch b := block.(type) {
		case *ai.TextContent:
			if b != nil {
				if t := strings.TrimSpace(b.Text); t != "" {
					textParts = append(textParts, t)
				}
			}
		case *ai.ToolCall:
			if b != nil {
				workParts = append(workParts, summarizeToolCall(*b, toolErrors[b.ID]))
			}
		}
	}
	return strings.Join(textParts, "\n"), workParts
}

func summarizeToolCall(tc ai.ToolCall, isErr bool) string {
	name := strings.TrimSpace(tc.Name)
	if name == "" {
		name = "tool"
	}
	target := extractToolTarget(tc.Arguments)
	var b strings.Builder
	b.WriteString(name)
	if target != "" {
		b.WriteByte('(')
		b.WriteString(target)
		b.WriteByte(')')
	}
	if isErr {
		b.WriteString("[ERR]")
	}
	return b.String()
}

func extractToolTarget(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	for _, key := range []string{"path", "file_path", "file", "target", "url", "pattern", "query", "action"} {
		if v, ok := args[key].(string); ok {
			v = strings.Join(strings.Fields(v), " ")
			if v != "" {
				return truncateUTF8(v, maxCommandPreview)
			}
		}
	}
	if cmd, ok := args["command"].(string); ok {
		cmd = strings.Join(strings.Fields(cmd), " ")
		if cmd != "" {
			return truncateUTF8(cmd, maxCommandPreview)
		}
	}
	return ""
}

func formatCompactWork(parts []string, peer, session string) string {
	if len(parts) == 0 {
		return ""
	}
	suffix := ""
	if peer != "" || session != "" {
		suffix = fmt.Sprintf(" [%s, %s]", peer, session)
	}
	budget := maxWorkSummaryBytes - len(suffix)
	if budget < 32 {
		budget = maxWorkSummaryBytes
		suffix = ""
	}
	body := strings.Join(parts, "; ")
	if len(body) > budget {
		const ellipsis = "…"
		body = strings.TrimRight(truncateUTF8(body, budget-len(ellipsis)), " ;,") + ellipsis
	}
	return body + suffix
}

func truncateUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	n := maxBytes
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func decode(raw, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func textResult(text string) engine.AgentToolResult {
	return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
}
