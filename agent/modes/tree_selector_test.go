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

func TestTreeShowsOneHistoryWithVersions(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil, nil, "", "default")
	// The summary opens the active branch, so it is where the history forks.
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 root", "summary  tried old 2/2", "● 2 active"}) {
		t.Fatalf("rows = %#v", got)
	}
	if selector.selectedID() != "active" {
		t.Fatalf("selected %q, want the current turn", selector.selectedID())
	}
	press(selector, "k", "\x1b[D")
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 root", "2 old 1/2"}) || selector.selectedID() != "old" {
		t.Fatalf("left showed %#v selecting %q", got, selector.selectedID())
	}
	if row := selector.rows[1]; row.inContext || row.reply != "old reply" {
		t.Fatalf("the other version reads as current: %#v", row)
	}
	press(selector, "\x1b[D", "\x1b[C")
	if got := plainTreeRows(selector.rows); got[2] != "● 2 active" {
		t.Fatalf("right did not come back to the current history: %#v", got)
	}
}

// A reply retried for the same prompt forks after the last prompt; the
// history still ends on a row that carries the versions.
func TestTreeRetriedReplyKeepsItsVersions(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	root := treeTestMessage("q", "", "user", "question")
	root.Children = []*sessionstore.SessionTreeNode{
		treeTestMessage("a1", "q", "assistant", "first answer"),
		treeTestMessage("a2", "q", "assistant", "second answer"),
	}
	selector := NewTreeSelectorComponent([]*sessionstore.SessionTreeNode{root}, "a2", 24, nil, nil, nil, "", "default")
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 question", "● second answer 2/2"}) {
		t.Fatalf("rows = %#v", got)
	}
	press(selector, "\x1b[D")
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 question", "first answer 1/2"}) {
		t.Fatalf("left showed %#v", got)
	}
}

func TestTreeSelectorGoesToTurnEnds(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	var selected []string
	type fork struct {
		id     string
		before bool
	}
	var forks []fork
	selector := NewTreeSelectorComponent(roots, leaf, 24, func(id string) { selected = append(selected, id) }, nil, nil, "", "default")
	selector.OnFork = func(id string, before bool) { forks = append(forks, fork{id, before}) }
	// Enter continues after a turn's reply, e edits its prompt, f forks it.
	press(selector, "g", "\r", "e", "G", "\r", "f")
	if want := []string{"answer", "root", "done"}; !slices.Equal(selected, want) {
		t.Fatalf("selected %#v, want %#v", selected, want)
	}
	if want := []fork{{"done", false}}; !slices.Equal(forks, want) {
		t.Fatalf("forks = %#v, want %#v", forks, want)
	}
	// e only applies to prompts.
	press(selector, "k", "e")
	if len(selected) != 3 {
		t.Fatalf("e on a summary selected %#v", selected)
	}
}

func TestTreeSelectorSearchAndEntries(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	cancelled := 0
	var selected string
	selector := NewTreeSelectorComponent(roots, leaf, 24, func(id string) { selected = id }, func() { cancelled++ }, nil, "", "default")

	height := len(selector.Render(80))
	press(selector, "/", "o", "l", "d")
	// Search covers every history, not only the one shown.
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"2 old 1/2", "summary  tried old 2/2"}) {
		t.Fatalf("search rows = %#v", got)
	}
	lines := stripTreeLines(selector.Render(80))
	if footer := lines[len(lines)-1]; !strings.HasPrefix(footer, "  /old") || !strings.Contains(footer, "2 matches") {
		t.Fatalf("search footer = %q", footer)
	}
	if len(lines) != height {
		t.Fatalf("searching resized the tree from %d to %d lines", height, len(lines))
	}
	// Command letters type into the query while searching.
	press(selector, "f")
	if lines := stripTreeLines(selector.Render(80)); selector.query() != "oldf" || lines[2] != "  Nothing matches “oldf”" {
		t.Fatalf("query %q rendered %#v", selector.query(), lines)
	}
	press(selector, "\x7f", "\x1b[A", "\x1b")
	// Leaving the search shows the history of the match it was on.
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 root", "2 old 1/2"}) || cancelled != 0 {
		t.Fatalf("escape left search with %#v, cancelled = %d", got, cancelled)
	}
	press(selector, "/", "x", "\x7f", "\x7f")
	if selector.filterInput != nil {
		t.Fatal("backspace on an empty query kept the search open")
	}
	press(selector, "/", "a", "c", "t", "\r")
	if selected != "done" {
		t.Fatalf("enter while searching selected %q, want the end of that turn", selected)
	}
	press(selector, "\x1b", "\t")
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"1 root", "answer", "summary  tried old 2/2", "2 active", "● done"}) {
		t.Fatalf("entries rows = %#v", got)
	}
	press(selector, "\x1b")
	if cancelled != 1 {
		t.Fatalf("escape outside the search cancelled = %d, want 1", cancelled)
	}
}

func TestTreeSelectorRendersCountsReplyAndHints(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	roots, leaf := treeTestForked()
	selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil, nil, "", "default")
	lines := stripTreeLines(selector.Render(80))
	want := []string{"  3 turns · 2 branches", "", "  1  root", "     summary  tried old", "● 2  active", "", "     done", "", "", "", "",
		"  enter go to · e edit · / search · ? more"}
	if lines[3] = strings.TrimSuffix(lines[3], "2/2"); !slices.Equal(trimTreeLines(lines), want) {
		t.Fatalf("render =\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	press(selector, "k", "?")
	if footer := stripTreeANSI(selector.Render(80)[len(lines)-1]); footer != "  enter go to · ←→ versions · / search · f fork · tab entries · shift+l label" {
		t.Fatalf("all hints = %q", footer)
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
	press(selector, "\x18")
	if copied != "active" {
		t.Fatalf("copied = %q", copied)
	}
	press(selector, "L", "k", "e", "p", "t")
	if footer := stripTreeANSI(selector.Render(80)[len(selector.Render(80))-1]); !strings.Contains(footer, "label: kept") {
		t.Fatalf("label footer = %q", footer)
	}
	press(selector, "\r")
	if labeled != "kept" {
		t.Fatalf("label = %q", labeled)
	}
	if row := stripTreeLines(selector.Render(80))[4]; !strings.HasPrefix(row, "● 2  active") || !strings.HasSuffix(row, "#kept") {
		t.Fatalf("labeled row = %q", row)
	}
	press(selector, "g", "\x0c")
	if selector.selectedID() != "active" {
		t.Fatalf("ctrl+l jumped to %q, want the labeled row", selector.selectedID())
	}
	press(selector, "/", "k", "e", "p", "t")
	if got := plainTreeRows(selector.rows); !slices.Equal(got, []string{"2 active"}) {
		t.Fatalf("label search rows = %#v", got)
	}
}

// The renderer panics on any line wider than the terminal, so every line has
// to fit, whatever the mode.
func TestTreeSelectorFitsNarrowWidths(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	for _, width := range []int{1, 8, 16, 26, 34, 60, 120} {
		for _, keys := range [][]string{nil, {"L"}, {"/", "o"}, {"\t"}, {"?"}} {
			roots, leaf := treeTestForked()
			selector := NewTreeSelectorComponent(roots, leaf, 24, nil, nil, func(string, *string) {}, "", "default")
			press(selector, keys...)
			for _, line := range selector.Render(width) {
				if got := tui.VisibleWidth(line); got > width {
					t.Fatalf("width %d keys %q: line of width %d: %q", width, keys, got, line)
				}
			}
		}
	}
}

// Renders run on the TUI's timer while keys arrive: a keystroke must never
// leave a render indexing rows that are gone.
func TestTreeSelectorRendersWhileTyping(t *testing.T) {
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
		press(selector, "/", "o", "l", "d", "\x1b", "\t", "k", "\x1b[D", "\x1b[C")
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
	if rows := len(selector.rows); rows != 10000 {
		t.Fatalf("rows = %d, want 10000 prompts", rows)
	}
	lines := stripTreeLines(selector.Render(80))
	if len(lines) != selector.maxVisible+5+treePreviewLines {
		t.Fatalf("rendered %d lines, want window %d plus counts, preview and hints", len(lines), selector.maxVisible)
	}
	if row := lines[1+selector.maxVisible]; !strings.HasPrefix(row, "● 10000  user 19998") || !strings.HasSuffix(lines[0], "10000/10000") {
		t.Fatalf("current turn is not the last row: %q / %q", row, lines[0])
	}
	press(selector, "/", "u", "s", "e", "r")
	if rows := len(selector.rows); rows != 9999 {
		t.Fatalf("search rows = %d", rows)
	}
}

func press(selector *TreeSelectorComponent, keys ...string) {
	for _, key := range keys {
		selector.HandleInput(tui.KeyEvent{Raw: key})
	}
}

// plainTreeRows reads rows as "● 2 text 1/2": the current dot, the turn, the
// text and the version shown.
func plainTreeRows(rows []treeRow) []string {
	result := make([]string, len(rows))
	for index, row := range rows {
		var parts []string
		if row.current {
			parts = append(parts, "●")
		}
		if row.turn > 0 {
			parts = append(parts, fmt.Sprint(row.turn))
		}
		parts = append(parts, row.text)
		if len(row.versions) > 1 {
			parts = append(parts, fmt.Sprintf("%d/%d", row.version+1, len(row.versions)))
		}
		result[index] = strings.Join(parts, " ")
	}
	return result
}

func trimTreeLines(lines []string) []string {
	result := make([]string, len(lines))
	for index, line := range lines {
		result[index] = strings.TrimRight(line, " ")
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
