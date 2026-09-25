package modes

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

// The tree reads as a list of turns: user prompts are the rows, rails appear
// only where the history forks, and each branch end shows its last reply so
// an abandoned branch can be resumed where it stopped. The other filter modes
// list individual entries with the same rails.

type treeRowKind int

const (
	treeRowPrompt treeRowKind = iota
	treeRowReply
	treeRowNote
)

// treeEntryInfo is what the tree needs from an entry, parsed once per
// selector so rebuilding on every filter keystroke stays linear.
type treeEntryInfo struct {
	role   string
	kind   treeRowKind
	text   string
	search string
	// full is the entry's own text, bounded, for the preview pane.
	full string
	// messageBelow reports whether any descendant is a message entry;
	// a message without one ends a branch.
	messageBelow bool
	// prompts counts the user prompts in the subtree, the entry included.
	prompts int
}

type treeIndex struct {
	byID     map[string]*sessionstore.SessionTreeNode
	info     map[string]*treeEntryInfo
	prompts  int
	branches int
}

func newTreeIndex(roots []*sessionstore.SessionTreeNode) *treeIndex {
	index := &treeIndex{
		byID: make(map[string]*sessionstore.SessionTreeNode),
		info: make(map[string]*treeEntryInfo),
	}
	var preorder []*sessionstore.SessionTreeNode
	stack := appendReversedTreeNodes(nil, roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		index.byID[node.Entry.ID] = node
		index.info[node.Entry.ID] = newTreeEntryInfo(node.Entry)
		preorder = append(preorder, node)
		stack = appendReversedTreeNodes(stack, node.Children)
	}
	// Reverse pre-order visits children before their parent.
	for position := len(preorder) - 1; position >= 0; position-- {
		node := preorder[position]
		info := index.info[node.Entry.ID]
		if info.role == "user" {
			info.prompts++
		}
		for _, child := range node.Children {
			childInfo := index.info[child.Entry.ID]
			info.prompts += childInfo.prompts
			info.messageBelow = info.messageBelow || childInfo.messageBelow || child.Entry.Type == "message"
		}
		if node.Entry.Type == "message" && !info.messageBelow && info.role != "system" {
			index.branches++
		}
	}
	for _, root := range roots {
		index.prompts += index.info[root.Entry.ID].prompts
	}
	return index
}

func newTreeEntryInfo(entry sessionstore.SessionEntry) *treeEntryInfo {
	info := &treeEntryInfo{kind: treeRowNote}
	full := ""
	switch entry.Type {
	case "message":
		var message struct {
			Role     string          `json:"role"`
			Content  json.RawMessage `json:"content"`
			ToolName string          `json:"toolName"`
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
			if strings.TrimSpace(full) == "" {
				info.text = "(no text)"
			}
		case "toolResult":
			info.text = "tool " + message.ToolName + ": "
		default:
			info.text = message.Role + ": "
		}
	case "branch_summary":
		info.text, full = "summary · ", entry.Summary
	case "compaction":
		info.text, full = "compacted · ", entry.Summary
	case "model_change":
		info.text = "model · " + entry.ModelID
	case "thinking_level_change":
		info.text = "thinking · " + entry.ThinkingLevel
	case "session_info":
		info.text = "name · " + entry.Name
	case "custom_message", "custom":
		info.text = entry.CustomType
	case "label":
		info.text = "label"
		if entry.Label != nil {
			info.text += " · " + *entry.Label
		}
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

type treeRow struct {
	node *sessionstore.SessionTreeNode
	// rail holds the branch lanes, two cells per fork depth; "" on a
	// history that never forked.
	rail    string
	kind    treeRowKind
	text    string
	onPath  bool
	current bool
	// forkStart marks the first row of a branch below a fork.
	forkStart bool
}

type treeView struct {
	rows     []treeRow
	children map[string][]string
	// path holds the entries from the root to the current leaf.
	path map[string]bool
}

func buildTreeView(index *treeIndex, roots []*sessionstore.SessionTreeNode, leafID, filterMode, query string) treeView {
	onPath := make(map[string]bool)
	view := treeView{children: make(map[string][]string), path: onPath}
	for id := leafID; id != ""; {
		node := index.byID[id]
		if node == nil {
			break
		}
		onPath[id] = true
		if node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}

	tokens := strings.Fields(strings.ToLower(query))
	inView := make(map[string]bool)
	visible := make(map[string]bool)
	ordered := make([]*sessionstore.SessionTreeNode, 0, len(index.byID))
	stack := appendReversedTreeNodes(nil, prioritizeActiveTreeNodes(roots, onPath))
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ordered = append(ordered, node)
		id := node.Entry.ID
		info := index.info[id]
		shown := false
		if filterMode == "default" {
			shown = treeTurnVisible(node, info, id == leafID)
		} else {
			shown = treeEntryVisible(node, id == leafID, filterMode)
		}
		inView[id] = shown
		visible[id] = shown && treeMatchesTokens(node, info, tokens)
		stack = appendReversedTreeNodes(stack, prioritizeActiveTreeNodes(node.Children, onPath))
	}

	// The current position is the leaf, or its nearest ancestor when the
	// leaf itself is bookkeeping the view hides; a search never moves it.
	current := ""
	for id := leafID; id != ""; {
		if inView[id] {
			if visible[id] {
				current = id
			}
			break
		}
		node := index.byID[id]
		if node == nil || node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}

	visibleChildren := make(map[string][]*sessionstore.SessionTreeNode)
	for _, node := range ordered {
		if !visible[node.Entry.ID] {
			continue
		}
		parentID := nearestVisibleTreeParent(node, index.byID, visible)
		visibleChildren[parentID] = append(visibleChildren[parentID], node)
		view.children[parentID] = append(view.children[parentID], node.Entry.ID)
	}

	type frame struct {
		node      *sessionstore.SessionTreeNode
		lanes     []bool
		connector string
	}
	var frames []frame
	pushChildren := func(children []*sessionstore.SessionTreeNode, lanes []bool) {
		if len(children) == 1 {
			frames = append(frames, frame{node: children[0], lanes: lanes})
			return
		}
		for position := len(children) - 1; position >= 0; position-- {
			connector := "├ "
			if position == len(children)-1 {
				connector = "└ "
			}
			frames = append(frames, frame{node: children[position], lanes: lanes, connector: connector})
		}
	}
	pushChildren(visibleChildren[""], nil)
	for len(frames) > 0 {
		item := frames[len(frames)-1]
		frames = frames[:len(frames)-1]
		id := item.node.Entry.ID
		info := index.info[id]
		var rail strings.Builder
		for _, lane := range item.lanes {
			if lane {
				rail.WriteString("│ ")
			} else {
				rail.WriteString("  ")
			}
		}
		rail.WriteString(item.connector)
		view.rows = append(view.rows, treeRow{
			node: item.node, rail: rail.String(), kind: info.kind, text: info.text,
			onPath: onPath[id], current: id == current, forkStart: item.connector != "",
		})
		lanes := item.lanes
		if item.connector != "" {
			lanes = append(append([]bool(nil), item.lanes...), item.connector == "├ ")
		}
		pushChildren(visibleChildren[id], lanes)
	}
	return view
}

// treeTurnVisible is the default view: prompts, summaries, the current entry,
// and the reply that ends each branch.
func treeTurnVisible(node *sessionstore.SessionTreeNode, info *treeEntryInfo, current bool) bool {
	switch node.Entry.Type {
	case "branch_summary", "compaction":
		return true
	case "message":
		return info.role == "user" || current || !info.messageBelow && info.role != "system"
	}
	return false
}

func treeMatchesTokens(node *sessionstore.SessionTreeNode, info *treeEntryInfo, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
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

func nearestVisibleTreeParent(
	node *sessionstore.SessionTreeNode,
	byID map[string]*sessionstore.SessionTreeNode,
	visible map[string]bool,
) string {
	parent := node.Entry.ParentID
	for parent != nil {
		if visible[*parent] {
			return *parent
		}
		parentNode := byID[*parent]
		if parentNode == nil {
			break
		}
		parent = parentNode.Entry.ParentID
	}
	return ""
}

func prioritizeActiveTreeNodes(nodes []*sessionstore.SessionTreeNode, active map[string]bool) []*sessionstore.SessionTreeNode {
	result := make([]*sessionstore.SessionTreeNode, 0, len(nodes))
	for _, node := range nodes {
		if active[node.Entry.ID] {
			result = append(result, node)
		}
	}
	for _, node := range nodes {
		if !active[node.Entry.ID] {
			result = append(result, node)
		}
	}
	return result
}

func appendReversedTreeNodes(
	stack, nodes []*sessionstore.SessionTreeNode,
) []*sessionstore.SessionTreeNode {
	for index := len(nodes) - 1; index >= 0; index-- {
		stack = append(stack, nodes[index])
	}
	return stack
}

var treeFilterModes = []string{"default", "no-tools", "user-only", "labeled-only", "all"}

var treeFilterNames = map[string]string{
	"no-tools": "messages", "user-only": "prompts only", "labeled-only": "labeled", "all": "all entries",
}

// TreeSelectorComponent is the session tree opened by /tree and double Escape.
type TreeSelectorComponent struct {
	roots               []*sessionstore.SessionTreeNode
	index               *treeIndex
	leafID              string
	filterMode          string
	showLabelTimestamps bool
	view                treeView
	selected            int
	maxVisible          int
	// height only grows within a view, so typing a search never shrinks
	// the list under the cursor; switching views sizes it afresh.
	height        int
	filterInput   *tui.Input
	labelInput    *tui.Input
	labelEntryID  string
	focused       bool
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
	treeTop, rowStart, rows int

	OnCopy func(string)
	// OnFork forks the selected entry into a new session; before is set for
	// a user prompt, which is forked from just before it like /fork.
	OnFork func(entryID string, before bool)
	// Framed renders for a titled modal: the frame carries the name, so the
	// tree opens with a quiet count line instead of its own rule, and a wide
	// modal previews the selected turn beside the list.
	Framed bool
	// listWidth is where the last framed render ended the list, so pointer
	// input over the preview never selects a row.
	listWidth int
	allHints  bool
}

// SetMaxVisible sizes the list for the space its container offers.
func (component *TreeSelectorComponent) SetMaxVisible(rows int) {
	component.mu.Lock()
	defer component.mu.Unlock()
	component.maxVisible = max(5, rows)
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
	if filterMode == "" {
		filterMode = "default"
	}
	component := &TreeSelectorComponent{
		roots: roots, index: newTreeIndex(roots), leafID: leafID, filterMode: filterMode,
		maxVisible: max(5, terminalHeight/2), onSelect: onSelect, onCancel: onCancel,
		onLabelChange: onLabelChange, now: time.Now,
	}
	if initialSelectedID == "" {
		initialSelectedID = leafID
	}
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
	component.view = buildTreeView(component.index, component.roots, component.leafID, component.filterMode, component.query())
	component.selectNearest(preferredID)
}

func (component *TreeSelectorComponent) selectNearest(id string) {
	for id != "" {
		for index, row := range component.view.rows {
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
	component.selected = max(0, len(component.view.rows)-1)
}

func (component *TreeSelectorComponent) selectedNode() *sessionstore.SessionTreeNode {
	if component.selected < 0 || component.selected >= len(component.view.rows) {
		return nil
	}
	return component.view.rows[component.selected].node
}

func (component *TreeSelectorComponent) selectedID() string {
	if node := component.selectedNode(); node != nil {
		return node.Entry.ID
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
	case key == "?":
		component.allHints = !component.allHints
	case key == "/":
		component.filterInput = tui.NewInput()
		component.filterInput.Prompt = "/"
		component.filterInput.SetFocused(component.focused)
	case key == "f":
		if node := component.selectedNode(); node != nil && component.OnFork != nil {
			id, before, fork := node.Entry.ID, component.index.info[node.Entry.ID].role == "user", component.OnFork
			component.later(func() { fork(id, before) })
		}
	case tui.MatchesKey(raw, "tab") || bindings.Matches(raw, "app.tree.filter.cycleForward"):
		component.cycleFilter(1)
	case tui.MatchesKey(raw, "shift+tab") || bindings.Matches(raw, "app.tree.filter.cycleBackward"):
		component.cycleFilter(-1)
	case bindings.Matches(raw, "app.tree.filter.default"):
		component.setFilter("default", false)
	case bindings.Matches(raw, "app.tree.filter.noTools"):
		component.setFilter("no-tools", true)
	case bindings.Matches(raw, "app.tree.filter.userOnly"):
		component.setFilter("user-only", true)
	case bindings.Matches(raw, "app.tree.filter.labeledOnly"):
		component.setFilter("labeled-only", true)
	case bindings.Matches(raw, "app.tree.filter.all"):
		component.setFilter("all", true)
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
		component.selected = max(0, len(component.view.rows)-1)
	}
}

// handleNavigation covers the keys that work the same while filtering:
// moving, jumping between forks, confirming and copying.
func (component *TreeSelectorComponent) handleNavigation(raw string) bool {
	bindings := tui.GetKeybindings()
	last := max(0, len(component.view.rows)-1)
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
	case bindings.Matches(raw, "app.tree.foldOrUp") || component.filterInput == nil && tui.MatchesKey(raw, "left"):
		component.jumpFork(-1)
	case bindings.Matches(raw, "app.tree.unfoldOrDown") || component.filterInput == nil && tui.MatchesKey(raw, "right"):
		component.jumpFork(1)
	case bindings.Matches(raw, "tui.select.confirm"):
		component.confirm()
	case bindings.Matches(raw, "app.message.copy"):
		if copyText := component.OnCopy; copyText != nil {
			text := treeCopyText(component.selectedNode())
			component.later(func() { copyText(text) })
		}
	default:
		return false
	}
	return true
}

// handleFilterInput edits the query; Escape or erasing an empty query leaves
// filtering and restores the whole tree.
func (component *TreeSelectorComponent) handleFilterInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	selectedID := component.selectedID()
	if bindings.Matches(event.Raw, "tui.select.cancel") ||
		bindings.Matches(event.Raw, "tui.editor.deleteCharBackward") && component.query() == "" {
		component.filterInput = nil
		component.refresh(selectedID)
		return
	}
	before := component.query()
	component.filterInput.HandleInput(event)
	if component.query() != before {
		component.refresh(selectedID)
	}
}

// jumpFork moves to the previous or next row where the history forks: a row
// with several branches below it, or the first row of a branch.
func (component *TreeSelectorComponent) jumpFork(direction int) {
	for index := component.selected + direction; index >= 0 && index < len(component.view.rows); index += direction {
		row := component.view.rows[index]
		if row.forkStart || len(component.view.children[row.node.Entry.ID]) > 1 {
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
	if component.labelInput != nil || len(component.view.rows) == 0 {
		return false
	}
	if component.listWidth > 0 && event.Column >= component.listWidth {
		return false
	}
	return tui.HandleListMouse(component, event)
}

// The List* methods run under HandleMouse's lock.

// ListRowAt maps a rendered row to its tree-view row index.
func (component *TreeSelectorComponent) ListRowAt(row int) (int, bool) {
	return tui.ListRowIndex(row, component.treeTop, component.rowStart, component.rows, len(component.view.rows))
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
	component.selected = max(0, min(component.selected+direction*3, len(component.view.rows)-1))
}

// ListConfirm jumps to the selected entry, as Enter does.
func (component *TreeSelectorComponent) ListConfirm() { component.confirm() }

func (component *TreeSelectorComponent) confirm() {
	if id, onSelect := component.selectedID(), component.onSelect; id != "" && onSelect != nil {
		component.later(func() { onSelect(id) })
	}
}

func (component *TreeSelectorComponent) move(delta int) {
	if len(component.view.rows) == 0 {
		return
	}
	component.selected = (component.selected + delta + len(component.view.rows)) % len(component.view.rows)
}

func (component *TreeSelectorComponent) setFilter(filter string, toggle bool) {
	selectedID := component.selectedID()
	if toggle && component.filterMode == filter {
		filter = "default"
	}
	component.filterMode = filter
	component.height = 0
	component.refresh(selectedID)
}

func (component *TreeSelectorComponent) cycleFilter(delta int) {
	index := 0
	for candidate, mode := range treeFilterModes {
		if mode == component.filterMode {
			index = candidate
			break
		}
	}
	component.setFilter(treeFilterModes[(index+delta+len(treeFilterModes))%len(treeFilterModes)], false)
}

func (component *TreeSelectorComponent) editLabel() {
	node := component.selectedNode()
	if node == nil || component.onLabelChange == nil {
		return
	}
	component.labelEntryID = node.Entry.ID
	component.labelInput = tui.NewInput()
	component.labelInput.Prompt = "label: "
	if node.Label != nil {
		component.labelInput.SetValue(*node.Label)
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

func (component *TreeSelectorComponent) Render(width int) []string {
	component.mu.Lock()
	defer component.mu.Unlock()
	width = max(1, width)
	if component.Framed {
		component.listWidth = 0
		lines := []string{component.renderCounts(width), ""}
		if listWidth := treeListWidth(width); listWidth < width {
			component.listWidth = listWidth
			rows := component.renderRows(listWidth, len(lines))
			preview := component.renderPreview(width-listWidth-3, len(rows))
			for index, row := range rows {
				pad := strings.Repeat(" ", max(0, listWidth-tui.VisibleWidth(row)))
				lines = append(lines, row+pad+" "+theme.FG("borderMuted", "│")+" "+preview[index])
			}
		} else {
			lines = append(lines, component.renderRows(width, len(lines))...)
		}
		return append(lines, "", component.renderFooter(width))
	}
	lines := []string{component.renderTitle(width)}
	lines = append(lines, component.renderRows(width, len(lines))...)
	return append(lines, component.renderFooter(width))
}

// renderCounts is the framed header: the view and honest counts, dim, with
// the scroll position right-aligned once the list scrolls.
func (component *TreeSelectorComponent) renderCounts(width int) string {
	left := "  " + strings.Join(component.titleParts()[1:], " · ")
	position := ""
	if count := len(component.view.rows); count > component.maxVisible {
		position = fmt.Sprintf("%d/%d  ", component.selected+1, count)
	}
	gap := width - tui.VisibleWidth(left) - tui.VisibleWidth(position)
	if gap < 1 {
		return tui.TruncateToWidth(theme.FG("dim", left), width, "…", false)
	}
	return theme.FG("dim", left) + strings.Repeat(" ", gap) + theme.FG("dim", position)
}

// renderTitle is the one rule that separates the tree from the transcript:
// the view, honest counts, and the scroll position once the list scrolls.
func (component *TreeSelectorComponent) renderTitle(width int) string {
	parts := component.titleParts()
	title := " " + strings.Join(parts, " · ") + " "
	position := ""
	if count := len(component.view.rows); count > component.maxVisible {
		position = fmt.Sprintf(" %d/%d ", component.selected+1, count)
	}
	lead := "──"
	fill := width - tui.VisibleWidth(lead+title+position) - 2
	if fill < 1 {
		return tui.TruncateToWidth(theme.FG("muted", strings.TrimSpace(title)), width, "…", false)
	}
	return theme.FG("borderMuted", lead) + theme.FG("muted", title) +
		theme.FG("borderMuted", strings.Repeat("─", fill)) + theme.FG("dim", position) + theme.FG("borderMuted", "──")
}

// titleParts names the view and counts turns and branches honestly.
func (component *TreeSelectorComponent) titleParts() []string {
	parts := []string{"Tree"}
	if name := treeFilterNames[component.filterMode]; name != "" {
		parts = append(parts, name)
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
	} else if component.index.prompts > 1 {
		parts = append(parts, "no forks")
	}
	return parts
}

func (component *TreeSelectorComponent) setRowLayout(top, start, count int) {
	component.treeTop, component.rowStart, component.rows = top, start, count
}

// rowLayout reports where the last render put the rows.
func (component *TreeSelectorComponent) rowLayout() (int, int, int) {
	component.mu.Lock()
	defer component.mu.Unlock()
	return component.treeTop, component.rowStart, component.rows
}

func (component *TreeSelectorComponent) renderRows(width, top int) []string {
	component.height = max(component.height, min(component.maxVisible, max(1, len(component.view.rows))))
	lines := make([]string, 0, component.height)
	if len(component.view.rows) == 0 {
		component.setRowLayout(top, 0, 0)
		lines = append(lines, tui.TruncateToWidth("  "+theme.FG("muted", component.emptyText()), width, "…", false))
	} else {
		start := component.window.Start(component.selected, len(component.view.rows), component.maxVisible)
		end := min(start+component.maxVisible, len(component.view.rows))
		component.setRowLayout(top, start, end-start)
		now := component.now()
		for index := start; index < end; index++ {
			lines = append(lines, component.renderRow(component.view.rows[index], index == component.selected, width, now))
		}
	}
	for len(lines) < component.height {
		lines = append(lines, "")
	}
	return lines
}

func (component *TreeSelectorComponent) emptyText() string {
	switch {
	case component.query() != "":
		return "Nothing matches “" + component.query() + "”"
	case component.filterMode == "labeled-only":
		return "No labels yet"
	default:
		return "Nothing to show yet"
	}
}

// renderRow lays out cursor, rails, text and right-aligned metadata. Rows
// off the current path are muted; the current position carries the one
// accent dot.
func (component *TreeSelectorComponent) renderRow(row treeRow, selected bool, width int, now time.Time) string {
	cursor := "  "
	if selected {
		cursor = theme.FG("accent", "› ")
	}
	rail := row.rail
	if limit := width / 3; tui.VisibleWidth(rail) > limit {
		runes := []rune(rail)
		rail = "…" + string(runes[len(runes)-max(0, limit-1):])
	}
	marker := ""
	if row.current {
		marker = "● "
	}
	lead := 2 + tui.VisibleWidth(rail) + tui.VisibleWidth(marker)
	meta := component.rowMeta(row, selected, width, now)
	metaWidth := tui.VisibleWidth(meta)
	if metaWidth > 0 && width-lead-metaWidth-2 < 16 {
		meta, metaWidth = "", 0
	}
	textWidth := max(0, width-lead-metaWidth-boolInt(metaWidth > 0)*2)
	text := row.text
	if row.kind == treeRowReply {
		text = "↳ " + text
	}
	text = tui.TruncateToWidth(text, textWidth, "…", false)

	textColor := "text"
	switch {
	case row.kind != treeRowPrompt:
		textColor = "dim"
		if row.onPath {
			textColor = "muted"
		}
	case !row.onPath:
		textColor = "muted"
	}
	line := cursor + theme.FG("borderMuted", rail) + theme.FG("accent", marker) + theme.FG(textColor, text)
	if selected || meta != "" {
		gap := max(0, width-lead-tui.VisibleWidth(text)-metaWidth)
		line += strings.Repeat(" ", gap) + meta
	}
	line = tui.TruncateToWidth(line, width, "", false)
	if selected {
		line = theme.BG("selectedBg", line)
	}
	return line
}

// rowMeta is the dim right column: labels always, and on the selected row
// the size of a branch where one starts and when a prompt was sent.
func (component *TreeSelectorComponent) rowMeta(row treeRow, selected bool, width int, now time.Time) string {
	var parts []string
	if label := row.node.Label; label != nil && *label != "" {
		text := "#" + *label
		if component.showLabelTimestamps && row.node.LabelTimestamp != nil {
			text += " " + formatTreeTime(*row.node.LabelTimestamp, now)
		}
		parts = append(parts, theme.FG("mdLink", text))
	}
	if !selected {
		return strings.Join(parts, "  ")
	}
	if row.forkStart && component.filterMode == "default" && width >= 56 {
		switch prompts := component.index.info[row.node.Entry.ID].prompts; prompts {
		case 0:
		case 1:
			parts = append(parts, theme.FG("dim", "1 turn"))
		default:
			parts = append(parts, theme.FG("dim", fmt.Sprintf("%d turns", prompts)))
		}
	}
	if row.kind == treeRowPrompt && width >= 64 {
		if stamp := formatTreeTime(row.node.Entry.Timestamp, now); stamp != "" {
			parts = append(parts, theme.FG("dim", stamp))
		}
	}
	return strings.Join(parts, "  ")
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
		count := len(component.view.rows)
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
	hints := []string{RawKeyHint("enter", "go to"), RawKeyHint("f", "fork"), RawKeyHint("/", "search")}
	if component.allHints || !component.Framed {
		hints = append(hints, RawKeyHint("tab", "view"))
		if component.index.branches > 1 {
			hints = append(hints, RawKeyHint("←→", "forks"))
		}
		hints = append(hints, KeyHint("app.tree.editLabel", "label"))
	}
	if !component.Framed {
		hints = append(hints, RawKeyHint("esc", "close"))
	} else if !component.allHints {
		hints = append(hints, RawKeyHint("?", "keys"))
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

// treeListWidth splits a wide framed tree: the list keeps what it needs to
// read well and the rest previews the selection; narrow modals stay one column.
func treeListWidth(width int) int {
	if width < 110 {
		return width
	}
	return min(max(56, width*5/11), 72)
}

// renderPreview shows the selected entry in full and, for a prompt, the
// reply that answered it on the history shown, clipped to the list height.
func (component *TreeSelectorComponent) renderPreview(width, height int) []string {
	lines := make([]string, 0, height)
	node := component.selectedNode()
	if node != nil && width >= 20 {
		info := component.index.info[node.Entry.ID]
		color := "text"
		if info.kind != treeRowPrompt {
			color = "muted"
		}
		for _, line := range tui.WrapTextWithANSI(info.full, width) {
			lines = append(lines, theme.FG(color, line))
		}
		if info.kind == treeRowPrompt {
			if reply := component.turnReply(node); reply != "" {
				lines = append(lines, "")
				for _, line := range tui.WrapTextWithANSI(reply, width) {
					lines = append(lines, theme.FG("dim", line))
				}
			}
		}
		if when := formatTreeTime(node.Entry.Timestamp, component.now()); when != "" && len(lines) < height-1 {
			for len(lines) < height-1 {
				lines = append(lines, "")
			}
			lines = append(lines, theme.FG("dim", when))
		}
	}
	if len(lines) > height {
		lines = append(lines[:height-1], theme.FG("dim", "…"))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return lines
}

// turnReply is the last assistant text before the next prompt, following the
// current history where it passes through the turn and the first branch
// elsewhere.
func (component *TreeSelectorComponent) turnReply(prompt *sessionstore.SessionTreeNode) string {
	reply := ""
	for node := prompt; node != nil; {
		next := (*sessionstore.SessionTreeNode)(nil)
		for _, child := range node.Children {
			if next == nil || component.view.path[child.Entry.ID] {
				next = child
			}
		}
		if next == nil {
			break
		}
		info := component.index.info[next.Entry.ID]
		if info.role == "user" {
			break
		}
		if info.role == "assistant" && info.full != "" {
			reply = info.full
		}
		node = next
	}
	return reply
}
