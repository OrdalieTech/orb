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
	nodeBytes               = 512             // size of one summary line
	viewHigh, viewLow       = 128_000, 64_000 // the view grows to viewHigh bytes, then one batch merges it to viewLow
	compactHigh, compactLow = 32_000, 16_000  // the same for the compactions' view
	pageChars               = 30_000          // tool output keeps its head and tail, other long text is paged
	jobs                    = 8               // compactor calls running at once
	tries                   = 5               // attempts per node to get under nodeBytes
	retryAfter              = 10 * time.Second
	giveUp                  = 3 // failed calls before a node keeps its messages cut to size
	callTimeout             = 5 * time.Minute
	unbuilt                 = "(not summarized yet: zoom it)"
)

// message is one item of the log: a session entry yields one per user prompt, reply text,
// tool call, tool result or harness note, in branch order, and long text in several.
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

func (p part) start() int   { return p.i << p.l }
func (p part) end() int     { return p.start() + 1<<p.l }
func (p part) name() string { return fmt.Sprintf("%d+%d", p.start(), 1<<p.l) }

// ask calls the compactor; mark is the request's last whole block of view, -1 for none.
type ask func(ctx context.Context, messages ai.MessageList, mark int) (*ai.AssistantMessage, error)

// tree is one session's summary tree and views, built from its current branch.
type tree struct {
	sessions extensions.ReadonlySessionManager
	id       string
	save     func(customType string, data any) // appends a memtree entry
	ctx      context.Context
	stop     context.CancelFunc
	saving   sync.Mutex // saves keep their order: a view after the nodes it shows

	mu          sync.Mutex
	ask         ask
	status      func(string)
	nodes       map[nodeKey]string
	pending     []nodeKey // built, not yet saved
	busy        map[nodeKey]bool
	failing     map[nodeKey]int // failed calls per node
	retry       time.Duration
	path        []string // entry IDs of the folded branch
	log         []message
	view, cview []part // the chat's view, and the coarser one compactions read
	size, csize int
	batch       bool   // the view passed viewHigh and is not yet down to viewLow
	merged      bool   // the view merged since it was last saved
	parents     int    // merge nodes built: a view that stopped short of its size waits for one more
	stopped     [2]int // parents when the view and the compactions' view last stopped short
	next        int    // the frontier: the first message not known to be summarized
	ready       []part // merges whose halves are built
	progress    chan struct{}
}

// newTree opens a session's tree with the nodes its memtree entries hold, on every branch.
func newTree(sessions extensions.ReadonlySessionManager, save func(string, any)) *tree {
	ctx, stop := context.WithCancel(context.Background())
	t := &tree{
		sessions: sessions, id: sessions.GetSessionID(), save: save, ctx: ctx, stop: stop, status: func(string) {},
		nodes: map[nodeKey]string{}, busy: map[nodeKey]bool{}, failing: map[nodeKey]int{}, retry: retryAfter, progress: make(chan struct{}),
		stopped: [2]int{-1, -1},
	}
	for _, entry := range sessions.GetEntries() {
		var node savedNode
		if entry.Type == "custom" && entry.CustomType == nodeType && json.Unmarshal(entry.Data, &node) == nil && node.Text != "" {
			t.nodes[node.nodeKey] = node.Text
		}
	}
	return t
}

// savedNode and savedView are memtree session entries: the tree is a cache of model calls, and
// the view is saved after each merge, so both follow the session to every host, fork and export.
type savedNode struct {
	nodeKey
	Text string `json:"t"`
}

type savedView struct {
	Parts [][2]int `json:"v"`
	Batch bool     `json:"b"`
}

const (
	nodeType = "memtree"
	viewType = "memtree-view"
)

// saveLocked keeps node p, built under key: a branch move may have changed what p names.
func (t *tree) saveLocked(p part, key nodeKey, text string) {
	if key.Level > 0 {
		t.parents++
	}
	t.nodes[key] = text
	t.pending = append(t.pending, key)
	if p.end() <= len(t.log) && t.keyLocked(p.l, p.i) == key {
		t.queueLocked(p)
	}
}

// queueLocked queues the merge above node p, built, once its sibling is built too.
func (t *tree) queueLocked(p part) {
	if sibling := (part{p.l, p.i ^ 1}); sibling.end() <= len(t.log) && t.builtLocked(sibling.l, sibling.i) && !t.builtLocked(p.l+1, p.i/2) {
		t.ready = append(t.ready, part{p.l + 1, p.i / 2})
	}
}

// syncLocked folds the entries added to the branch since the last call, walking back from the
// leaf to the folded branch's last entry. A branch that does not extend the folded one (a /tree
// move, a rewound prompt) is folded again from message 0, and its view is the last one saved on
// it: a view rebuilt from the log differs from the live one, and every cache entry dies.
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
		t.path, t.log, t.view, t.cview, t.size, t.csize, t.batch, t.next, t.ready, t.stopped = nil, nil, nil, nil, 0, 0, false, 0, nil, [2]int{-1, -1}
	}
	var saved []part
	batch := false
	if len(t.path) == 0 {
		saved, batch = lastView(added)
	}
	for k := len(added) - 1; k >= 0; k-- {
		t.path = append(t.path, added[k].ID)
		for _, message := range entryMessages(added[k]) {
			message.at = len(t.path) - 1
			t.log = append(t.log, message)
			for l, i := 0, len(t.log)-1; (i+1)%(1<<l) == 0 && t.builtLocked(l, i>>l); l++ {
				t.queueLocked(part{l, i >> l})
			}
			switch {
			case saved == nil:
				t.appendLocked()
			case saved[len(saved)-1].end() == len(t.log):
				t.restoreLocked(saved, batch)
				saved = nil
			}
		}
	}
	if saved != nil {
		t.restoreLocked(nil, false)
	}
}

// lastView is the newest view saved among entries, newest first, when it tiles messages from 0.
func lastView(entries []session.SessionEntry) ([]part, bool) {
	for _, entry := range entries {
		var saved savedView
		if entry.Type != "custom" || entry.CustomType != viewType || json.Unmarshal(entry.Data, &saved) != nil {
			continue
		}
		var view []part
		for next := 0; len(view) < len(saved.Parts); next = view[len(view)-1].end() {
			p := part{saved.Parts[len(view)][0], saved.Parts[len(view)][1]}
			if p.l < 0 || p.l > 40 || p.start() != next {
				return nil, false
			}
			view = append(view, p)
		}
		return view, saved.Batch
	}
	return nil, false
}

// restoreLocked makes saved the view of the log, or rebuilds the view from the log when saved
// does not cover it with built lines.
func (t *tree) restoreLocked(saved []part, batch bool) {
	usable := len(saved) > 0 && saved[len(saved)-1].end() == len(t.log)
	for index := 0; usable && index < len(saved); index++ {
		usable = saved[index].l == 0 || t.builtLocked(saved[index].l, saved[index].i)
	}
	if !usable {
		log := t.log
		t.log = nil
		for _, message := range log {
			t.log = append(t.log, message)
			t.appendLocked()
		}
		return
	}
	t.view, t.size, t.batch, t.merged = saved, t.sizeLocked(saved), batch, false
	t.stopped[1] = -1
	t.cview, t.csize = t.mergeLocked(slices.Clone(saved), t.size, compactLow, &t.stopped[1])
}

// appendLocked adds the last message's line to both views.
func (t *tree) appendLocked() {
	p := part{0, len(t.log) - 1}
	t.view, t.cview = append(t.view, p), append(t.cview, p)
	t.size += t.partSizeLocked(p)
	t.csize += t.partSizeLocked(p)
	t.fitLocked()
}

// fitLocked batches merges: once the view passes viewHigh, the most due pairs merge until it is
// at most viewLow, again at each change while their parents are not built yet. The compactions'
// view merges to compactLow whenever it passes compactHigh or the view has merged. Between
// batches both only grow at their end, so the prompt cache keeps them.
func (t *tree) fitLocked() {
	merge := t.csize > compactHigh
	if t.batch = t.batch || t.size > viewHigh; t.batch {
		lines := len(t.view)
		t.view, t.size = t.mergeLocked(t.view, t.size, viewLow, &t.stopped[0])
		t.batch = t.size > viewLow
		if len(t.view) < lines {
			t.merged, merge = true, true
		}
	}
	if merge {
		t.cview, t.csize = t.mergeLocked(t.cview, t.csize, compactLow, &t.stopped[1])
	}
}

// mergeLocked merges the most due pair of sibling lines whose parent is built, the oldest of
// equal ones, until view is at most low bytes. A pair is due by how long ago it ended in its own
// line size, (T - last) / 2^l, which makes Taelin's rollback push at any size: detail fades with
// age, old lines stay put and new ones churn. A view that stopped short of low is not scanned
// again before a merge node is built.
func (t *tree) mergeLocked(view []part, size, low int, stopped *int) ([]part, int) {
	for size > low && *stopped != t.parents {
		best, due := -1, 0.0
		for index := 0; index+1 < len(view); index++ {
			a, b := view[index], view[index+1]
			if a.l != b.l || a.i%2 != 0 || b.i != a.i+1 || !t.builtLocked(a.l+1, a.i/2) {
				continue
			}
			if value := float64(len(t.log)-b.end()+1) / float64(b.end()-b.start()); best < 0 || value > due {
				best, due = index, value
			}
		}
		if best < 0 {
			*stopped = t.parents
			break
		}
		a, b := view[best], view[best+1]
		merged := part{a.l + 1, a.i / 2}
		size += t.partSizeLocked(merged) - t.partSizeLocked(a) - t.partSizeLocked(b)
		view = slices.Replace(view, best, best+2, merged)
	}
	return view, size
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

// partSizeLocked is the bytes of p's line in a rendered view.
func (t *tree) partSizeLocked(p part) int { return len(t.lineLocked(p)) + 1 }

func (t *tree) sizeLocked(view []part) int {
	size := 0
	for _, p := range view {
		size += t.partSizeLocked(p)
	}
	return size
}

// pump starts every node that is ready, saves what was built and wakes settle.
func (t *tree) pump() {
	t.saving.Lock()
	defer t.saving.Unlock()
	t.mu.Lock()
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		return
	}
	t.pumpLocked()
	close(t.progress)
	t.progress = make(chan struct{})
	nodes := make([]savedNode, len(t.pending))
	for index, key := range t.pending {
		nodes[index] = savedNode{key, t.nodes[key]}
	}
	t.pending = nil
	var view *savedView
	if t.merged {
		view = &savedView{Batch: t.batch}
		for _, p := range t.view {
			view.Parts = append(view.Parts, [2]int{p.l, p.i})
		}
		t.merged = false
	}
	t.mu.Unlock()
	for _, node := range nodes {
		t.save(nodeType, node)
	}
	if view != nil {
		t.save(viewType, view)
	}
}

// pumpLocked starts the queued merges, then each message's node once fewer than 8 lines before it
// are unbuilt, jobs at a time: merges first, so the views keep fitting through a backlog.
func (t *tree) pumpLocked() {
	t.syncLocked()
	t.ready = slices.DeleteFunc(t.ready, func(p part) bool { return t.builtLocked(p.l, p.i) })
	for index := 0; index < len(t.ready) && len(t.busy) < jobs; index++ {
		if p := t.ready[index]; !t.busy[t.keyLocked(p.l, p.i)] {
			t.startLocked(p.l, p.i, t.keyLocked(p.l, p.i))
		}
	}
	for t.next < len(t.log) && t.builtLocked(0, t.next) {
		t.next++
	}
	for i, waiting := t.next, 0; i < len(t.log) && waiting < 8 && len(t.busy) < jobs; i++ {
		if !t.builtLocked(0, i) {
			if waiting++; !t.busy[t.keyLocked(0, i)] {
				t.startLocked(0, i, t.keyLocked(0, i))
			}
		}
	}
	t.fitLocked() // free merges may have built parents
}

// startLocked builds node (l, i): a merge of two lines that fit together is free, any other step
// is a compaction. Its context is the compactions' view up to the node's message, or the merge's
// last one, and up to its first unbuilt line, so no call sees a placeholder or half a message.
func (t *tree) startLocked(l, i int, key nodeKey) {
	var task, fallback string
	end := i
	if l == 0 {
		task, fallback = fmt.Sprintf(compressTask, i, ruler, t.log[i].line), cut(flat(t.log[i].line), nodeBytes)
	} else {
		left, right := part{l - 1, 2 * i}, part{l - 1, 2*i + 1}
		a, b := flat(t.textLocked(left.l, left.i)), flat(t.textLocked(right.l, right.i))
		if len(a)+1+len(b) <= nodeBytes {
			t.saveLocked(part{l, i}, key, a+"\n"+b)
			return
		}
		end = right.end()
		task = fmt.Sprintf(mergeTask, left.name(), right.name(), ruler, left.start(), end-1, t.lineLocked(left), t.lineLocked(right))
		fallback = cut(a, nodeBytes/2-1) + " " + cut(b, nodeBytes/2)
	}
	var lines []string
	for _, p := range t.cview {
		if p.start() >= end || !t.builtLocked(p.l, p.i) {
			break
		}
		lines = append(lines, t.lineLocked(p))
	}
	t.busy[key] = true
	go t.build(part{l, i}, key, t.ask, ai.MessageList{chat(lines, task)}, len(lines)/4-1, fallback)
}

func (t *tree) build(p part, key nodeKey, ask ask, request ai.MessageList, mark int, fallback string) {
	text, err := compress(t.ctx, ask, request, mark)
	t.mu.Lock()
	switch {
	case err != nil:
		if t.failing[key] == 0 && t.ctx.Err() == nil {
			t.status("compactor failed: " + err.Error())
		}
		// A node the compactor keeps failing on (a refusal, no model) keeps its messages cut to
		// size, saved like a summary: the session goes on, and nothing calls again, now or at the
		// next start.
		if t.failing[key]++; t.failing[key] < giveUp && ask != nil {
			t.mu.Unlock()
			time.AfterFunc(t.retry, func() {
				t.mu.Lock()
				delete(t.busy, key)
				t.mu.Unlock()
				t.pump()
			})
			return
		}
		text = fallback
	case t.failing[key] > 0:
		delete(t.failing, key)
		t.status("")
	}
	t.saveLocked(p, key, text)
	delete(t.busy, key)
	// The line replaces a placeholder when it is in a view.
	t.size, t.csize = t.sizeLocked(t.view), t.sizeLocked(t.cview)
	t.fitLocked()
	t.mu.Unlock()
	t.pump()
}

// compress asks for one line and, while it is over nodeBytes, shows the model where the limit
// cuts it, in the same conversation; models can't count bytes. The shortest try is kept.
func compress(ctx context.Context, ask ask, request ai.MessageList, mark int) (string, error) {
	if ask == nil {
		return "", errors.New("no compactor model")
	}
	var best string
	for range tries {
		callCtx, cancel := context.WithTimeout(ctx, callTimeout)
		reply, err := ask(callCtx, request, mark)
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
		request = append(request, reply, &ai.UserMessage{Content: ai.NewUserText(fmt.Sprintf(tooLong, len(line), cut(line, nodeBytes)))})
	}
	return best, nil
}

// chat is a view's lines as one message, in blocks of 4 lines so a cache mark can sit on the last
// whole one, then the blocks of after.
func chat(lines []string, after ...string) *ai.UserMessage {
	blocks := ai.UserContentBlocks{&ai.TextContent{Text: "<chat>\n"}}
	for index, line := range lines {
		if index > 0 && index%4 == 0 {
			blocks = append(blocks, &ai.TextContent{})
		}
		blocks[len(blocks)-1].(*ai.TextContent).Text += line + "\n"
	}
	blocks = append(blocks, &ai.TextContent{Text: "</chat>"})
	for _, text := range after {
		blocks = append(blocks, &ai.TextContent{Text: text})
	}
	return &ai.UserMessage{Content: ai.UserContent{Blocks: blocks}, Timestamp: time.Now().UnixMilli()}
}

// settle waits until every message before n is summarized, so no call sees a placeholder; it
// reports false when ctx ends first.
func (t *tree) settle(ctx context.Context, n int) bool {
	for waited := false; ; waited = true {
		t.mu.Lock()
		frontier, progress, status := t.next, t.progress, t.status
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

// render is view's lines for the messages before n, one "id+n|text" per node; a line running
// past n opens into the lines it was made from.
func (t *tree) render(n int, view []part) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var lines []string
	var add func(part)
	add = func(p part) {
		switch {
		case p.start() >= n:
		case p.end() <= n:
			lines = append(lines, t.lineLocked(p))
		default:
			add(part{p.l - 1, 2 * p.i})
			add(part{p.l - 1, 2*p.i + 1})
		}
	}
	for _, p := range view {
		add(p)
	}
	return lines
}

func (t *tree) lineLocked(p part) string {
	text := t.textLocked(p.l, p.i)
	if text == "" {
		text = unbuilt
	}
	return p.name() + "|" + flat(text)
}

// current is the log's length and a copy of the view, as a turn's message would find them.
func (t *tree) current() (int, []part) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	return len(t.log), slices.Clone(t.view)
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

func (t *tree) zoom(id, n int) ai.ToolResultContent {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.syncLocked()
	if n < 1 || n&(n-1) != 0 || id < 0 || id%n != 0 || n > len(t.log)-id {
		return ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf("No line %d+%d.", id, n)}}
	}
	if n == 1 {
		content := ai.ToolResultContent{&ai.TextContent{Text: fmt.Sprintf("%d+0|%s", id, t.log[id].line)}}
		if entryID, page, _ := strings.Cut(t.log[id].key, "/"); page == "0" {
			if entry := t.sessions.GetEntry(entryID); entry != nil {
				for _, image := range entryImages(*entry) {
					content = append(content, image)
				}
			}
		}
		return content
	}
	l := 0
	for 2<<l < n {
		l++
	}
	return ai.ToolResultContent{&ai.TextContent{Text: t.lineLocked(part{l, id >> l}) + "\n" + t.lineLocked(part{l, id>>l + 1})}}
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
// the replies and tool calls don't show, and summarizing it trips reasoning safeguards. A tool's
// output keeps its head and tail, pageChars in all; any other long text goes in several messages.
func entryMessages(entry session.SessionEntry) []message {
	var out []message
	add := func(kind, text string) {
		text = strings.TrimSpace(text)
		if utf8.RuneCountInString(text) > pageChars {
			runes := []rune(text)
			if kind == "echo" {
				text = string(runes[:pageChars/2]) + "\n...\n" + string(runes[len(runes)-pageChars/2:])
			} else {
				for ; len(runes) > pageChars; runes = runes[pageChars:] {
					out = append(out, message{key: fmt.Sprintf("%s/%d", entry.ID, len(out)), line: kind + ": " + string(runes[:pageChars]), time: entry.Timestamp})
				}
				text = string(runes)
			}
		}
		if text != "" {
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
			add("orb", strings.Join(text, "\n"))
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

// entryImages are the images of a user message, tool result or note entry.
func entryImages(entry session.SessionEntry) []*ai.ImageContent {
	raw := entry.Content
	if entry.Type == "message" {
		var m struct {
			Content json.RawMessage `json:"content"`
		}
		_ = json.Unmarshal(entry.Message, &m)
		raw = m.Content
	}
	var blocks []struct {
		Type     string `json:"type"`
		Data     string `json:"data"`
		MimeType string `json:"mimeType"`
	}
	_ = json.Unmarshal(raw, &blocks)
	var images []*ai.ImageContent
	for _, block := range blocks {
		if block.Type == "image" {
			images = append(images, &ai.ImageContent{Data: block.Data, MimeType: block.MimeType})
		}
	}
	return images
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
