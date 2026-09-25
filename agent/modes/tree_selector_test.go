package modes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/tui"
)

func TestTreeViewRows(t *testing.T) {
	linear := func() ([]*sessionstore.SessionTreeNode, string) {
		root := treeTestMessage("root", "", "user", "one")
		treeTestChain(root,
			treeTestMessage("reply", "root", "assistant", "two"),
			treeTestMessage("next", "reply", "user", "three"),
			treeTestMessage("leaf", "next", "assistant", "four"),
		)
		return []*sessionstore.SessionTreeNode{root}, "leaf"
	}
	hiddenFork := func() ([]*sessionstore.SessionTreeNode, string) {
		root := treeTestMessage("root", "", "user", "root")
		hidden := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
			Type: "model_change", ID: "hidden", ParentID: treeTestParent("root"), ModelID: "m",
		}}
		root.Children = []*sessionstore.SessionTreeNode{hidden}
		hidden.Children = []*sessionstore.SessionTreeNode{
			treeTestMessage("left", "hidden", "user", "left"),
			treeTestMessage("right", "hidden", "user", "right"),
		}
		return []*sessionstore.SessionTreeNode{root}, "right"
	}
	for _, test := range []struct {
		name, filter, query string
		tree                func() ([]*sessionstore.SessionTreeNode, string)
		want                []string
	}{
		{"linear history is a flat list of prompts", "default", "", linear, []string{"one", "three", "● ↳ four"}},
		{"rails only where history forks, active branch first", "default", "", treeTestForked, []string{
			"root", "├ summary · tried old", "│ active", "│ ● ↳ done", "└ old", "  ↳ old reply",
		}},
		{"branches reattach across hidden entries", "default", "", hiddenFork, []string{"root", "├ ● right", "└ left"}},
		{"messages view lists every reply", "no-tools", "", linear, []string{"one", "↳ two", "three", "● ↳ four"}},
		{"prompts view marks the nearest shown position", "user-only", "", linear, []string{"one", "● three"}},
		{"all view shows bookkeeping", "all", "", hiddenFork, []string{"root", "model · m", "├ ● right", "└ left"}},
		{"a search never moves the position", "default", "old", treeTestForked, []string{"├ summary · tried old", "└ old", "  ↳ old reply"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			roots, leaf := test.tree()
			view := buildTreeView(newTreeIndex(roots), roots, leaf, test.filter, test.query)
			if got := plainTreeRows(view.rows); !slices.Equal(got, test.want) {
				t.Fatalf("rows =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(test.want, "\n"))
			}
		})
	}
}

func TestTreeSelectorTitleIsHonest(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "only prompt")
	selector := NewTreeSelectorComponent([]*sessionstore.SessionTreeNode{root}, "root", 24, nil, nil, nil, "", "default")
	if title := stripTreeANSI(selector.Render(60)[0]); !strings.Contains(title, "Tree · 1 turn ") {
		t.Fatalf("single-turn title = %q", title)
	}
	treeTestChain(root, treeTestMessage("a", "root", "assistant", "a"), treeTestMessage("b", "a", "user", "b"))
	selector = NewTreeSelectorComponent([]*sessionstore.SessionTreeNode{root}, "b", 24, nil, nil, nil, "", "labeled-only")
	lines := stripTreeLines(selector.Render(60))
	if !strings.Contains(lines[0], "Tree · labeled · 2 turns · no forks") || lines[1] != "  No labels yet" {
		t.Fatalf("linear labeled view = %#v", lines)
	}
}

func TestTreeSelectorStartsAtLeafAndCentersIt(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	root := treeTestMessage("root", "", "user", "root")
	parent := root
	for index := range 8 {
		child := treeTestMessage(fmt.Sprint(index), parent.Entry.ID, "user", fmt.Sprint("prompt ", index))
		parent.Children = []*sessionstore.SessionTreeNode{child}
		parent = child
	}

	selector := NewTreeSelectorComponent([]*sessionstore.SessionTreeNode{root}, parent.Entry.ID, 10, nil, nil, nil, "", "default")
	if got := selector.selectedID(); got != parent.Entry.ID {
		t.Fatalf("selected = %q, want current leaf %q", got, parent.Entry.ID)
	}
	lines := stripTreeLines(selector.Render(80))
	if index := slices.IndexFunc(lines, func(line string) bool { return strings.HasPrefix(line, "› ● prompt 7") }); index < 0 {
		t.Fatalf("current leaf is not selected in the window:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(lines[0], " 9/9 ") {
		t.Fatalf("scrolling title lacks the position: %q", lines[0])
	}
}

func TestTreeSelectorNavigationForkAndJump(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	var selected string
	type fork struct {
		id     string
		before bool
	}
	var forks []fork
	selector := NewTreeSelectorComponent(roots, leaf, 24, func(id string) { selected = id }, nil, nil, "", "default")
	selector.OnFork = func(id string, before bool) { forks = append(forks, fork{id, before}) }

	press := func(keys ...string) {
		for _, key := range keys {
			selector.HandleInput(tui.KeyEvent{Raw: key})
		}
	}
	press("g")
	if got := selector.selectedID(); got != "root" {
		t.Fatalf("g selected %q, want root", got)
	}
	press("\x1b[C")
	if got := selector.selectedID(); got != "summary" {
		t.Fatalf("right jumped to %q, want the first branch", got)
	}
	press("\x1b[C")
	if got := selector.selectedID(); got != "old" {
		t.Fatalf("right jumped to %q, want the second branch", got)
	}
	press("\x1b[D", "j", "j")
	if got := selector.selectedID(); got != "done" {
		t.Fatalf("left then j j selected %q, want done", got)
	}
	press("f", "k", "f")
	if want := []fork{{"done", false}, {"active", true}}; !slices.Equal(forks, want) {
		t.Fatalf("forks = %#v, want %#v", forks, want)
	}
	press("\r")
	if selected != "active" {
		t.Fatalf("enter selected %q, want active", selected)
	}
}

func TestTreeSelectorFilterAndViews(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	cancelled := 0
	var selected string
	selector := NewTreeSelectorComponent(roots, leaf, 24, func(id string) { selected = id }, func() { cancelled++ }, nil, "", "default")

	height := len(selector.Render(80))
	for _, key := range "/old" {
		selector.HandleInput(tui.KeyEvent{Raw: string(key)})
	}
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"summary", "old", "oldReply"}) {
		t.Fatalf("filter visible IDs = %#v", got)
	}
	lines := stripTreeLines(selector.Render(80))
	if footer := lines[len(lines)-1]; !strings.HasPrefix(footer, "  /old") || !strings.Contains(footer, "3 matches") {
		t.Fatalf("filter footer = %q", footer)
	}
	if len(lines) != height {
		t.Fatalf("filtering resized the tree from %d to %d lines", height, len(lines))
	}
	// Command letters type into the query while filtering.
	selector.HandleInput(tui.KeyEvent{Raw: "f"})
	if lines := stripTreeLines(selector.Render(80)); selector.query() != "oldf" || lines[1] != "  Nothing matches “oldf”" {
		t.Fatalf("query %q rendered %#v", selector.query(), lines)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if got := visibleTreeIDs(selector); len(got) != 6 || cancelled != 0 {
		t.Fatalf("escape left filtering with %#v, cancelled = %d", got, cancelled)
	}
	for _, key := range []string{"/", "x", "\x7f", "\x7f"} {
		selector.HandleInput(tui.KeyEvent{Raw: key})
	}
	if selector.filterInput != nil {
		t.Fatal("backspace on an empty query kept the filter open")
	}
	for _, key := range []string{"/", "a", "c", "t", "\r"} {
		selector.HandleInput(tui.KeyEvent{Raw: key})
	}
	if selected != "active" {
		t.Fatalf("enter while filtering selected %q, want active", selected)
	}

	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	selector.HandleInput(tui.KeyEvent{Raw: "\t"})
	if selector.filterMode != "no-tools" || !slices.Contains(visibleTreeIDs(selector), "answer") {
		t.Fatalf("tab switched to %q showing %#v", selector.filterMode, visibleTreeIDs(selector))
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x15"})
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"root", "active", "old"}) {
		t.Fatalf("user-only visible IDs = %#v", got)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if cancelled != 1 {
		t.Fatalf("escape outside the filter cancelled = %d, want 1", cancelled)
	}
}

func TestTreeSelectorCopyAndLabel(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	var copied, labeled string
	selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil,
		func(_ string, label *string) {
			if label != nil {
				labeled = *label
			}
		},
		"active", "default",
	)
	selector.now = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.Local) }
	selector.OnCopy = func(text string) { copied = text }
	selector.HandleInput(tui.KeyEvent{Raw: "\x18"})
	if copied != "active" {
		t.Fatalf("copied = %q", copied)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "L"})
	for _, key := range "kept" {
		selector.HandleInput(tui.KeyEvent{Raw: string(key)})
	}
	if footer := stripTreeANSI(selector.Render(80)[len(selector.Render(80))-1]); !strings.Contains(footer, "label: kept") {
		t.Fatalf("label footer = %q", footer)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\r"})
	if labeled != "kept" {
		t.Fatalf("label = %q", labeled)
	}
	row := stripTreeLines(selector.Render(80))[3]
	if !strings.HasPrefix(row, "› │ active") || !strings.HasSuffix(row, "#kept") {
		t.Fatalf("labeled row = %q", row)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x0c"})
	if got := visibleTreeIDs(selector); !slices.Equal(got, []string{"active"}) {
		t.Fatalf("labeled-only visible IDs = %#v", got)
	}
}

// The renderer panics on any line wider than the terminal, so every line has
// to fit, whatever the mode.
func TestTreeSelectorFitsNarrowWidths(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	for _, width := range []int{1, 8, 16, 26, 34, 60, 120} {
		for _, keys := range [][]string{nil, {"L"}, {"/", "o"}} {
			roots, leaf := treeTestForked()
			selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil, func(string, *string) {}, "", "default")
			for _, key := range keys {
				selector.HandleInput(tui.KeyEvent{Raw: key})
			}
			for _, line := range selector.Render(width) {
				if got := tui.VisibleWidth(line); got > width {
					t.Fatalf("width %d keys %q: line of width %d: %q", width, keys, got, line)
				}
			}
		}
	}
}

// Renders run on the TUI's timer while keys arrive: a filter keystroke must
// never leave a render indexing rows that are gone.
func TestTreeSelectorRendersWhileFiltering(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil, nil, "", "default")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 300 {
			selector.Render(80)
		}
	}()
	for range 60 {
		for _, key := range []string{"/", "o", "l", "d", "\x1b", "\t"} {
			selector.HandleInput(tui.KeyEvent{Raw: key})
		}
	}
	<-done
}

// Long sessions build the index once and render only the window.
func TestTreeSelectorLongHistoryStaysWindowed(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	root := treeTestMessage("u0", "", "user", "prompt 0")
	parent := root
	for index := 1; index < 20000; index++ {
		role := "assistant"
		if index%2 == 0 {
			role = "user"
		}
		child := treeTestMessage(fmt.Sprint(index), parent.Entry.ID, role, fmt.Sprint(role, " ", index))
		parent.Children = []*sessionstore.SessionTreeNode{child}
		parent = child
	}
	selector := NewTreeSelectorComponent([]*sessionstore.SessionTreeNode{root}, parent.Entry.ID, 40, nil, nil, nil, "", "default")
	if rows := len(selector.view.rows); rows != 10001 {
		t.Fatalf("rows = %d, want 10000 prompts plus the current reply", rows)
	}
	lines := selector.Render(80)
	if len(lines) != selector.maxVisible+2 {
		t.Fatalf("rendered %d lines, want window %d plus title and footer", len(lines), selector.maxVisible)
	}
	if !strings.Contains(stripTreeANSI(lines[len(lines)-2]), "› ● ↳ assistant 19999") {
		t.Fatalf("current reply is not the selected last row: %q", stripTreeANSI(lines[len(lines)-2]))
	}
}

func plainTreeRows(rows []treeRow) []string {
	result := make([]string, len(rows))
	for index, row := range rows {
		text := row.rail
		if row.current {
			text += "● "
		}
		if row.kind == treeRowReply {
			text += "↳ "
		}
		result[index] = text + row.text
	}
	return result
}

func stripTreeANSI(line string) string {
	return strings.TrimRight(selectorANSI.ReplaceAllString(line, ""), " ")
}

func stripTreeLines(lines []string) []string {
	result := make([]string, len(lines))
	for index, line := range lines {
		result[index] = stripTreeANSI(line)
	}
	return result
}

// treeTestForked is root → answer, forking into an abandoned "old" branch and
// the active branch that opens with a summary of it.
func treeTestForked() ([]*sessionstore.SessionTreeNode, string) {
	root := treeTestMessage("root", "", "user", "root")
	answer := treeTestMessage("answer", "root", "assistant", "answer")
	summary := &sessionstore.SessionTreeNode{Entry: sessionstore.SessionEntry{
		Type: "branch_summary", ID: "summary", ParentID: treeTestParent("answer"), Summary: "tried old",
	}}
	old := treeTestMessage("old", "answer", "user", "old")
	treeTestChain(root, answer)
	answer.Children = []*sessionstore.SessionTreeNode{old, summary}
	treeTestChain(old, treeTestMessage("oldReply", "old", "assistant", "old reply"))
	treeTestChain(summary, treeTestMessage("active", "summary", "user", "active"), treeTestMessage("done", "active", "assistant", "done"))
	return []*sessionstore.SessionTreeNode{root}, "done"
}

// treeTestChain links nodes into a single line below parent.
func treeTestChain(parent *sessionstore.SessionTreeNode, nodes ...*sessionstore.SessionTreeNode) {
	for _, node := range nodes {
		parent.Children = []*sessionstore.SessionTreeNode{node}
		parent = node
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
