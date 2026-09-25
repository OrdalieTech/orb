package modes

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

// The tree reads like the conversation: one history at a time, one numbered
// row per prompt. Where the history forks, the row that differs counts its
// versions ("2/3") and ←→ shows another one, so every branch is reachable
// without drawing the tree. The selected turn's reply shows below the list.

type treeRowKind int

const (
	treeRowPrompt treeRowKind = iota
	treeRowReply
	treeRowNote
)

// treeEntryInfo is what the tree needs from an entry, parsed once per
// selector so rebuilding on every keystroke stays linear.
type treeEntryInfo struct {
	role   string
	kind   treeRowKind
	text   string
	search string
	// full is the entry's own text, bounded, for the reply preview.
	full string
	// turn numbers a prompt within its own history.
	turn int
	// latest is the newest timestamp in the subtree, which picks the branch
	// a history continues along below a fork.
	latest time.Time
	time   time.Time
}

type treeIndex struct {
	roots    []*sessionstore.SessionTreeNode
	byID     map[string]*sessionstore.SessionTreeNode
	info     map[string]*treeEntryInfo
	order    []*sessionstore.SessionTreeNode
	prompts  int
	branches int
}

func newTreeIndex(roots []*sessionstore.SessionTreeNode) *treeIndex {
	index := &treeIndex{
		roots: roots,
		byID:  make(map[string]*sessionstore.SessionTreeNode),
		info:  make(map[string]*treeEntryInfo),
	}
	stack := appendReversedTreeNodes(nil, roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		info := newTreeEntryInfo(node.Entry)
		if parent := node.Entry.ParentID; parent != nil && index.info[*parent] != nil {
			info.turn = index.info[*parent].turn
		}
		if info.role == "user" {
			info.turn++
			index.prompts++
		}
		index.byID[node.Entry.ID] = node
		index.info[node.Entry.ID] = info
		index.order = append(index.order, node)
		stack = appendReversedTreeNodes(stack, node.Children)
	}
	// Reverse pre-order visits children before their parent.
	for position := len(index.order) - 1; position >= 0; position-- {
		node := index.order[position]
		info := index.info[node.Entry.ID]
		info.latest = info.time
		for _, child := range node.Children {
			if latest := index.info[child.Entry.ID].latest; latest.After(info.latest) {
				info.latest = latest
			}
		}
		if len(node.Children) == 0 {
			index.branches++
		}
	}
	return index
}

func newTreeEntryInfo(entry sessionstore.SessionEntry) *treeEntryInfo {
	info := &treeEntryInfo{kind: treeRowNote}
	info.time, _ = time.Parse(time.RFC3339Nano, entry.Timestamp)
	full := ""
	switch entry.Type {
	case "message":
		var message struct {
			Role     string `json:"role"`
			ToolName string `json:"toolName"`
		}
		_ = json.Unmarshal(entry.Message, &message)
		_, full = sessionMessageRoleText(entry.Message)
		info.role = message.Role
		switch message.Role {
		case "user":
			info.kind = treeRowPrompt
			full = skillPreview(full)
		case "assistant":
			info.kind = treeRowReply
		case "toolResult":
			info.text = message.ToolName + "  "
		default:
			info.text = message.Role + "  "
		}
	case "branch_summary":
		info.text, full = "summary  ", entry.Summary
	case "compaction":
		info.text, full = "compacted  ", entry.Summary
	default:
		info.text = entry.Type
	}
	info.text += oneLine(full, 400)
	info.search = strings.ToLower(info.text + " " + full)
	if runes := []rune(strings.TrimSpace(full)); len(runes) > 4000 {
		info.full = string(runes[:4000])
	} else {
		info.full = string(runes)
	}
	return info
}

// oneLine collapses whitespace so a row never wraps, keeping at most limit
// runes; rows are truncated to the terminal width at render time anyway.
func oneLine(text string, limit int) string {
	fields := strings.Fields(text)
	var builder strings.Builder
	for _, field := range fields {
		if builder.Len() > 0 {
			builder.WriteByte(' ')
		}
		builder.WriteString(field)
		if builder.Len() >= limit*4 {
			break
		}
	}
	runes := []rune(builder.String())
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return string(runes)
}

// children lists the entries below id; "" stands for the session itself,
// whose children are the roots.
func (index *treeIndex) children(id string) []*sessionstore.SessionTreeNode {
	if id == "" {
		return index.roots
	}
	if node := index.byID[id]; node != nil {
		return node.Children
	}
	return nil
}

// pathTo lists the entries from the root down to id.
func (index *treeIndex) pathTo(id string) []*sessionstore.SessionTreeNode {
	var path []*sessionstore.SessionTreeNode
	for node := index.byID[id]; node != nil; {
		path = append(path, node)
		if node.Entry.ParentID == nil {
			break
		}
		node = index.byID[*node.Entry.ParentID]
	}
	slices.Reverse(path)
	return path
}

// next is the entry a history continues with below id: the current history
// where it passes, otherwise the most recently active branch.
func (index *treeIndex) next(id string, current map[string]bool) *sessionstore.SessionTreeNode {
	var next *sessionstore.SessionTreeNode
	for _, child := range index.children(id) {
		switch {
		case next == nil, current[child.Entry.ID] && !current[next.Entry.ID]:
			next = child
		case current[next.Entry.ID] != current[child.Entry.ID]:
		case index.info[child.Entry.ID].latest.After(index.info[next.Entry.ID].latest):
			next = child
		}
	}
	return next
}

// descend follows a history from id to its end.
func (index *treeIndex) descend(id string, current map[string]bool) string {
	for next := index.next(id, current); next != nil; next = index.next(id, current) {
		id = next.Entry.ID
	}
	return id
}

// byTime orders entries oldest first.
func (index *treeIndex) byTime(a, b *sessionstore.SessionTreeNode) int {
	return index.info[a.Entry.ID].time.Compare(index.info[b.Entry.ID].time)
}

type treeRow struct {
	node *sessionstore.SessionTreeNode
	kind treeRowKind
	text string
	// turn numbers a prompt within the history shown; 0 on other rows.
	turn int
	// end is the entry going to the row reaches: the end of a turn, so the
	// conversation continues after its reply.
	end string
	// versions are the histories that differ from this row on, oldest first;
	// version indexes the one shown. Fewer than two means no fork here.
	versions []*sessionstore.SessionTreeNode
	version  int
	// inContext marks rows on the current history; current holds the leaf.
	inContext bool
	current   bool
	reply     string
}

// TreeSelectorComponent is the session tree opened by /tree and double Escape.
type TreeSelectorComponent struct {
	index *treeIndex
	// history is the entries from the root to the leaf: what the model sees.
	history map[string]bool
	leafID  string
	// shown is the end of the history on screen; ←→ at a fork moves it.
	shown string
	// entries lists every message instead of one row per prompt.
	entries             bool
	showLabelTimestamps bool
	rows                []treeRow
	selected            int
	maxVisible          int
	// height only grows within a view, so typing a search never shrinks
	// the modal under the cursor; switching views sizes it afresh.
	height        int
	filterInput   *tui.Input
	labelInput    *tui.Input
	labelEntryID  string
	focused       bool
	allHints      bool
	onSelect      func(string)
	onCancel      func()
	onLabelChange func(string, *string)
	now           func() time.Time

	// mu serializes input against renders, which run on the TUI's render
	// timer; callbacks queue in pending and run once it is released.
	mu      sync.Mutex
	pending []func()
	// Where the last render put the rows, so pointer rows resolve to what
	// the user saw; renders anchor window while pointer input freezes it.
	window                  tui.ListWindow
	treeTop, rowStart, rowN int
	// textColumn is where the last render started row text, so the preview
	// lines up with it.
	textColumn int

	OnCopy func(string)
	// OnFork forks the selected entry into a new session; before is set for
	// a user prompt, which is forked from just before it like /fork.
	OnFork func(entryID string, before bool)
}

// treePreviewLines is the fixed height of the reply preview, so moving the
// selection never resizes the modal.
const treePreviewLines = 4

// SetMaxVisible sizes the list for the space its container offers.
func (component *TreeSelectorComponent) SetMaxVisible(rows int) {
	component.mu.Lock()
	defer component.mu.Unlock()
	component.maxVisible = max(3, rows)
	component.height = 0
}

func NewTreeSelectorComponent(
	roots []*sessionstore.SessionTreeNode,
	leafID string,
	terminalHeight int,
	onSelect func(string),
	onCancel func(),
	onLabelChange func(string, *string),
	initialSelectedID string,
	filterMode string,
) *TreeSelectorComponent {
	component := &TreeSelectorComponent{
		index: newTreeIndex(roots), leafID: leafID, history: make(map[string]bool),
		entries:    filterMode == "all" || filterMode == "no-tools",
		maxVisible: max(3, terminalHeight/2), onSelect: onSelect, onCancel: onCancel,
		onLabelChange: onLabelChange, now: time.Now,
	}
	for _, node := range component.index.pathTo(leafID) {
		component.history[node.Entry.ID] = true
	}
	if initialSelectedID == "" || component.index.byID[initialSelectedID] == nil {
		initialSelectedID = leafID
	}
	component.shown = component.index.descend(initialSelectedID, component.history)
	component.refresh(initialSelectedID)
	return component
}

func (component *TreeSelectorComponent) query() string {
	if component.filterInput == nil {
		return ""
	}
	return component.filterInput.GetValue()
}

func (component *TreeSelectorComponent) refresh(preferredID string) {
	if tokens := strings.Fields(strings.ToLower(component.query())); len(tokens) > 0 {
		component.rows = component.searchRows(tokens)
	} else {
		component.rows = component.historyRows()
	}
	component.selectNearest(preferredID)
}

// rowEntry reports whether an entry gets its own row.
func (component *TreeSelectorComponent) rowEntry(node *sessionstore.SessionTreeNode) bool {
	info := component.index.info[node.Entry.ID]
	switch {
	case node.Entry.Type == "branch_summary", node.Entry.Type == "compaction":
		return true
	case component.entries:
		return treeEntryVisible(node, node.Entry.ID == component.leafID, "no-tools") && info.full != ""
	}
	return info.role == "user"
}

// historyRows lays out the history ending at shown.
func (component *TreeSelectorComponent) historyRows() []treeRow {
	index := component.index
	path := index.pathTo(component.shown)
	var at []int
	for position, node := range path {
		if component.rowEntry(node) {
			at = append(at, position)
		}
	}
	// A history whose last fork lies after its last row, like a reply that
	// was abandoned for a new prompt, ends on a row of its own so its other
	// versions stay reachable.
	last := -1
	if len(at) > 0 {
		last = at[len(at)-1]
	}
	if last != len(path)-1 && len(component.versions(path, last, len(path)-1)) > 1 {
		at = append(at, len(path)-1)
	}
	leafAt := slices.IndexFunc(path, func(node *sessionstore.SessionTreeNode) bool { return node.Entry.ID == component.leafID })
	rows := make([]treeRow, 0, len(at))
	previous, turn := -1, 0
	for number, position := range at {
		node := path[position]
		info := index.info[node.Entry.ID]
		end := len(path) - 1
		if number+1 < len(at) {
			end = at[number+1] - 1
		}
		row := treeRow{
			node: node, kind: info.kind, text: info.text, end: node.Entry.ID,
			inContext: component.history[node.Entry.ID],
			current:   leafAt >= position && leafAt <= end,
		}
		if info.kind == treeRowPrompt {
			turn++
			row.turn = turn
		}
		if !component.entries {
			row.end = path[end].Entry.ID
		}
		row.versions = component.versions(path, previous, position)
		row.version = slices.IndexFunc(row.versions, func(version *sessionstore.SessionTreeNode) bool {
			return slices.Contains(path, version)
		})
		row.reply = component.reply(row, path[position+1:end+1])
		rows = append(rows, row)
		previous = position
	}
	return rows
}

// versions lists the histories that part from path between from (the row
// before, or -1 for the session itself) and to, oldest first: the one shown
// plus every branch left at a fork on the way.
func (component *TreeSelectorComponent) versions(path []*sessionstore.SessionTreeNode, from, to int) []*sessionstore.SessionTreeNode {
	parent := func(position int) string {
		if position < 0 {
			return ""
		}
		return path[position].Entry.ID
	}
	deepest := -2
	for position := max(-1, from); position < to; position++ {
		if len(component.index.children(parent(position))) > 1 {
			deepest = position
		}
	}
	if deepest < -1 {
		return nil
	}
	// The branches a shallower fork kept are the one shown; it stands in
	// once, at the deepest fork.
	var versions []*sessionstore.SessionTreeNode
	for position := max(-1, from); position <= deepest; position++ {
		for _, child := range component.index.children(parent(position)) {
			if child != path[position+1] || position == deepest {
				versions = append(versions, child)
			}
		}
	}
	slices.SortStableFunc(versions, component.index.byTime)
	return versions
}

// reply is the preview for a row: the last reply of a turn, or the row's own
// text for anything else.
func (component *TreeSelectorComponent) reply(row treeRow, turn []*sessionstore.SessionTreeNode) string {
	if row.kind != treeRowPrompt {
		return component.index.info[row.node.Entry.ID].full
	}
	if component.entries {
		return ""
	}
	reply := ""
	for _, node := range turn {
		if info := component.index.info[node.Entry.ID]; info.role == "assistant" && info.full != "" {
			reply = info.full
		}
	}
	return reply
}

// searchRows lists the matching prompts and summaries of every history,
// oldest first; going to one continues after its turn.
func (component *TreeSelectorComponent) searchRows(tokens []string) []treeRow {
	var rows []treeRow
	for _, node := range component.index.order {
		info := component.index.info[node.Entry.ID]
		if !component.rowEntry(node) || !treeMatchesTokens(node, info, tokens) {
			continue
		}
		row := treeRow{node: node, kind: info.kind, text: info.text, end: node.Entry.ID, inContext: component.history[node.Entry.ID]}
		// A prompt sent again matches twice; its version tells them apart.
		parent := ""
		if node.Entry.ParentID != nil {
			parent = *node.Entry.ParentID
		}
		if siblings := component.index.children(parent); len(siblings) > 1 {
			row.versions = slices.SortedStableFunc(slices.Values(siblings), component.index.byTime)
			row.version = slices.Index(row.versions, node)
		}
		if info.kind == treeRowPrompt && !component.entries {
			row.turn = info.turn
			var turn []*sessionstore.SessionTreeNode
			for next := component.index.next(node.Entry.ID, component.history); next != nil && component.index.info[next.Entry.ID].role != "user"; next = component.index.next(next.Entry.ID, component.history) {
				turn = append(turn, next)
				row.end = next.Entry.ID
			}
			row.reply = component.reply(row, turn)
		}
		rows = append(rows, row)
	}
	slices.SortStableFunc(rows, func(a, b treeRow) int { return component.index.byTime(a.node, b.node) })
	return rows
}

func treeMatchesTokens(node *sessionstore.SessionTreeNode, info *treeEntryInfo, tokens []string) bool {
	label := ""
	if node.Label != nil {
		label = strings.ToLower(*node.Label)
	}
	for _, token := range tokens {
		if !strings.Contains(info.search, token) && !strings.Contains(label, token) {
			return false
		}
	}
	return true
}

func appendReversedTreeNodes(
	stack, nodes []*sessionstore.SessionTreeNode,
) []*sessionstore.SessionTreeNode {
	for index := len(nodes) - 1; index >= 0; index-- {
		stack = append(stack, nodes[index])
	}
	return stack
}

// selectNearest selects the row holding id, or the nearest row above it.
func (component *TreeSelectorComponent) selectNearest(id string) {
	for id != "" {
		for index, row := range component.rows {
			if row.node.Entry.ID == id {
				component.selected = index
				return
			}
		}
		node := component.index.byID[id]
		if node == nil || node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}
	component.selected = max(0, len(component.rows)-1)
}

func (component *TreeSelectorComponent) selectedRow() *treeRow {
	if component.selected < 0 || component.selected >= len(component.rows) {
		return nil
	}
	return &component.rows[component.selected]
}

func (component *TreeSelectorComponent) selectedID() string {
	if row := component.selectedRow(); row != nil {
		return row.node.Entry.ID
	}
	return ""
}

// later queues a callback to run after the lock is released, so hosts may
// close or re-render the selector from it.
func (component *TreeSelectorComponent) later(callback func()) {
	component.pending = append(component.pending, callback)
}

func (component *TreeSelectorComponent) unlockAndRun() {
	pending := component.pending
	component.pending = nil
	component.mu.Unlock()
	for _, callback := range pending {
		callback()
	}
}

func (component *TreeSelectorComponent) HandleInput(event tui.KeyEvent) {
	component.mu.Lock()
	defer component.unlockAndRun()
	// Any keyboard interaction re-anchors the window on the selection; only
	// pointer selection keeps it frozen.
	component.window.Recenter()
	bindings := tui.GetKeybindings()
	raw := event.Raw
	key := raw
	if printable := tui.DecodeKittyPrintable(raw); printable != "" {
		key = printable
	}
	if component.labelInput != nil {
		switch {
		case bindings.Matches(raw, "tui.select.confirm"):
			component.saveLabel()
		case bindings.Matches(raw, "tui.select.cancel"):
			component.labelInput = nil
		default:
			component.labelInput.HandleInput(event)
		}
		return
	}
	if component.handleNavigation(raw) {
		return
	}
	if component.filterInput != nil {
		component.handleFilterInput(event)
		return
	}

	switch {
	case bindings.Matches(raw, "tui.select.cancel"):
		if component.onCancel != nil {
			component.later(component.onCancel)
		}
	case tui.MatchesKey(raw, "left"):
		component.switchVersion(-1)
	case tui.MatchesKey(raw, "right"):
		component.switchVersion(1)
	case key == "?":
		component.allHints = !component.allHints
	case key == "/":
		component.filterInput = tui.NewInput()
		component.filterInput.Prompt = "/"
		component.filterInput.SetFocused(component.focused)
	case key == "e":
		if row := component.selectedRow(); row != nil && row.kind == treeRowPrompt && component.onSelect != nil {
			id, onSelect := row.node.Entry.ID, component.onSelect
			component.later(func() { onSelect(id) })
		}
	case key == "f":
		if row := component.selectedRow(); row != nil && component.OnFork != nil {
			id, before, fork := row.end, row.end == row.node.Entry.ID && row.kind == treeRowPrompt, component.OnFork
			component.later(func() { fork(id, before) })
		}
	case tui.MatchesKey(raw, "tab") || tui.MatchesKey(raw, "shift+tab") ||
		bindings.Matches(raw, "app.tree.filter.cycleForward") || bindings.Matches(raw, "app.tree.filter.cycleBackward"):
		component.setEntries(!component.entries)
	case bindings.Matches(raw, "app.tree.filter.default"), bindings.Matches(raw, "app.tree.filter.userOnly"):
		component.setEntries(false)
	case bindings.Matches(raw, "app.tree.filter.noTools"), bindings.Matches(raw, "app.tree.filter.all"):
		component.setEntries(true)
	case bindings.Matches(raw, "app.tree.filter.labeledOnly"):
		for step := 1; step <= len(component.rows); step++ {
			if index := (component.selected + step) % len(component.rows); component.rows[index].node.Label != nil {
				component.selected = index
				break
			}
		}
	case bindings.Matches(raw, "app.tree.editLabel"):
		component.editLabel()
	case bindings.Matches(raw, "app.tree.toggleLabelTimestamp"):
		component.showLabelTimestamps = !component.showLabelTimestamps
	case key == "j":
		component.move(1)
	case key == "k":
		component.move(-1)
	case key == "g":
		component.selected = 0
	case key == "G":
		component.selected = max(0, len(component.rows)-1)
	}
}

// handleNavigation covers the keys that work the same while searching:
// moving, jumping between forks, confirming and copying.
func (component *TreeSelectorComponent) handleNavigation(raw string) bool {
	bindings := tui.GetKeybindings()
	last := max(0, len(component.rows)-1)
	switch {
	case bindings.Matches(raw, "tui.select.up"):
		component.move(-1)
	case bindings.Matches(raw, "tui.select.down"):
		component.move(1)
	case bindings.Matches(raw, "tui.select.pageUp"):
		component.selected = max(0, component.selected-component.maxVisible)
	case bindings.Matches(raw, "tui.select.pageDown"):
		component.selected = min(last, component.selected+component.maxVisible)
	case tui.MatchesKey(raw, "home"):
		component.selected = 0
	case tui.MatchesKey(raw, "end"):
		component.selected = last
	case bindings.Matches(raw, "app.tree.foldOrUp"):
		component.jumpFork(-1)
	case bindings.Matches(raw, "app.tree.unfoldOrDown"):
		component.jumpFork(1)
	case bindings.Matches(raw, "tui.select.confirm"):
		component.confirm()
	case bindings.Matches(raw, "app.message.copy"):
		if copyText, row := component.OnCopy, component.selectedRow(); copyText != nil && row != nil {
			text := treeCopyText(row.node)
			component.later(func() { copyText(text) })
		}
	default:
		return false
	}
	return true
}

// handleFilterInput edits the query; Escape or erasing an empty query leaves
// the search on the history of the match it was on.
func (component *TreeSelectorComponent) handleFilterInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	selectedID := component.selectedID()
	if bindings.Matches(event.Raw, "tui.select.cancel") ||
		bindings.Matches(event.Raw, "tui.editor.deleteCharBackward") && component.query() == "" {
		component.filterInput = nil
		if selectedID != "" {
			component.shown = component.index.descend(selectedID, component.history)
		}
		component.refresh(selectedID)
		return
	}
	before := component.query()
	component.filterInput.HandleInput(event)
	if component.query() != before {
		component.refresh(selectedID)
	}
}

// switchVersion shows the previous or next version of the selected row's
// history, keeping the selection on that row.
func (component *TreeSelectorComponent) switchVersion(direction int) {
	row := component.selectedRow()
	if row == nil || len(row.versions) < 2 {
		return
	}
	next := row.version + direction
	if next < 0 || next >= len(row.versions) {
		return
	}
	selected := component.selected
	component.shown = component.index.descend(row.versions[next].Entry.ID, component.history)
	component.rows = component.historyRows()
	component.selected = min(selected, len(component.rows)-1)
}

// jumpFork moves to the previous or next row that has other versions.
func (component *TreeSelectorComponent) jumpFork(direction int) {
	for index := component.selected + direction; index >= 0 && index < len(component.rows); index += direction {
		if len(component.rows[index].versions) > 1 {
			component.selected = index
			return
		}
	}
}

// WantsMouseMotion turns on hover reports while the selector holds focus.
func (component *TreeSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse drives the shared list pointer semantic: hover highlights,
// a click jumps, the wheel scrolls.
func (component *TreeSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	component.mu.Lock()
	defer component.unlockAndRun()
	if component.labelInput != nil || len(component.rows) == 0 {
		return false
	}
	return tui.HandleListMouse(component, event)
}

// The List* methods run under HandleMouse's lock.

// ListRowAt maps a rendered row to its row index.
func (component *TreeSelectorComponent) ListRowAt(row int) (int, bool) {
	return tui.ListRowIndex(row, component.treeTop, component.rowStart, component.rowN, len(component.rows))
}

// ListSelectRow moves the highlight without re-anchoring the window, so
// hover can never shift rows under the cursor.
func (component *TreeSelectorComponent) ListSelectRow(index int) {
	component.window.Freeze()
	component.selected = index
}

// ListScroll moves the selection three rows per tick, recentring like
// keyboard paging does.
func (component *TreeSelectorComponent) ListScroll(direction int) {
	component.window.Recenter()
	component.selected = max(0, min(component.selected+direction*3, len(component.rows)-1))
}

// ListConfirm goes to the selected row, as Enter does.
func (component *TreeSelectorComponent) ListConfirm() { component.confirm() }

func (component *TreeSelectorComponent) confirm() {
	if row, onSelect := component.selectedRow(), component.onSelect; row != nil && onSelect != nil {
		id := row.end
		component.later(func() { onSelect(id) })
	}
}

func (component *TreeSelectorComponent) move(delta int) {
	if len(component.rows) == 0 {
		return
	}
	component.selected = (component.selected + delta + len(component.rows)) % len(component.rows)
}

func (component *TreeSelectorComponent) setEntries(entries bool) {
	selectedID := component.selectedID()
	component.entries = entries
	component.height = 0
	component.refresh(selectedID)
}

func (component *TreeSelectorComponent) editLabel() {
	row := component.selectedRow()
	if row == nil || component.onLabelChange == nil {
		return
	}
	component.labelEntryID = row.node.Entry.ID
	component.labelInput = tui.NewInput()
	component.labelInput.Prompt = "label: "
	if row.node.Label != nil {
		component.labelInput.SetValue(*row.node.Label)
		component.labelInput.HandleInput(tui.KeyEvent{Raw: "\x05"})
	}
	component.labelInput.SetFocused(component.focused)
}

func (component *TreeSelectorComponent) saveLabel() {
	value := strings.TrimSpace(component.labelInput.GetValue())
	var label *string
	if value != "" {
		label = &value
	}
	if node := component.index.byID[component.labelEntryID]; node != nil {
		node.Label = label
		node.LabelTimestamp = nil
		if label != nil {
			now := component.now().Format(time.RFC3339Nano)
			node.LabelTimestamp = &now
		}
	}
	if onLabelChange := component.onLabelChange; onLabelChange != nil {
		id := component.labelEntryID
		component.later(func() { onLabelChange(id, label) })
	}
	component.labelInput = nil
	component.refresh(component.labelEntryID)
}

func (component *TreeSelectorComponent) SetFocused(focused bool) {
	component.mu.Lock()
	defer component.mu.Unlock()
	component.focused = focused
	for _, input := range []*tui.Input{component.labelInput, component.filterInput} {
		if input != nil {
			input.SetFocused(focused)
		}
	}
}

func (component *TreeSelectorComponent) Invalidate() {}

// Render lays out, top to bottom: the counts, the rows, the selected turn's
// reply and the key hints.
func (component *TreeSelectorComponent) Render(width int) []string {
	component.mu.Lock()
	defer component.mu.Unlock()
	width = max(1, width)
	lines := []string{component.renderCounts(width), ""}
	lines = append(lines, component.renderRows(width, len(lines))...)
	lines = append(lines, "")
	lines = append(lines, component.renderPreview(width)...)
	return append(lines, "", component.renderFooter(width))
}

// renderCounts is the dim header: honest counts, and the scroll position
// once the list scrolls.
func (component *TreeSelectorComponent) renderCounts(width int) string {
	left := "  " + strings.Join(component.titleParts(), " · ")
	position := ""
	if count := len(component.rows); count > component.maxVisible {
		position = fmt.Sprintf("%d/%d  ", component.selected+1, count)
	}
	gap := width - tui.VisibleWidth(left) - tui.VisibleWidth(position)
	if gap < 1 {
		return tui.TruncateToWidth(theme.FG("dim", left), width, "…", false)
	}
	return theme.FG("dim", left) + strings.Repeat(" ", gap) + theme.FG("dim", position)
}

// titleParts counts turns and branches across the whole session.
func (component *TreeSelectorComponent) titleParts() []string {
	var parts []string
	if component.entries {
		parts = append(parts, "entries")
	}
	switch prompts := component.index.prompts; prompts {
	case 0:
		parts = append(parts, "no turns yet")
	case 1:
		parts = append(parts, "1 turn")
	default:
		parts = append(parts, fmt.Sprintf("%d turns", prompts))
	}
	if branches := component.index.branches; branches > 1 {
		parts = append(parts, fmt.Sprintf("%d branches", branches))
	}
	return parts
}

// rowLayout reports where the last render put the rows.
func (component *TreeSelectorComponent) rowLayout() (int, int, int) {
	component.mu.Lock()
	defer component.mu.Unlock()
	return component.treeTop, component.rowStart, component.rowN
}

func (component *TreeSelectorComponent) renderRows(width, top int) (lines []string) {
	component.height = max(component.height, min(component.maxVisible, max(1, len(component.rows))))
	lines = make([]string, 0, component.height)
	defer func() {
		for len(lines) < component.height {
			lines = append(lines, "")
		}
	}()
	if len(component.rows) == 0 {
		component.treeTop, component.rowStart, component.rowN = top, 0, 0
		lines = append(lines, tui.TruncateToWidth("  "+theme.FG("muted", component.emptyText()), width, "…", false))
		return lines
	}
	start := component.window.Start(component.selected, len(component.rows), component.maxVisible)
	end := min(start+component.maxVisible, len(component.rows))
	component.treeTop, component.rowStart, component.rowN = top, start, end-start
	turnWidth := 0
	for _, row := range component.rows[start:end] {
		turnWidth = max(turnWidth, len(fmt.Sprint(row.turn)))
	}
	component.textColumn = 2
	if turnWidth > 0 && width >= 40 {
		component.textColumn += turnWidth + 2
	}
	now := component.now()
	for index := start; index < end; index++ {
		lines = append(lines, component.renderRow(component.rows[index], index == component.selected, turnWidth, width, now))
	}
	return lines
}

func (component *TreeSelectorComponent) emptyText() string {
	if component.query() != "" {
		return "Nothing matches “" + component.query() + "”"
	}
	return "Nothing to show yet"
}

// renderRow lays out a row: the current-position dot, the turn number, the
// text, and on the right the versions, labels and, when selected, the time.
// Rows outside the current history are muted.
func (component *TreeSelectorComponent) renderRow(row treeRow, selected bool, turnWidth, width int, now time.Time) string {
	marker := "  "
	if row.current {
		marker = theme.FG("accent", "●") + " "
	}
	number := ""
	if turnWidth > 0 && width >= 40 {
		number = strings.Repeat(" ", turnWidth) + "  "
		if row.turn > 0 {
			number = theme.FG("dim", fmt.Sprintf("%*d", turnWidth, row.turn)) + "  "
		}
	}
	lead := 2 + tui.VisibleWidth(number)
	meta := component.rowMeta(row, selected, now)
	metaWidth := tui.VisibleWidth(meta)
	if metaWidth > 0 && width-lead-metaWidth-3 < 16 {
		meta, metaWidth = "", 0
	}
	textWidth := max(0, width-lead-metaWidth-boolInt(metaWidth > 0)*3-1)
	text := tui.TruncateToWidth(row.text, textWidth, "…", false)

	color := "text"
	switch {
	case row.kind != treeRowPrompt:
		color = "dim"
	case !row.inContext:
		color = "muted"
	}
	line := marker + number + theme.FG(color, text)
	gap := max(0, width-lead-tui.VisibleWidth(text)-metaWidth-1)
	line += strings.Repeat(" ", gap) + meta + " "
	line = tui.TruncateToWidth(line, width, "", false)
	if selected {
		line = theme.BG("selectedBg", line)
	}
	return line
}

// rowMeta is the dim right column: the version shown where the history
// forks, labels, and the time of the selected row.
func (component *TreeSelectorComponent) rowMeta(row treeRow, selected bool, now time.Time) string {
	var parts []string
	if label := row.node.Label; label != nil && *label != "" {
		text := "#" + *label
		if component.showLabelTimestamps && row.node.LabelTimestamp != nil {
			text += " " + formatTreeTime(*row.node.LabelTimestamp, now)
		}
		parts = append(parts, theme.FG("mdLink", text))
	}
	if selected {
		if stamp := formatTreeTime(row.node.Entry.Timestamp, now); stamp != "" {
			parts = append(parts, theme.FG("dim", stamp))
		}
	}
	if len(row.versions) > 1 {
		color := "dim"
		if selected {
			color = "accent"
		}
		parts = append(parts, theme.FG(color, fmt.Sprintf("%d/%d", row.version+1, len(row.versions))))
	}
	return strings.Join(parts, "  ")
}

// renderPreview shows the selected turn's reply, dim, aligned with the row
// text and clipped to a fixed height.
func (component *TreeSelectorComponent) renderPreview(width int) []string {
	lines := make([]string, 0, treePreviewLines)
	if row := component.selectedRow(); row != nil && width >= 24 {
		indent := strings.Repeat(" ", component.textColumn)
		wrapped := tui.WrapTextWithANSI(plainPreview(row.reply), width-len(indent)-2)
		for index, line := range wrapped {
			if index == treePreviewLines-1 && len(wrapped) > treePreviewLines {
				line = tui.TruncateToWidth(line, width-len(indent)-3, "", false) + "…"
			}
			lines = append(lines, indent+theme.FG("muted", line))
			if len(lines) == treePreviewLines {
				break
			}
		}
	}
	for len(lines) < treePreviewLines {
		lines = append(lines, "")
	}
	return lines
}

var (
	previewHeading = regexp.MustCompile(`(?m)^\s*#{1,6}\s+`)
	previewMarks   = strings.NewReplacer("**", "", "__", "", "`", "")
)

// plainPreview flattens a reply's markdown into one paragraph of plain text.
func plainPreview(text string) string {
	return strings.Join(strings.Fields(previewMarks.Replace(previewHeading.ReplaceAllString(text, ""))), " ")
}

// renderFooter is one dim line: the key hints, or the query or label being
// typed.
func (component *TreeSelectorComponent) renderFooter(width int) string {
	switch {
	case component.labelInput != nil:
		hint := "  " + RawKeyHint("enter", "save") + "  " + RawKeyHint("esc", "cancel")
		inputWidth := max(1, width-2-tui.VisibleWidth(hint))
		if width < 40 {
			hint, inputWidth = "", max(1, width-2)
		}
		return tui.TruncateToWidth("  "+component.labelInput.Render(inputWidth)[0]+hint, width, "", false)
	case component.filterInput != nil:
		count := len(component.rows)
		status := fmt.Sprintf("%d matches", count)
		if count == 1 {
			status = "1 match"
		}
		status = "  " + theme.FG("dim", status) + "  " + RawKeyHint("esc", "clear")
		inputWidth := max(1, width-2-tui.VisibleWidth(status))
		if width < 40 {
			status, inputWidth = "", max(1, width-2)
		}
		return tui.TruncateToWidth("  "+component.filterInput.Render(inputWidth)[0]+status, width, "", false)
	}
	row := component.selectedRow()
	hints := []string{RawKeyHint("enter", "go to")}
	if row != nil && len(row.versions) > 1 {
		hints = append(hints, RawKeyHint("←→", "versions"))
	}
	if row != nil && row.kind == treeRowPrompt {
		hints = append(hints, RawKeyHint("e", "edit"))
	}
	hints = append(hints, RawKeyHint("/", "search"))
	if component.allHints {
		view := "entries"
		if component.entries {
			view = "turns"
		}
		hints = append(hints, RawKeyHint("f", "fork"), RawKeyHint("tab", view), KeyHint("app.tree.editLabel", "label"))
	} else {
		hints = append(hints, RawKeyHint("?", "more"))
	}
	separator := theme.FG("dim", " · ")
	line := "  "
	for index, hint := range hints {
		next := line + hint
		if index > 0 {
			next = line + separator + hint
		}
		if tui.VisibleWidth(next) > width {
			break
		}
		line = next
	}
	return tui.TruncateToWidth(line, width, "", false)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func treeCopyText(node *sessionstore.SessionTreeNode) string {
	if node == nil {
		return ""
	}
	entry := node.Entry
	switch entry.Type {
	case "message":
		_, text := sessionMessageRoleText(entry.Message)
		return strings.TrimSpace(text)
	case "compaction", "branch_summary":
		return strings.TrimSpace(entry.Summary)
	}
	return ""
}

// formatTreeTime keeps times short: the clock today, the date this year.
func formatTreeTime(value string, now time.Time) string {
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	timestamp, now = timestamp.Local(), now.Local()
	switch {
	case timestamp.Year() == now.Year() && timestamp.YearDay() == now.YearDay():
		return timestamp.Format("15:04")
	case timestamp.Year() == now.Year():
		return timestamp.Format("Jan 2")
	default:
		return timestamp.Format("Jan 2006")
	}
}
