package modes

import (
	"fmt"
	"strings"
	"sync"
	"time"

	sessionstore "github.com/OrdalieTech/orb/codingagent/session"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/codingagent/modes/theme"
)

type treeGutter struct {
	position int
	show     bool
}

type treeVisualNode struct {
	node               *sessionstore.SessionTreeNode
	indent             int
	showConnector      bool
	isLast             bool
	gutters            []treeGutter
	isVirtualRootChild bool
}

type treeRow struct {
	node   *sessionstore.SessionTreeNode
	label  string
	anchor int
}

type treeView struct {
	rows     []treeRow
	byID     map[string]*sessionstore.SessionTreeNode
	parent   map[string]string
	children map[string][]string
}

func buildTreeView(
	roots []*sessionstore.SessionTreeNode,
	leafID, filterMode, query string,
	folded map[string]bool,
	showLabelTimestamps bool,
) treeView {
	view := treeView{
		byID:     make(map[string]*sessionstore.SessionTreeNode),
		parent:   make(map[string]string),
		children: make(map[string][]string),
	}
	stack := appendReversedTreeNodes(nil, roots)
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		view.byID[node.Entry.ID] = node
		stack = appendReversedTreeNodes(stack, node.Children)
	}

	active := make(map[string]bool)
	for id := leafID; id != ""; {
		active[id] = true
		node := view.byID[id]
		if node == nil || node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}

	ordered := make([]*sessionstore.SessionTreeNode, 0, len(view.byID))
	stack = appendReversedTreeNodes(nil, prioritizeActiveTreeNodes(roots, active))
	for len(stack) > 0 {
		node := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ordered = append(ordered, node)
		stack = appendReversedTreeNodes(stack, prioritizeActiveTreeNodes(node.Children, active))
	}

	searchTokens := strings.Fields(strings.ToLower(query))
	// ordered is DFS pre-order, so a parent always precedes its children and one
	// pass builds the skip set. Walking ancestors per node instead is O(n^2) on
	// the near-linear chains real sessions produce.
	foldedDescendants := make(map[string]bool)
	if len(folded) > 0 {
		for _, node := range ordered {
			parent := node.Entry.ParentID
			if parent != nil && (folded[*parent] || foldedDescendants[*parent]) {
				foldedDescendants[node.Entry.ID] = true
			}
		}
	}
	visible := make(map[string]bool)
	for _, node := range ordered {
		visible[node.Entry.ID] = treeEntryVisible(node, node.Entry.ID == leafID, filterMode) &&
			treeMatchesSearch(node, searchTokens) && !foldedDescendants[node.Entry.ID]
	}
	visibleNodes := make(map[string][]*sessionstore.SessionTreeNode)
	for _, node := range ordered {
		if !visible[node.Entry.ID] {
			continue
		}
		parentID := nearestVisibleTreeParent(node, view.byID, visible)
		view.parent[node.Entry.ID] = parentID
		view.children[parentID] = append(view.children[parentID], node.Entry.ID)
		visibleNodes[parentID] = append(visibleNodes[parentID], node)
	}

	visibleRoots := visibleNodes[""]
	multipleRoots := len(visibleRoots) > 1
	visuals := make([]treeVisualNode, 0, len(visible))
	visualStack := make([]treeVisualNode, 0, len(visible))
	for index := len(visibleRoots) - 1; index >= 0; index-- {
		visualStack = append(visualStack, treeVisualNode{
			node: visibleRoots[index], indent: boolInt(multipleRoots),
			showConnector: multipleRoots, isLast: index == len(visibleRoots)-1,
			isVirtualRootChild: multipleRoots,
		})
	}
	for len(visualStack) > 0 {
		item := visualStack[len(visualStack)-1]
		visualStack = visualStack[:len(visualStack)-1]
		visuals = append(visuals, item)

		nodeChildren := visibleNodes[item.node.Entry.ID]
		branched := len(nodeChildren) > 1
		childIndent := item.indent
		if branched || (item.showConnector && item.indent > 0) {
			childIndent++
		}
		childGutters := item.gutters
		if item.showConnector && !item.isVirtualRootChild {
			displayIndent := item.indent
			if multipleRoots {
				displayIndent = max(0, displayIndent-1)
			}
			childGutters = append(append([]treeGutter(nil), item.gutters...), treeGutter{
				position: max(0, displayIndent-1),
				show:     !item.isLast,
			})
		}
		for index := len(nodeChildren) - 1; index >= 0; index-- {
			visualStack = append(visualStack, treeVisualNode{
				node: nodeChildren[index], indent: childIndent,
				showConnector: branched, isLast: index == len(nodeChildren)-1,
				gutters: childGutters,
			})
		}
	}

	view.rows = make([]treeRow, 0, len(visuals))
	for _, item := range visuals {
		id := item.node.Entry.ID
		foldMarker := rune(0)
		if folded[id] {
			foldMarker = '⊞'
		} else if len(view.children[id]) > 0 {
			parentID := view.parent[id]
			if parentID == "" || len(view.children[parentID]) > 1 {
				foldMarker = '⊟'
			}
		}
		prefix := treeVisualPrefix(item, multipleRoots, foldMarker)
		if folded[item.node.Entry.ID] && (!item.showConnector || item.isVirtualRootChild) {
			prefix += "⊞ "
		}
		if active[item.node.Entry.ID] {
			prefix += "• "
		}
		anchor := tui.VisibleWidth(prefix)
		if item.node.Label != nil && *item.node.Label != "" {
			prefix += "[" + *item.node.Label + "] "
			if showLabelTimestamps && item.node.LabelTimestamp != nil {
				prefix += formatTreeLabelTimestamp(*item.node.LabelTimestamp, time.Now()) + " "
			}
		}
		view.rows = append(view.rows, treeRow{
			node: item.node, label: prefix + sessionEntryLabel(item.node.Entry), anchor: anchor,
		})
	}
	return view
}

func treeMatchesSearch(node *sessionstore.SessionTreeNode, tokens []string) bool {
	if len(tokens) == 0 {
		return true
	}
	entry := node.Entry
	parts := []string{entry.Type, entry.CustomType, entry.Summary, entry.ModelID, entry.ThinkingLevel, entry.Name}
	if node.Label != nil {
		parts = append(parts, *node.Label)
	}
	if entry.Type == "message" {
		role, text := sessionMessageRoleText(entry.Message)
		parts = append(parts, role, text)
	}
	text := strings.ToLower(strings.Join(parts, " "))
	for _, token := range tokens {
		if !strings.Contains(text, token) {
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

func treeVisualPrefix(node treeVisualNode, multipleRoots bool, foldMarker rune) string {
	displayIndent := node.indent
	if multipleRoots {
		displayIndent = max(0, displayIndent-1)
	}
	prefix := []rune(strings.Repeat(" ", displayIndent*3))
	connectorPosition := -1
	if node.showConnector && !node.isVirtualRootChild {
		connectorPosition = displayIndent - 1
	}
	for level := range displayIndent {
		offset := level * 3
		for _, gutter := range node.gutters {
			if gutter.position == level && gutter.show {
				prefix[offset] = '│'
			}
		}
		if level == connectorPosition {
			if node.isLast {
				prefix[offset] = '└'
			} else {
				prefix[offset] = '├'
			}
			if foldMarker == 0 {
				foldMarker = '─'
			}
			prefix[offset+1] = foldMarker
		}
	}
	return string(prefix)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// TreeSelectorComponent is the dedicated /tree picker used by pi.
type TreeSelectorComponent struct {
	roots               []*sessionstore.SessionTreeNode
	leafID              string
	filterMode          string
	query               string
	folded              map[string]bool
	showLabelTimestamps bool
	view                treeView
	selected            int
	maxVisible          int
	labelInput          *tui.Input
	labelEntryID        string
	onSelect            func(string)
	onCancel            func()
	onLabelChange       func(string, *string)

	// Where the last render put the tree rows and how far it scrolled them
	// sideways, so clicks resolve to the row and column the user saw. Guarded
	// because renders can overlap; the rest of the component is single-writer.
	rowsMu                                 sync.Mutex
	treeTop, rowStart, rowCount, rowScroll int
	foldedByClick                          bool

	OnCopy func(string)
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
		roots: roots, leafID: leafID, filterMode: filterMode, folded: make(map[string]bool),
		maxVisible: max(5, terminalHeight/2), onSelect: onSelect, onCancel: onCancel,
		onLabelChange: onLabelChange,
	}
	if initialSelectedID == "" {
		initialSelectedID = leafID
	}
	component.refresh(initialSelectedID)
	return component
}

func (component *TreeSelectorComponent) refresh(preferredID string) {
	component.view = buildTreeView(
		component.roots, component.leafID, component.filterMode, component.query,
		component.folded, component.showLabelTimestamps,
	)
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
		node := component.view.byID[id]
		if node == nil || node.Entry.ParentID == nil {
			break
		}
		id = *node.Entry.ParentID
	}
	component.selected = max(0, len(component.view.rows)-1)
}

func (component *TreeSelectorComponent) selectedID() string {
	if component.selected < 0 || component.selected >= len(component.view.rows) {
		return ""
	}
	return component.view.rows[component.selected].node.Entry.ID
}

func (component *TreeSelectorComponent) HandleInput(event tui.KeyEvent) {
	bindings := tui.GetKeybindings()
	if component.labelInput != nil {
		switch {
		case bindings.Matches(event.Raw, "tui.select.confirm"):
			component.saveLabel()
		case bindings.Matches(event.Raw, "tui.select.cancel"):
			component.labelInput = nil
		default:
			component.labelInput.HandleInput(event)
		}
		return
	}

	selectedID := component.selectedID()
	switch {
	case bindings.Matches(event.Raw, "tui.select.up"):
		component.move(-1)
	case bindings.Matches(event.Raw, "tui.select.down"):
		component.move(1)
	case bindings.Matches(event.Raw, "app.tree.foldOrUp"):
		if selectedID != "" && component.isFoldable(selectedID) && !component.folded[selectedID] {
			component.folded[selectedID] = true
			component.refresh(selectedID)
		} else {
			component.selectBranch("up")
		}
	case bindings.Matches(event.Raw, "app.tree.unfoldOrDown"):
		if component.folded[selectedID] {
			delete(component.folded, selectedID)
			component.refresh(selectedID)
		} else {
			component.selectBranch("down")
		}
	case bindings.Matches(event.Raw, "tui.editor.cursorLeft") ||
		bindings.Matches(event.Raw, "tui.select.pageUp"):
		component.selected = max(0, component.selected-component.maxVisible)
	case bindings.Matches(event.Raw, "tui.editor.cursorRight") ||
		bindings.Matches(event.Raw, "tui.select.pageDown"):
		component.selected = min(len(component.view.rows)-1, component.selected+component.maxVisible)
	case bindings.Matches(event.Raw, "tui.select.confirm"):
		if selectedID != "" && component.onSelect != nil {
			component.onSelect(selectedID)
		}
	case bindings.Matches(event.Raw, "app.message.copy"):
		if component.OnCopy != nil {
			component.OnCopy(treeCopyText(component.selectedNode()))
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		if component.query != "" {
			component.query = ""
			clear(component.folded)
			component.refresh(selectedID)
		} else if component.onCancel != nil {
			component.onCancel()
		}
	case bindings.Matches(event.Raw, "app.tree.filter.default"):
		component.setFilter("default", false)
	case bindings.Matches(event.Raw, "app.tree.filter.noTools"):
		component.setFilter("no-tools", true)
	case bindings.Matches(event.Raw, "app.tree.filter.userOnly"):
		component.setFilter("user-only", true)
	case bindings.Matches(event.Raw, "app.tree.filter.labeledOnly"):
		component.setFilter("labeled-only", true)
	case bindings.Matches(event.Raw, "app.tree.filter.all"):
		component.setFilter("all", true)
	case bindings.Matches(event.Raw, "app.tree.filter.cycleBackward"):
		component.cycleFilter(-1)
	case bindings.Matches(event.Raw, "app.tree.filter.cycleForward"):
		component.cycleFilter(1)
	case bindings.Matches(event.Raw, "tui.editor.deleteCharBackward"):
		runes := []rune(component.query)
		if len(runes) > 0 {
			component.query = string(runes[:len(runes)-1])
			clear(component.folded)
			component.refresh(selectedID)
		}
	case bindings.Matches(event.Raw, "app.tree.editLabel"):
		component.editLabel()
	case bindings.Matches(event.Raw, "app.tree.toggleLabelTimestamp"):
		component.showLabelTimestamps = !component.showLabelTimestamps
		component.refresh(selectedID)
	default:
		component.appendSearch(event.Raw)
	}
}

// treeFoldSpan is the visible column range the ⊞/⊟ fold marker occupies inside
// a row's prefix, or an empty span when the row is not foldable.
func treeFoldSpan(row treeRow) (int, int) {
	prefix := tui.SliceByColumn(row.label, 0, row.anchor, false)
	index := strings.IndexAny(prefix, "⊞⊟")
	if index < 0 {
		return 0, 0
	}
	start := tui.VisibleWidth(prefix[:index])
	return start, start + tui.VisibleWidth(prefix[index:index+len("⊞")])
}

// WantsMouseMotion turns on hover reports while the selector holds focus.
func (component *TreeSelectorComponent) WantsMouseMotion() bool { return true }

// HandleMouse selects the clicked row, toggles the fold marker when the click
// lands on it, confirms on a double click, and scrolls on the wheel. The two
// leading cells are the selection cursor, and rowScroll undoes the horizontal
// viewport so clicks stay on the glyph the user aimed at.
func (component *TreeSelectorComponent) HandleMouse(event tui.MouseEvent) bool {
	if component.labelInput != nil || len(component.view.rows) == 0 {
		return false
	}
	switch {
	case event.Type == tui.MouseMove:
		// Hover moves the highlight only while the tree cannot scroll: a
		// recentring viewport would shift rows under the cursor and feed back.
		top, first, count, _ := component.rowLayout()
		if len(component.view.rows) > component.maxVisible ||
			event.Row < top || event.Row >= top+count {
			return false
		}
		if index := first + event.Row - top; index < len(component.view.rows) {
			component.selected = index
		}
		return true
	case event.Type == tui.MouseWheelUp || event.Type == tui.MouseWheelDown:
		delta := -3
		if event.Type == tui.MouseWheelDown {
			delta = 3
		}
		component.selected = max(0, min(component.selected+delta, len(component.view.rows)-1))
		return true
	case event.Type == tui.MousePress && event.Button == 0:
		top, first, count, scroll := component.rowLayout()
		if event.Row < top || event.Row >= top+count {
			return false
		}
		// The first press of a double click already acted on this cell:
		// re-resolving would open whatever the recentred viewport moved under
		// it, and double-clicking a fold marker must not navigate the session.
		if event.Clicks >= 2 {
			if id := component.selectedID(); id != "" && !component.foldedByClick && component.onSelect != nil {
				component.onSelect(id)
			}
			return true
		}
		index := first + event.Row - top
		if index >= len(component.view.rows) {
			return false
		}
		row := component.view.rows[index]
		component.selected = index
		id := row.node.Entry.ID
		start, end := treeFoldSpan(row)
		column := event.Column - 2 + scroll
		component.foldedByClick = end > start && column >= start && column < end
		if component.foldedByClick {
			if component.folded[id] {
				delete(component.folded, id)
			} else {
				component.folded[id] = true
			}
			component.refresh(id)
		}
		return true
	}
	return false
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
	clear(component.folded)
	component.refresh(selectedID)
}

func (component *TreeSelectorComponent) cycleFilter(delta int) {
	modes := []string{"default", "no-tools", "user-only", "labeled-only", "all"}
	index := 0
	for candidate, mode := range modes {
		if mode == component.filterMode {
			index = candidate
			break
		}
	}
	component.setFilter(modes[(index+delta+len(modes))%len(modes)], false)
}

func (component *TreeSelectorComponent) appendSearch(data string) {
	if printable := tui.DecodeKittyPrintable(data); printable != "" {
		data = printable
	}
	if data == "" {
		return
	}
	for _, value := range data {
		if value < 32 || value == 0x7f || value >= 0x80 && value <= 0x9f {
			return
		}
	}
	selectedID := component.selectedID()
	component.query += data
	clear(component.folded)
	component.refresh(selectedID)
}

func (component *TreeSelectorComponent) selectedNode() *sessionstore.SessionTreeNode {
	if component.selected < 0 || component.selected >= len(component.view.rows) {
		return nil
	}
	return component.view.rows[component.selected].node
}

func (component *TreeSelectorComponent) isFoldable(id string) bool {
	children := component.view.children[id]
	if len(children) == 0 {
		return false
	}
	parent := component.view.parent[id]
	return parent == "" || len(component.view.children[parent]) > 1
}

func (component *TreeSelectorComponent) selectBranch(direction string) {
	selectedID := component.selectedID()
	if selectedID == "" {
		return
	}
	indexByID := make(map[string]int, len(component.view.rows))
	for index, row := range component.view.rows {
		indexByID[row.node.Entry.ID] = index
	}
	currentID := selectedID
	if direction == "down" {
		for {
			children := component.view.children[currentID]
			if len(children) == 0 {
				component.selected = indexByID[currentID]
				return
			}
			if len(children) > 1 {
				component.selected = indexByID[children[0]]
				return
			}
			currentID = children[0]
		}
	}
	for {
		parentID := component.view.parent[currentID]
		if parentID == "" {
			component.selected = indexByID[currentID]
			return
		}
		if len(component.view.children[parentID]) > 1 && indexByID[currentID] < component.selected {
			component.selected = indexByID[currentID]
			return
		}
		currentID = parentID
	}
}

func (component *TreeSelectorComponent) editLabel() {
	node := component.selectedNode()
	if node == nil || component.onLabelChange == nil {
		return
	}
	component.labelEntryID = node.Entry.ID
	component.labelInput = tui.NewInput()
	if node.Label != nil {
		component.labelInput.SetValue(*node.Label)
		component.labelInput.HandleInput(tui.KeyEvent{Raw: "\x05"})
	}
	component.labelInput.SetFocused(true)
}

func (component *TreeSelectorComponent) saveLabel() {
	value := strings.TrimSpace(component.labelInput.GetValue())
	var label *string
	if value != "" {
		label = &value
	}
	node := component.view.byID[component.labelEntryID]
	if node != nil {
		node.Label = label
		if label == nil {
			node.LabelTimestamp = nil
		} else {
			now := time.Now().Format(time.RFC3339Nano)
			node.LabelTimestamp = &now
		}
	}
	if component.onLabelChange != nil {
		component.onLabelChange(component.labelEntryID, label)
	}
	component.labelInput = nil
	component.refresh(component.labelEntryID)
}

func (component *TreeSelectorComponent) SetFocused(focused bool) {
	if component.labelInput != nil {
		component.labelInput.SetFocused(focused)
	}
}

func (component *TreeSelectorComponent) Invalidate() {}

func (component *TreeSelectorComponent) Render(width int) []string {
	border := extensionDialogBorder().Render(width)[0]
	lines := []string{"", border}
	lines = append(lines, tui.TruncateToWidth(theme.Bold("  Session Tree"), width, "", false))
	help := strings.Join([]string{
		RawKeyHint("↑/↓", "move"), RawKeyHint("←/→", "page"),
		KeyHint("app.tree.foldOrUp", "branch"), KeyHint("app.message.copy", "copy"),
		KeyHint("app.tree.editLabel", "label"), KeyHint("app.tree.toggleLabelTimestamp", "label time"),
		theme.FG("muted", "filters ctrl+d/t/u/l/a"), theme.FG("muted", "cycle ctrl+o/shift+ctrl+o"),
	}, " · ")
	for _, line := range tui.WrapTextWithANSI("  "+help, max(1, width)) {
		lines = append(lines, theme.FG("muted", line))
	}
	search := "  " + theme.FG("muted", "Type to search:")
	if component.query != "" {
		search += " " + theme.FG("accent", component.query)
	}
	lines = append(lines, tui.TruncateToWidth(search, width, "", false))
	lines = append(lines, border)
	lines = append(lines, "")
	if component.labelInput != nil {
		lines = append(lines, tui.TruncateToWidth(theme.FG("muted", "  Label (empty to remove):"), width, "", false))
		for _, line := range component.labelInput.Render(max(1, width-2)) {
			lines = append(lines, tui.TruncateToWidth("  "+line, width, "", false))
		}
		lines = append(lines, tui.TruncateToWidth("  "+KeyHint("tui.select.confirm", "save")+"  "+KeyHint("tui.select.cancel", "cancel"), width, "", false))
	} else {
		lines = append(lines, component.renderTree(width, len(lines))...)
	}
	lines = append(lines, "", border)
	return lines
}

func (component *TreeSelectorComponent) setRowLayout(top, start, count, scroll int) {
	component.rowsMu.Lock()
	component.treeTop, component.rowStart, component.rowCount, component.rowScroll = top, start, count, scroll
	component.rowsMu.Unlock()
}

func (component *TreeSelectorComponent) rowLayout() (int, int, int, int) {
	component.rowsMu.Lock()
	defer component.rowsMu.Unlock()
	return component.treeTop, component.rowStart, component.rowCount, component.rowScroll
}

func (component *TreeSelectorComponent) renderTree(width, top int) []string {
	if len(component.view.rows) == 0 {
		component.setRowLayout(top, 0, 0, 0)
		return []string{
			tui.TruncateToWidth(theme.FG("muted", "  No entries found"), width, "", false),
			tui.TruncateToWidth(theme.FG("muted", "  (0/0)"+component.statusLabels()), width, "", false),
		}
	}
	start := max(0, min(component.selected-component.maxVisible/2, len(component.view.rows)-component.maxVisible))
	end := min(start+component.maxVisible, len(component.view.rows))
	bodyWidth := max(0, width-2)
	scroll, maxBodyWidth := 0, 0
	for _, row := range component.view.rows[start:end] {
		maxBodyWidth = max(maxBodyWidth, tui.VisibleWidth(row.label))
	}
	selected := component.view.rows[component.selected]
	if maxBodyWidth > bodyWidth {
		minContent := min(20, max(4, bodyWidth/3))
		if selected.anchor > bodyWidth-minContent {
			scroll = min(maxBodyWidth-bodyWidth, selected.anchor-max(2, min(12, bodyWidth/4)))
		}
	}
	component.setRowLayout(top, start, end-start, scroll)
	lines := make([]string, 0, end-start+1)
	for index := start; index < end; index++ {
		body := component.view.rows[index].label
		if scroll > 0 {
			body = tui.SliceByColumn(body, scroll, bodyWidth, true)
		}
		cursor := "  "
		if index == component.selected {
			cursor = theme.FG("accent", "› ")
			body = theme.BG("selectedBg", body)
		}
		lines = append(lines, tui.TruncateToWidth(cursor+body, width, "", false))
	}
	status := fmt.Sprintf("  (%d/%d)%s", component.selected+1, len(component.view.rows), component.statusLabels())
	return append(lines, tui.TruncateToWidth(theme.FG("muted", status), width, "", false))
}

func (component *TreeSelectorComponent) statusLabels() string {
	status := ""
	switch component.filterMode {
	case "no-tools":
		status = " [no-tools]"
	case "user-only":
		status = " [user]"
	case "labeled-only":
		status = " [labeled]"
	case "all":
		status = " [all]"
	}
	if component.showLabelTimestamps {
		status += " [+label time]"
	}
	return status
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

func formatTreeLabelTimestamp(value string, now time.Time) string {
	timestamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return ""
	}
	timestamp = timestamp.Local()
	now = now.Local()
	switch {
	case timestamp.Year() == now.Year() && timestamp.YearDay() == now.YearDay():
		return timestamp.Format("15:04")
	case timestamp.Year() == now.Year():
		return timestamp.Format("1/2 15:04")
	default:
		return timestamp.Format("06/1/2 15:04")
	}
}
