package modes

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"
)

// Every selector drives the one shared pointer semantic in tui.HandleListMouse.
var (
	_ tui.ListMouseTarget = (*ModelSelectorComponent)(nil)
	_ tui.ListMouseTarget = (*OAuthSelectorComponent)(nil)
	_ tui.ListMouseTarget = (*SessionSelectorComponent)(nil)
	_ tui.ListMouseTarget = (*TreeSelectorComponent)(nil)
	_ tui.ListRowClicker  = (*TreeSelectorComponent)(nil)
	_ tui.ListMouseTarget = (*ExtensionSelectorComponent)(nil)
)

// mouseTerminal keeps the TUI input callback so tests can push real SGR bytes.
type mouseTerminal struct {
	*fakeTerminalImpl
	input func(string)
}

func (terminal *mouseTerminal) Start(input func(string), _ func()) error {
	terminal.input = input
	return nil
}

func lineIndexContaining(t *testing.T, lines []string, text string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(selectorANSI.ReplaceAllString(line, ""), text) {
			return index
		}
	}
	t.Fatalf("no rendered line contains %q:\n%s", text, strings.Join(lines, "\n"))
	return -1
}

// branchingTreeFixture builds a chain where every level branches, so rows carry
// fold markers at increasing indents and CJK labels widen them unevenly.
func branchingTreeFixture(levels int) (*sessionstore.SessionTreeNode, string) {
	root := treeTestMessage("root", "", "user", "根 root")
	parent, leaf := root, "root"
	for level := range levels {
		main := treeTestMessage(fmt.Sprintf("m%d", level), parent.Entry.ID, "assistant", fmt.Sprintf("主%d main", level))
		side := treeTestMessage(fmt.Sprintf("s%d", level), parent.Entry.ID, "user", fmt.Sprintf("側%d side", level))
		parent.Children = []*sessionstore.SessionTreeNode{main, side}
		parent, leaf = main, main.Entry.ID
	}
	return root, leaf
}

func newTreeFixtureSelector(t *testing.T, levels, terminalHeight int, onSelect func(string)) *TreeSelectorComponent {
	t.Helper()
	initTestTheme(t)
	useTreeTestKeybindings(t)
	root, leaf := branchingTreeFixture(levels)
	return NewTreeSelectorComponent(
		[]*sessionstore.SessionTreeNode{root}, leaf, terminalHeight, onSelect, nil, nil, "", "default",
	)
}

func TestTreeSelectorClickSelectsRowAndDoubleClickConfirms(t *testing.T) {
	confirmed := ""
	// A five-row window recentres on every click, so the double click must not
	// re-resolve the cell it landed on.
	selector := newTreeFixtureSelector(t, 6, 10, func(id string) { confirmed = id })
	row := lineIndexContaining(t, selector.Render(60), "assistant: 主3 main")

	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: 20, Clicks: 1}) {
		t.Fatal("click on a tree row was not consumed")
	}
	if got := selector.selectedID(); got != "m3" {
		t.Fatalf("single click selected %q, want m3", got)
	}
	if confirmed != "" {
		t.Fatalf("single click confirmed %q", confirmed)
	}
	if selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Column: 2, Clicks: 1}) {
		t.Fatal("click on the selector header was consumed")
	}

	selector.Render(60)
	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: 20, Clicks: 2})
	if confirmed != "m3" {
		t.Fatalf("double click confirmed %q, want m3", confirmed)
	}
}

// treeScreenRow locates a row on screen by entry ID; this test is about the
// column math, and the row math is covered above.
func treeScreenRow(t *testing.T, selector *TreeSelectorComponent, id string) int {
	t.Helper()
	top, start, count, _ := selector.rowLayout()
	for index, row := range selector.view.rows {
		if row.node.Entry.ID != id {
			continue
		}
		if index < start || index >= start+count {
			t.Fatalf("row %s is off screen", id)
		}
		return top + index - start
	}
	t.Fatalf("no row for %s", id)
	return -1
}

func TestTreeSelectorClickTogglesFoldAcrossHorizontalScroll(t *testing.T) {
	confirmed := ""
	selector := newTreeFixtureSelector(t, 6, 40, func(id string) { confirmed = id })
	// Width 28 scrolls the tree sideways; the marker column must follow it.
	lines := selector.Render(28)
	_, _, _, scroll := selector.rowLayout()
	if scroll == 0 {
		t.Fatalf("fixture did not scroll horizontally:\n%s", strings.Join(lines, "\n"))
	}
	row := treeScreenRow(t, selector, "m4")
	start, end := treeFoldSpan(selector.view.rows[5])
	if end <= start || start < scroll {
		t.Fatalf("fold marker [%d,%d) is not visible at scroll %d", start, end, scroll)
	}

	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: start - scroll + 2, Clicks: 1})
	if !selector.folded["m4"] {
		t.Fatalf("click on the scrolled fold marker did not fold:\n%s", strings.Join(selector.Render(28), "\n"))
	}
	if slices.Contains(visibleTreeIDs(selector), "m5") {
		t.Fatal("folded branch still lists its descendants")
	}

	selector.Render(28)
	_, _, _, scroll = selector.rowLayout()
	row = treeScreenRow(t, selector, "m4")
	_, end = treeFoldSpan(selector.view.rows[5])
	// One column past the marker selects the row without toggling the fold.
	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: end - scroll + 2, Clicks: 1})
	if !selector.folded["m4"] || selector.selectedID() != "m4" {
		t.Fatalf("click beside the marker = folded %v selected %q", selector.folded["m4"], selector.selectedID())
	}

	selector.Render(28)
	_, _, _, scroll = selector.rowLayout()
	row = treeScreenRow(t, selector, "m4")
	start, _ = treeFoldSpan(selector.view.rows[5])
	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: start - scroll + 2, Clicks: 1})
	if selector.folded["m4"] {
		t.Fatal("clicking the marker again did not unfold")
	}
	// A double click on the marker folds and stops; it must not navigate.
	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Column: start - scroll + 2, Clicks: 2})
	if confirmed != "" {
		t.Fatalf("double click on the fold marker opened %q", confirmed)
	}
}

func TestTreeSelectorWheelScrollsSelection(t *testing.T) {
	selector := newTreeFixtureSelector(t, 6, 40, nil)
	selector.Render(60)
	before := selector.selected

	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelUp}) {
		t.Fatal("wheel was not consumed")
	}
	if selector.selected != before-3 {
		t.Fatalf("wheel up = %d, want %d", selector.selected, before-3)
	}
	for range 10 {
		selector.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelUp})
	}
	if selector.selected != 0 {
		t.Fatalf("wheel up clamped at %d, want 0", selector.selected)
	}
	for range 20 {
		selector.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelDown})
	}
	if selector.selected != len(selector.view.rows)-1 {
		t.Fatalf("wheel down clamped at %d, want %d", selector.selected, len(selector.view.rows)-1)
	}
}

func TestTreeSelectorClickArrivesThroughTerminalBytes(t *testing.T) {
	selector := newTreeFixtureSelector(t, 6, 40, nil)
	terminal := &mouseTerminal{fakeTerminalImpl: newFakeTerminal(60, 30)}
	ui := tui.NewTUI(terminal)
	body, chrome := &tui.Container{}, &tui.Container{}
	chrome.AddChild(selector)
	ui.AddChild(body)
	ui.AddChild(chrome)
	ui.SetViewport(body, chrome)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()

	lines := selector.Render(60)
	row := lineIndexContaining(t, lines, "assistant: 主1 main")
	// The chrome is bottom-aligned, so the selector starts below the transcript.
	screenRow := terminal.Rows() - len(lines) + row
	terminal.input(fmt.Sprintf("\x1b[<0;%d;%dM", 21, screenRow+1))
	if got := selector.selectedID(); got != "m1" {
		t.Fatalf("terminal click selected %q, want m1", got)
	}
	// Unrecognised reports must be swallowed, never typed into the search box.
	terminal.input("\x1b[<66;10;10M")
	if selector.query != "" {
		t.Fatalf("mouse bytes leaked into the tree search: %q", selector.query)
	}
}

func TestSessionSelectorClickSelectsAndDoubleClickResumes(t *testing.T) {
	initTestTheme(t)
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	sessions := []sessionstore.SessionInfo{
		{Path: "/tmp/one.jsonl", ID: "one", FirstMessage: "first session", Modified: now},
		{Path: "/tmp/two.jsonl", ID: "two", FirstMessage: "second session", Modified: now},
		{Path: "/tmp/three.jsonl", ID: "three", FirstMessage: "third session", Modified: now},
	}
	resumed := ""
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: func(sessionstore.SessionListProgress) []sessionstore.SessionInfo { return sessions },
		Now:             func() time.Time { return now },
	}, func(path string) { resumed = path }, nil)
	waitForSelector(t, selector, "third session")

	row := lineIndexContaining(t, selector.Render(100), "third session")
	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 1}) {
		t.Fatal("session click was not consumed")
	}
	if resumed != "" {
		t.Fatalf("single click resumed %q", resumed)
	}
	selector.Render(100)
	selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 2})
	if resumed != "/tmp/three.jsonl" {
		t.Fatalf("double click resumed %q", resumed)
	}
	if selector.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Clicks: 1}) {
		t.Fatal("click on the session selector header was consumed")
	}
}

func modelSelectorMouseFixture(t *testing.T, count int, onSelect func(ai.Model)) *ModelSelectorComponent {
	t.Helper()
	initTestTheme(t)
	models := make([]ai.Model, count)
	for index := range count {
		models[index] = ai.Model{
			ID:       fmt.Sprintf("model-%02d", index),
			Name:     fmt.Sprintf("Model %02d", index),
			Provider: "prov",
		}
	}
	return NewModelSelectorComponent(nil, models, nil, onSelect, nil, "")
}

func TestModelSelectorClickWheelHoverAndDoubleClick(t *testing.T) {
	confirmed := ""
	component := modelSelectorMouseFixture(t, 5, func(model ai.Model) { confirmed = model.ID })
	row := lineIndexContaining(t, component.Render(80), "model-02")

	if !component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 1}) {
		t.Fatal("model click was not consumed")
	}
	if confirmed != "" {
		t.Fatalf("single click confirmed %q, want a selection only", confirmed)
	}
	if index := lineIndexContaining(t, component.Render(80), "→ model-02"); index != row {
		t.Fatalf("cursor moved to row %d, want %d", index, row)
	}
	component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 2})
	if confirmed != "model-02" {
		t.Fatalf("double click confirmed %q", confirmed)
	}
	if component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Clicks: 1}) {
		t.Fatal("click on the dialog border was consumed")
	}

	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelDown}) {
		t.Fatal("wheel was not consumed")
	}
	lineIndexContaining(t, component.Render(80), "→ model-03")

	hoverRow := lineIndexContaining(t, component.Render(80), "model-01")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: hoverRow}) {
		t.Fatal("hover was not consumed")
	}
	if index := lineIndexContaining(t, component.Render(80), "→ model-01"); index != hoverRow {
		t.Fatalf("hover highlight on row %d, want %d", index, hoverRow)
	}
	if confirmed != "model-02" {
		t.Fatalf("hover confirmed %q", confirmed)
	}
}

// visibleRowTokens is the ordered set of rows the render showed whose text
// contains prefix, reduced to the fixed-width token starting at prefix, with
// styling and the selection cursor stripped. Scroll counters, footers, and
// search lines carry no such token, so comparisons see only the window.
func visibleRowTokens(lines []string, prefix string, tokenLen int) []string {
	tokens := []string{}
	for _, line := range lines {
		plain := selectorANSI.ReplaceAllString(line, "")
		if index := strings.Index(plain, prefix); index >= 0 && index+tokenLen <= len(plain) {
			tokens = append(tokens, plain[index:index+tokenLen])
		}
	}
	return tokens
}

// visibleModelIDs is the ordered set of model rows the last render showed.
func visibleModelIDs(component *ModelSelectorComponent, width int) []string {
	return visibleRowTokens(component.Render(width), "model-", len("model-00"))
}

func TestModelSelectorHoverPreservesWindowOnScrollableList(t *testing.T) {
	// The /model list is scrollable (30 models, 10 visible). Hover must move
	// the selection IN PLACE: same window offset, same visible row set.
	confirmed := ""
	component := modelSelectorMouseFixture(t, 30, func(model ai.Model) { confirmed = model.ID })
	before := visibleModelIDs(component, 80)
	if len(before) != modelSelectorMaxVisible {
		t.Fatalf("fixture window = %d rows, want %d", len(before), modelSelectorMaxVisible)
	}

	// Hovering the last visible row must not scroll the window.
	lastRow := lineIndexContaining(t, component.Render(80), "model-09")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: lastRow}) {
		t.Fatal("hover on a scrollable list was not consumed")
	}
	if index := lineIndexContaining(t, component.Render(80), "→ model-09"); index != lastRow {
		t.Fatalf("hover highlight on row %d, want %d", index, lastRow)
	}
	if after := visibleModelIDs(component, 80); !slices.Equal(before, after) {
		t.Fatalf("hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}

	// The first visible row behaves the same at the other edge.
	firstRow := lineIndexContaining(t, component.Render(80), "model-00")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: firstRow}) {
		t.Fatal("hover on the first visible row was not consumed")
	}
	if after := visibleModelIDs(component, 80); !slices.Equal(before, after) {
		t.Fatalf("edge hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}

	// The wheel scrolls exactly as before — selection moves, window
	// recenters — and hover then tracks the rows the new window shows.
	for range 7 {
		component.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelDown})
	}
	scrolled := visibleModelIDs(component, 80)
	if slices.Equal(before, scrolled) {
		t.Fatal("wheel did not scroll the window")
	}
	hoverRow := lineIndexContaining(t, component.Render(80), "model-04")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: hoverRow}) {
		t.Fatal("hover after wheel was not consumed")
	}
	if index := lineIndexContaining(t, component.Render(80), "→ model-04"); index != hoverRow {
		t.Fatalf("hover after wheel highlighted row %d, want %d", index, hoverRow)
	}
	if after := visibleModelIDs(component, 80); !slices.Equal(scrolled, after) {
		t.Fatalf("hover after wheel re-anchored the window:\nbefore %q\nafter  %q", scrolled, after)
	}

	// Enter confirms the hover selection exactly like a keyboard one.
	component.HandleInput(tui.KeyEvent{Raw: "\n"})
	if confirmed != "model-04" {
		t.Fatalf("enter confirmed %q, want model-04", confirmed)
	}

	// A hover-selected row scrolled away by the wheel: the wheel moves the
	// selection itself, so the selection stays valid and visible.
	component.HandleMouse(tui.MouseEvent{Type: tui.MouseWheelDown})
	lineIndexContaining(t, component.Render(80), "→ model-05")
}

func TestOAuthSelectorHoverPreservesWindowOnScrollableList(t *testing.T) {
	initTestTheme(t)
	providers := make([]InteractiveAuthProvider, 12)
	for index := range providers {
		providers[index] = InteractiveAuthProvider{ID: fmt.Sprintf("p%02d", index), Name: fmt.Sprintf("Provider%02d", index)}
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogin, providers, nil, nil, "")
	before := visibleRowTokens(component.Render(80), "Provider", len("Provider00"))
	if len(before) != authSelectorMaxVisible {
		t.Fatalf("fixture window = %d rows, want %d", len(before), authSelectorMaxVisible)
	}

	row := lineIndexContaining(t, component.Render(80), "Provider07")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: row}) {
		t.Fatal("hover on a scrollable provider list was not consumed")
	}
	if index := lineIndexContaining(t, component.Render(80), "→ Provider07"); index != row {
		t.Fatalf("hover highlight on row %d, want %d", index, row)
	}
	if after := visibleRowTokens(component.Render(80), "Provider", len("Provider00")); !slices.Equal(before, after) {
		t.Fatalf("hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}
}

func TestSessionSelectorHoverPreservesWindowOnScrollableList(t *testing.T) {
	initTestTheme(t)
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	sessions := make([]sessionstore.SessionInfo, 14)
	for index := range sessions {
		sessions[index] = sessionstore.SessionInfo{
			Path:         fmt.Sprintf("/tmp/s%02d.jsonl", index),
			ID:           fmt.Sprintf("s%02d", index),
			FirstMessage: fmt.Sprintf("session %02d", index),
			Modified:     now.Add(-time.Duration(index) * time.Minute),
		}
	}
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: func(sessionstore.SessionListProgress) []sessionstore.SessionInfo { return sessions },
		Now:             func() time.Time { return now },
	}, func(string) {}, nil)
	waitForSelector(t, selector, "session 00")

	before := visibleRowTokens(selector.Render(100), "session ", len("session 00"))
	if len(before) != selector.maxVisible {
		t.Fatalf("fixture window = %d rows, want %d", len(before), selector.maxVisible)
	}
	row := lineIndexContaining(t, selector.Render(100), "session 06")
	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: row}) {
		t.Fatal("hover on a scrollable session list was not consumed")
	}
	if index := lineIndexContaining(t, selector.Render(100), "session 06"); index != row {
		t.Fatalf("hovered row moved to %d, want %d", index, row)
	}
	if selector.selected < 0 || selector.selected >= len(selector.filtered) ||
		selector.filtered[selector.selected].session.ID != "s06" {
		t.Fatalf("hover selected index %d, want the s06 row", selector.selected)
	}
	if after := visibleRowTokens(selector.Render(100), "session ", len("session 00")); !slices.Equal(before, after) {
		t.Fatalf("hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}
}

func TestOAuthSelectorClickSelectsHoverHighlightsAndDoubleClickConfirms(t *testing.T) {
	initTestTheme(t)
	chosen := ""
	providers := []InteractiveAuthProvider{
		{ID: "alpha", Name: "Alpha"},
		{ID: "beta", Name: "Beta"},
		{ID: "gamma", Name: "Gamma"},
	}
	component := NewOAuthSelectorComponent(oauthSelectorLogin, providers,
		func(provider InteractiveAuthProvider) { chosen = provider.ID }, nil, "")

	row := lineIndexContaining(t, component.Render(80), "Beta")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 1}) {
		t.Fatal("provider click was not consumed")
	}
	if chosen != "" {
		t.Fatalf("single click confirmed %q, want a selection only", chosen)
	}
	if index := lineIndexContaining(t, component.Render(80), "→ Beta"); index != row {
		t.Fatalf("cursor moved to row %d, want %d", index, row)
	}
	component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 2})
	if chosen != "beta" {
		t.Fatalf("double click confirmed %q", chosen)
	}

	hoverRow := lineIndexContaining(t, component.Render(80), "Gamma")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: hoverRow}) {
		t.Fatal("hover was not consumed")
	}
	lineIndexContaining(t, component.Render(80), "→ Gamma")
	if component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Clicks: 1}) {
		t.Fatal("click on the dialog border was consumed")
	}
}

func TestSessionSelectorHoverMovesSelection(t *testing.T) {
	initTestTheme(t)
	now := time.Date(2026, 7, 18, 22, 0, 0, 0, time.UTC)
	sessions := []sessionstore.SessionInfo{
		{Path: "/tmp/one.jsonl", ID: "one", FirstMessage: "first session", Modified: now},
		{Path: "/tmp/two.jsonl", ID: "two", FirstMessage: "second session", Modified: now},
		{Path: "/tmp/three.jsonl", ID: "three", FirstMessage: "third session", Modified: now},
	}
	selector := NewSessionSelectorComponent(SessionSelectorOptions{
		CurrentSessions: func(sessionstore.SessionListProgress) []sessionstore.SessionInfo { return sessions },
		Now:             func() time.Time { return now },
	}, func(string) {}, nil)
	waitForSelector(t, selector, "third session")

	row := lineIndexContaining(t, selector.Render(100), "second session")
	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: row}) {
		t.Fatal("hover was not consumed")
	}
	if index := lineIndexContaining(t, selector.Render(100), "› second session"); index != row {
		t.Fatalf("hover highlight on row %d, want %d", index, row)
	}
}

func TestTreeSelectorHoverMovesSelectionInPlace(t *testing.T) {
	selector := newTreeFixtureSelector(t, 2, 40, nil)
	row := lineIndexContaining(t, selector.Render(60), "user: 側1 side")
	if !selector.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: row, Column: 10}) {
		t.Fatal("hover was not consumed")
	}
	if got := selector.selectedID(); got != "s1" {
		t.Fatalf("hover selected %q, want s1", got)
	}

	// A window smaller than the tree must not recentre on a hover-driven
	// selection change: the visible rows stay put under the cursor.
	scrolled := newTreeFixtureSelector(t, 6, 10, nil)
	scrolledRow := lineIndexContaining(t, scrolled.Render(60), "assistant: 主3 main")
	_, beforeStart, _, _ := scrolled.rowLayout()
	if !scrolled.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: scrolledRow, Column: 10}) {
		t.Fatal("hover on a scrolling tree was not consumed")
	}
	if got := scrolled.selectedID(); got != "m3" {
		t.Fatalf("hover selected %q, want m3", got)
	}
	if index := lineIndexContaining(t, scrolled.Render(60), "assistant: 主3 main"); index != scrolledRow {
		t.Fatalf("hovered row moved to %d, want %d", index, scrolledRow)
	}
	if _, afterStart, _, _ := scrolled.rowLayout(); afterStart != beforeStart {
		t.Fatalf("hover re-anchored the window: start %d, want %d", afterStart, beforeStart)
	}
}

func TestExtensionSelectorHoverMovesHighlight(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	chosen := ""
	component := NewExtensionSelectorItemsComponent("Permission", []tui.SelectItem{
		{Value: "y approve once"},
		{Value: "n reject"},
	}, func(value string) { chosen = value }, nil, nil)

	row := lineIndexContaining(t, component.Render(60), "n reject")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: row}) {
		t.Fatal("hover was not consumed")
	}
	if index := lineIndexContaining(t, component.Render(60), "→ n reject"); index != row {
		t.Fatalf("hover highlight on row %d, want %d", index, row)
	}
	if chosen != "" {
		t.Fatalf("hover confirmed %q", chosen)
	}
	if component.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: 0}) {
		t.Fatal("hover on the dialog border was consumed")
	}
}

func TestExtensionSelectorClickSelectsAndDoubleClickConfirms(t *testing.T) {
	initTestTheme(t)
	useTreeTestKeybindings(t)
	chosen := ""
	component := NewExtensionSelectorItemsComponent("Permission", []tui.SelectItem{
		{Value: "y approve once"},
		{Value: "s approve for this session"},
		{Value: "n reject"},
	}, func(value string) { chosen = value }, nil, nil)

	row := lineIndexContaining(t, component.Render(60), "approve for this session")
	if !component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 1}) {
		t.Fatal("option click was not consumed")
	}
	if chosen != "" {
		t.Fatalf("single click confirmed %q, want a selection only", chosen)
	}
	if index := lineIndexContaining(t, component.Render(60), "→ s approve for this session"); index != row {
		t.Fatalf("cursor moved to row %d, want %d", index, row)
	}
	component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: row, Clicks: 2})
	if chosen != "s approve for this session" {
		t.Fatalf("double click confirmed %q", chosen)
	}
	if component.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Clicks: 1}) {
		t.Fatal("click on the dialog border was consumed")
	}
}
