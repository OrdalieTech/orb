package memtree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
)

const (
	nodeBytes   = 512     // target size of one summary line
	viewBytes   = 128_000 // view budget on windows of 256k tokens and more
	jobs        = 8       // compactor calls running at once
	tries       = 5       // attempts per node to get under nodeBytes
	retryAfter  = 10 * time.Second
	callTimeout = 5 * time.Minute
	unbuilt     = "(not summarized yet: zoom it)"
)

// message is one item of the log: a session entry yields one per user prompt, reply text,
// tool call, tool result or harness note, in branch order.
type message struct {
	key  string // entryID/part, the same on every branch through the entry
	at   int    // index of its entry on the branch
	line string // "kind: text"
	time string
}

// nodeKey names node (level, i) by its last message, which fixes the whole branch before it.
type nodeKey struct {
	Level int    `json:"l"`
	End   string `json:"k"`
}

// part is a view line: node (l, i), covering messages [i·2^l, (i+1)·2^l).
type part struct{ l, i int }

func (p part) start() int { return p.i << p.l }

type ask func(context.Context, ai.MessageList) (*ai.AssistantMessage, error)

// tree is one session's summary tree and view, built from its current branch.
type tree struct {
	sessions extensions.ReadonlySessionManager
	id       string
	save     func(nodeKey, string) // persists a built node
	ctx      context.Context
	stop     context.CancelFunc

	mu       sync.Mutex
	ask      ask
	budget   int
	status   func(string)
	nodes    map[nodeKey]string
	pending  []nodeKey // built, not yet saved
	busy     map[nodeKey]bool
	failing  map[nodeKey]bool
	path     []string // entry IDs of the folded branch
	log      []message
	view     []part
	size     int
	next     []int // per level, the first node not known to be built; next[0] is the frontier
	progress chan struct{}
}

// newTree opens a session's tree with the nodes its memtree entries hold, on every branch.
func newTree(sessions extensions.ReadonlySessionManager, save func(nodeKey, string)) *tree {
	ctx, stop := context.WithCancel(context.Background())
	t := &tree{
		sessions: sessions, id: sessions.GetSessionID(), save: save, ctx: ctx, stop: stop, budget: viewBytes, status: func(string) {},
		nodes: map[nodeKey]string{}, busy: map[nodeKey]bool{}, failing: map[nodeKey]bool{}, progress: make(chan struct{}),
	}
	for _, entry := range sessions.GetEntries() {
		var node savedNode
		if entry.Type == "custom" && entry.CustomType == nodeType && json.Unmarshal(entry.Data, &node) == nil && node.Text != "" {
			t.nodes[node.nodeKey] = node.Text
		}
	}
	return t
}

// savedNode is a memtree session entry: the tree is a cache of model calls, kept with the session
// so it follows it to every host, fork and export.
type savedNode struct {
	nodeKey
	Text string `json:"t"`
}

const nodeType = "memtree"

func (t *tree) saveLocked(key nodeKey, text string) {
	t.nodes[key] = text
	t.pending = append(t.pending, key)
}

// syncLocked folds the entries added to the branch since the last call, walking back from the
// leaf to the folded branch's last entry. A branch that does not extend the folded one (a /tree
// move, a rewound prompt) is folded again from message 0.
func (t *tree) syncLocked() {
	if t.sessions.GetSessionID() != t.id {
		return
	}
	var added []session.SessionEntry // newest first
	id := t.sessions.GetLeafID()
	for id != nil && (len(t.path) == 0 || *id != t.path[len(t.path)-1]) {
		entry := t.sessions.GetEntry(*id)
		if entry == nil {
			id = nil
			break
		}
		added, id = append(added, *entry), entry.ParentID
	}
	if id == nil && len(t.path) > 0 {
		t.path, t.log, t.view, t.size, t.next = nil, nil, nil, 0, nil
	}
	if len(t.next) == 0 {
		t.next = []int{0}
	}
	for k := len(added) - 1; k >= 0; k-- {
		t.path = append(t.path, added[k].ID)
		for _, message := range entryMessages(added[k]) {
			message.at = len(t.path) - 1
			t.log = append(t.log, message)
			t.view = append(t.view, part{0, len(t.log) - 1})
			t.size += t.partSizeLocked(part{0, len(t.log) - 1})
			for t.next[0] < len(t.log) && t.builtLocked(0, t.next[0]) {
				t.next[0]++
			}
			t.fitLocked()
		}
	}
}

// fitLocked merges the most due pair of adjacent sibling lines until the view fits its budget.
// A pair is due by its age over its weight, so detail fades with age while every level keeps
// about as many lines; lines are never split again, which keeps the view's start stable.
func (t *tree) fitLocked() {
	total := len(t.log)
	for t.size > t.budget {
		best, due := -1, 0.0
		// Parents are built only up to the frontier, so the scan stops there.
		for index := 0; index+1 < len(t.view) && t.view[index+1].start() < t.frontierLocked(); index++ {
			a, b := t.view[index], t.view[index+1]
			if a.l != b.l || a.i%2 != 0 || b.i != a.i+1 || !t.builtLocked(a.l+1, a.i/2) {
				continue
			}
			if value := float64(total-a.start()) / float64(int(1)<<(a.l+2)); best < 0 || value > due {
				best, due = index, value
			}
		}
		if best < 0 {
			return // over budget until a parent is built
		}
		a, b := t.view[best], t.view[best+1]
		merged := part{a.l + 1, a.i / 2}
		t.size += t.partSizeLocked(merged) - t.partSizeLocked(a) - t.partSizeLocked(b)
		t.view = append(t.view[:best+1], t.view[best+2:]...)
		t.view[best] = merged
	}
}

func (t *tree) keyLocked(l, i int) nodeKey {
	return nodeKey{l, t.log[(i+1)<<l-1].key}
}

// textLocked is node (l, i), or "" when it is not built. A message that fits is its own node.
func (t *tree) textLocked(l, i int) string {
	if l == 0 && len(t.log[i].line) <= nodeBytes {
		return t.log[i].line
	}
	return t.nodes[t.keyLocked(l, i)]
}

func (t *tree) builtLocked(l, i int) bool { return t.textLocked(l, i) != "" }

func (t *tree) partSizeLocked(p part) int {
	if text := t.textLocked(p.l, p.i); text != "" {
		return len(text)
	}
	return len(unbuilt)
}

// endLocked is where node (l, i)'s context ends: before a message, or after a merged stretch.
func (t *tree) endLocked(l, i int) int {
	if l == 0 {
		return i
	}
	return (i + 1) << l
}

func (t *tree) frontierLocked() int {
	if len(t.next) == 0 {
		return 0
	}
	return t.next[0]
}

// pump starts every node whose sources are built and whose context is summarized: messages are
// compressed one at a time, in order, while merges of finished stretches run alongside, so the
// compactor never sees a line that is not a summary.
func (t *tree) pump() {
	t.mu.Lock()
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		return
	}
	t.pumpLocked()
	// Wake settle: the frontier may have moved over messages short enough to be their own lines.
	close(t.progress)
	t.progress = make(chan struct{})
	saved := make([]savedNode, len(t.pending))
	for index, key := range t.pending {
		saved[index] = savedNode{key, t.nodes[key]}
	}
	t.pending = nil
	t.mu.Unlock()
	for _, node := range saved {
		t.save(node.nodeKey, node.Text)
	}
}

func (t *tree) pumpLocked() {
	t.syncLocked()
	total := len(t.log)
	for l := 0; 1<<l <= total; l++ {
		if l == len(t.next) {
			t.next = append(t.next, 0)
		}
		for t.next[l] < total>>l && t.builtLocked(l, t.next[l]) {
			t.next[l]++
		}
		for i := t.next[l]; i < total>>l && len(t.busy) < jobs; i++ {
			if t.endLocked(l, i) > t.frontierLocked() {
				break
			}
			key := t.keyLocked(l, i)
			if t.busy[key] || t.builtLocked(l, i) || (l > 0 && (!t.builtLocked(l-1, 2*i) || !t.builtLocked(l-1, 2*i+1))) {
				continue
			}
			t.startLocked(l, i, key)
		}
	}
	t.fitLocked() // free merges may have built parents
}

func (t *tree) startLocked(l, i int, key nodeKey) {
	var step string
	if l == 0 {
		step = "Compress this message into one line, in at most 512 bytes:\n" + t.log[i].line
	} else {
		a, b := flat(t.textLocked(l-1, 2*i)), flat(t.textLocked(l-1, 2*i+1))
		if len(a)+1+len(b) <= nodeBytes {
			t.saveLocked(key, a+"\n"+b)
			return
		}
		step = "Merge these two lines into one, in at most 512 bytes:\n" + a + "\n" + b
	}
	var chat strings.Builder
	for _, p := range t.view {
		if p.start() >= t.endLocked(l, i) {
			break
		}
		chat.WriteString(flat(t.textLocked(p.l, p.i)) + "\n")
	}
	request := ai.MessageList{&ai.UserMessage{Content: ai.UserContent{Blocks: ai.UserContentBlocks{
		&ai.TextContent{Text: "<chat>\n" + chat.String() + "</chat>"},
		&ai.TextContent{Text: step},
	}}}}
	t.busy[key] = true
	go t.build(key, t.ask, request)
}

func (t *tree) build(key nodeKey, ask ask, request ai.MessageList) {
	text, err := compress(t.ctx, ask, request)
	t.mu.Lock()
	if err != nil {
		if !t.failing[key] && t.ctx.Err() == nil {
			t.status("compactor failed: " + err.Error())
		}
		t.failing[key] = true
		t.mu.Unlock()
		time.AfterFunc(retryAfter, func() {
			t.mu.Lock()
			delete(t.busy, key)
			t.mu.Unlock()
			t.pump()
		})
		return
	}
	if t.failing[key] {
		delete(t.failing, key)
		t.status("")
	}
	delete(t.busy, key)
	t.saveLocked(key, text)
	// The line replaces a placeholder when it is in the view.
	t.size = 0
	for _, p := range t.view {
		t.size += t.partSizeLocked(p)
	}
	t.fitLocked()
	t.mu.Unlock()
	t.pump()
}

// compress asks for one line and, while it is over nodeBytes, shows the model where the limit
// cuts it, in the same conversation; models can't count bytes. The shortest try is kept.
func compress(ctx context.Context, ask ask, request ai.MessageList) (string, error) {
	if ask == nil {
		return "", errors.New("no compactor model")
	}
	var best string
	for range tries {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		reply, err := ask(callCtx, request)
		cancel()
		if err != nil {
			return "", err
		}
		line := strings.TrimSpace(ai.ContentText(reply.Content))
		if line == "" {
			return "", errors.New("empty summary")
		}
		if best == "" || len(line) < len(best) {
			best = line
		}
		if len(line) <= nodeBytes {
			break
		}
		request = append(request, reply, &ai.UserMessage{Content: ai.NewUserText(fmt.Sprintf(
			"That line is %d bytes; the limit is %d. It must end where it is cut here:\n%s| ← LIMIT", len(line), nodeBytes, cut(line, nodeBytes)))})
	}
	return best, nil
}

// settle waits until every message before n is summarized, so no call sees a placeholder; it
// reports false when ctx ends first.
func (t *tree) settle(ctx context.Context, n int) bool {
	for waited := false; ; waited = true {
		t.mu.Lock()
		frontier, progress, status := t.frontierLocked(), t.progress, t.status
		t.mu.Unlock()
		if frontier >= n {
			return true
		}
		if !waited {
			defer status("")
		}
		status(fmt.Sprintf("summarizing %d messages", n-frontier))
		select {
		case <-progress:
		case <-ctx.Done():
			return false
		case <-t.ctx.Done():
			return false
		}
	}
}

// render is the view of the messages before n, one "id+n|text" line per part.
func (t *tree) render(n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var lines []string
	for _, p := range t.view {
		if p.start() >= n {
			break
		}
		lines = append(lines, t.lineLocked(p))
	}
	return strings.Join(lines, "\n")
}

func (t *tree) lineLocked(p part) string {
	text := t.textLocked(p.l, p.i)
	if text == "" {
		text = unbuilt
	}
	return fmt.Sprintf("%d+%d|%s", p.start(), 1<<p.l, flat(text))
}

func (t *tree) length() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	return len(t.log)
}

// before is the first message at or after entry id on the branch, or the log's length.
func (t *tree) before(id string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	at := slices.Index(t.path, id)
	if at < 0 {
		return len(t.log)
	}
	for m, message := range t.log {
		if message.at >= at {
			return m
		}
	}
	return len(t.log)
}

func (t *tree) zoom(id, n int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	if n < 1 || n&(n-1) != 0 || id < 0 || id%n != 0 || id+n > len(t.log) {
		return fmt.Sprintf("No line %d+%d.", id, n)
	}
	if n == 1 {
		return fmt.Sprintf("%d+0|%s", id, t.log[id].line)
	}
	l := 0
	for 2<<l < n {
		l++
	}
	return t.lineLocked(part{l, id >> l}) + "\n" + t.lineLocked(part{l, id>>l + 1})
}

func (t *tree) date(id int) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	if id < 0 || id >= len(t.log) {
		return fmt.Sprintf("No message %d.", id)
	}
	if at, err := time.Parse(time.RFC3339Nano, t.log[id].time); err == nil {
		return at.Local().Format("2006-01-02 15:04:05 MST")
	}
	return t.log[id].time
}

// entryMessages turns one session entry into log messages. Thinking is left out: it adds little
// the replies and tool calls don't show, and summarizing it trips reasoning safeguards.
func entryMessages(entry session.SessionEntry) []message {
	var out []message
	add := func(kind, text string) {
		if text = strings.TrimSpace(text); text != "" {
			out = append(out, message{key: fmt.Sprintf("%s/%d", entry.ID, len(out)), line: kind + ": " + text, time: entry.Timestamp})
		}
	}
	switch entry.Type {
	case "message":
		var m struct {
			Role               string          `json:"role"`
			Content            json.RawMessage `json:"content"`
			Command            string          `json:"command"`
			Output             string          `json:"output"`
			ExcludeFromContext bool            `json:"excludeFromContext"`
			IsError            bool            `json:"isError"`
			ErrorMessage       string          `json:"errorMessage"`
		}
		if json.Unmarshal(entry.Message, &m) != nil {
			return nil
		}
		switch m.Role {
		case "user":
			add("user", contentText(m.Content))
		case "assistant":
			var blocks []struct {
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Name      string          `json:"name"`
				Arguments json.RawMessage `json:"arguments"`
			}
			_ = json.Unmarshal(m.Content, &blocks)
			var text []string
			for _, block := range blocks {
				if block.Type == "text" {
					text = append(text, block.Text)
				}
			}
			if len(text) == 0 && m.ErrorMessage != "" {
				text = append(text, "(failed: "+m.ErrorMessage+")")
			}
			add("talk", strings.Join(text, "\n"))
			for _, block := range blocks {
				if block.Type == "toolCall" {
					add("tool", block.Name+" "+string(block.Arguments))
				}
			}
		case "toolResult":
			text := contentText(m.Content)
			if m.IsError {
				text = "error: " + text
			}
			add("echo", text)
		case "bashExecution":
			if !m.ExcludeFromContext {
				add("echo", "$ "+m.Command+"\n"+m.Output)
			}
		case "custom":
			add("note", contentText(m.Content))
		}
	case "custom_message":
		add("note", contentText(entry.Content))
	case "branch_summary":
		add("note", entry.Summary)
	}
	return out
}

// contentText reads a string or a list of text and image blocks.
func contentText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &blocks)
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		switch block.Type {
		case "text":
			parts = append(parts, block.Text)
		case "image":
			parts = append(parts, "[image]")
		}
	}
	return strings.Join(parts, "\n")
}

func flat(text string) string { return strings.Join(strings.Fields(text), " ") }

// cut keeps at most n bytes without splitting a UTF-8 character.
func cut(text string, n int) string {
	if len(text) <= n {
		return text
	}
	for n > 0 && !utf8.RuneStart(text[n]) {
		n--
	}
	return text[:n]
}
