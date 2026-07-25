package modes

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
)

func TestTreeSelectItemsKeepLinearConversationFlat(t *testing.T) {
	root := treeTestMessage("root", "", "user", "one")
	reply := treeTestMessage("reply", "root", "assistant", "two")
	leaf := treeTestMessage("leaf", "reply", "user", "three")
	root.Children = []*sessionstore.SessionTreeNode{reply}
	reply.Children = []*sessionstore.SessionTreeNode{leaf}

	view := buildTreeView([]*sessionstore.SessionTreeNode{root}, "leaf", "default", "", nil, false)
	assertTreeRows(t, view.rows, []string{
		"• user: one",
		"• assistant: two",
		"• user: three",
	})
}

func TestTreeSelectItemsIndentOnlyAtBranches(t *testing.T) {
	root := treeTestMessage("root", "", "user", "root")
	old := treeTestMessage("old", "root", "user", "old")
	active := treeTestMessage("active", "root", "assistant", "active")
	leaf := treeTestMessage("leaf", "active", "user", "leaf")
	root.Children = []*sessionstore.SessionTreeNode{old, active}
	active.Children = []*sessionstore.SessionTreeNode{leaf}

	view := buildTreeView([]*sessionstore.SessionTreeNode{root}, "leaf", "default", "", nil, false)
	assertTreeRows(t, view.rows, []string{
		"• user: root",
		"├⊟ • assistant: active",
		"│     • user: leaf",
		"└─ user: old",
	})
}

func TestTreeSelectItemsReattachAcrossHiddenEntries(t *testing.T) {
	root := treeTestMessage("root", "", "user", "root")
	hidden := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
		Type: "model_change", ID: "hidden", ParentID: treeTestParent("root"),
	}}
	left := treeTestMessage("left", "hidden", "user", "left")
	right := treeTestMessage("right", "hidden", "user", "right")
	root.Children = []*sessionstore.SessionTreeNode{hidden}
	hidden.Children = []*sessionstore.SessionTreeNode{left, right}

	view := buildTreeView([]*sessionstore.SessionTreeNode{root}, "right", "default", "", nil, false)
	assertTreeRows(t, view.rows, []string{
		"• user: root",
		"├─ • user: right",
		"└─ user: left",
	})
}

func TestTreeSelectorStartsAtLeafAndCentersIt(t *testing.T) {
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "root")
	parent := root
	for index := range 8 {
		child := treeTestMessage(string(rune('a'+index)), parent.Entry.ID, "user", string(rune('a'+index)))
		parent.Children = []*sessionstore.SessionTreeNode{child}
		parent = child
	}

	selector := NewTreeSelectorComponent(
		[]*sessionstore.SessionTreeNode{root}, parent.Entry.ID, 10, nil, nil, nil, "", "default",
	)
	if got := selector.selectedID(); got != parent.Entry.ID {
		t.Fatalf("selected = %q, want current leaf %q", got, parent.Entry.ID)
	}
	rendered := selector.Render(80)
	if !slices.ContainsFunc(rendered, func(line string) bool { return strings.Contains(line, "user: h") }) {
		t.Fatalf("current leaf is not in the centered window: %#v", rendered)
	}
}

func TestTreeSelectorSearchEscapeAndFilters(t *testing.T) {
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "alpha")
	assistant := treeTestMessage("assistant", "root", "assistant", "beta result")
	user := treeTestMessage("user", "assistant", "user", "gamma")
	root.Children = []*sessionstore.SessionTreeNode{assistant}
	assistant.Children = []*sessionstore.SessionTreeNode{user}

	cancelled := 0
	selector := NewTreeSelectorComponent(
		[]*sessionstore.SessionTreeNode{root}, "user", 24, nil,
		func() { cancelled++ }, nil, "", "default",
	)
	for _, key := range "beta" {
		selector.HandleInput(tui.KeyEvent{Raw: string(key)})
	}
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"assistant"}) {
		t.Fatalf("search visible IDs = %#v", got)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"root", "assistant", "user"}) || cancelled != 0 {
		t.Fatalf("first escape visible IDs = %#v, cancelled = %d", got, cancelled)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x15"})
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"root", "user"}) {
		t.Fatalf("user-only visible IDs = %#v", got)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if cancelled != 1 {
		t.Fatalf("second escape cancelled = %d, want 1", cancelled)
	}
}

func TestTreeSelectorFoldCopyAndLabel(t *testing.T) {
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "root")
	branch := treeTestMessage("branch", "root", "assistant", "copy all of this")
	leaf := treeTestMessage("leaf", "branch", "user", "leaf")
	other := treeTestMessage("other", "root", "user", "other")
	root.Children = []*sessionstore.SessionTreeNode{branch, other}
	branch.Children = []*sessionstore.SessionTreeNode{leaf}

	var copied, labeled string
	selector := NewTreeSelectorComponent(
		[]*sessionstore.SessionTreeNode{root}, "leaf", 24, nil, nil,
		func(_ string, label *string) {
			if label != nil {
				labeled = *label
			}
		},
		"branch", "default",
	)
	selector.OnCopy = func(text string) { copied = text }
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[1;5D"})
	if slices.Contains(visibleTreeIDs(selector), "leaf") {
		t.Fatalf("folded branch still shows its leaf: %#v", visibleTreeIDs(selector))
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[1;5C"})
	if !slices.Contains(visibleTreeIDs(selector), "leaf") {
		t.Fatalf("unfolded branch hides its leaf: %#v", visibleTreeIDs(selector))
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x18"})
	if copied != "copy all of this" {
		t.Fatalf("copied = %q", copied)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "L"})
	for _, key := range "kept" {
		selector.HandleInput(tui.KeyEvent{Raw: string(key)})
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\r"})
	if labeled != "kept" {
		t.Fatalf("label = %q", labeled)
	}
}

// Folding hides whole subtrees, not just direct children: the skip set is built
// in one pre-order pass, so a descendant is only hidden via its parent's entry.
func TestTreeSelectorFoldHidesDeepDescendants(t *testing.T) {
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "root")
	branch := treeTestMessage("branch", "root", "assistant", "branch")
	child := treeTestMessage("child", "branch", "user", "child")
	grandchild := treeTestMessage("grandchild", "child", "assistant", "grandchild")
	other := treeTestMessage("other", "root", "user", "other")
	root.Children = []*sessionstore.SessionTreeNode{branch, other}
	branch.Children = []*sessionstore.SessionTreeNode{child}
	child.Children = []*sessionstore.SessionTreeNode{grandchild}

	selector := NewTreeSelectorComponent(
		[]*sessionstore.SessionTreeNode{root}, "grandchild", 40, nil, nil, nil, "branch", "default",
	)
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[1;5D"})
	for _, hidden := range []string{"child", "grandchild"} {
		if slices.Contains(visibleTreeIDs(selector), hidden) {
			t.Fatalf("folded branch still shows %q: %#v", hidden, visibleTreeIDs(selector))
		}
	}
	if !slices.Contains(visibleTreeIDs(selector), "other") {
		t.Fatalf("folding hid an unrelated branch: %#v", visibleTreeIDs(selector))
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[1;5C"})
	if !slices.Contains(visibleTreeIDs(selector), "grandchild") {
		t.Fatalf("unfolded branch hides its grandchild: %#v", visibleTreeIDs(selector))
	}
}

// The renderer panics on any line wider than the terminal, so every line the
// label editor emits has to be truncated, not just the input row.
func TestTreeSelectorLabelEditorFitsNarrowWidth(t *testing.T) {
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "root")
	leaf := treeTestMessage("leaf", "root", "assistant", "leaf")
	root.Children = []*sessionstore.SessionTreeNode{leaf}

	for _, width := range []int{8, 16, 26, 34} {
		selector := NewTreeSelectorComponent(
			[]*sessionstore.SessionTreeNode{root}, "leaf", width, nil, nil, nil, "leaf", "default",
		)
		selector.HandleInput(tui.KeyEvent{Raw: "L"})
		for _, line := range selector.Render(width) {
			if got := tui.VisibleWidth(line); got > width {
				t.Fatalf("width %d: rendered line of width %d: %q", width, got, line)
			}
		}
	}
}

func assertTreeRows(t *testing.T, rows []treeRow, labels []string) {
	t.Helper()
	if len(rows) != len(labels) {
		t.Fatalf("got %d rows, want %d", len(rows), len(labels))
	}
	for index, label := range labels {
		if rows[index].label != label {
			t.Errorf("row %d label = %q, want %q", index, rows[index].label, label)
		}
		if rows[index].node.Entry.ID == "" {
			t.Errorf("row %d has no stable entry ID", index)
		}
	}
}

func visibleTreeIDs(selector *TreeSelectorComponent) []string {
	ids := make([]string, len(selector.view.rows))
	for index, row := range selector.view.rows {
		ids[index] = row.node.Entry.ID
	}
	return ids
}

func treeTestMessage(id, parent, role, text string) *sessionstore.SessionTreeNode {
	message, err := json.Marshal(map[string]any{"role": role, "content": text})
	if err != nil {
		panic(err)
	}
	return &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
		Type: "message", ID: id, ParentID: treeTestParent(parent), Message: message,
	}}
}

func treeTestParent(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func useTreeTestKeybindings(t *testing.T) {
	t.Helper()
	previous := tui.GetKeybindings()
	tui.SetKeybindings(NewAppKeybindings(nil))
	t.Cleanup(func() { tui.SetKeybindings(previous) })
}
