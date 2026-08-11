package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Selection e2e tests drive raw SGR bytes through the tracking-mode-faithful
// terminal emulator from mouse_e2e_test.go. They pin the content-anchored
// selection model: selection lives in transcript coordinates, is constrained
// to the thread, extends across messages, and auto-scrolls at the viewport
// edges.

// selectionFixture wires a copy channel and a controllable auto-scroll
// interval onto the shared chat-shaped fixture.
type selectionFixture struct {
	*mouseE2EFixture
	copied chan string
}

func newSelectionFixture(t *testing.T) *selectionFixture {
	t.Helper()
	fixture := &selectionFixture{mouseE2EFixture: newMouseE2EFixture(t), copied: make(chan string, 1)}
	fixture.ui.SetSelectionHandler(func(text string) { fixture.copied <- text })
	return fixture
}

func (fixture *selectionFixture) setScrollInterval(interval time.Duration) {
	fixture.ui.renderMu.Lock()
	fixture.ui.selectionScroll.interval = interval
	fixture.ui.renderMu.Unlock()
}

func (fixture *selectionFixture) bodyHeight() int {
	fixture.ui.renderMu.Lock()
	defer fixture.ui.renderMu.Unlock()
	return fixture.ui.viewportBodyHeight
}

func (fixture *selectionFixture) selectionState() (mouseSelection, selectionAutoScroll) {
	fixture.ui.renderMu.Lock()
	defer fixture.ui.renderMu.Unlock()
	return fixture.ui.selection, fixture.ui.selectionScroll
}

// tickSelectionScroll fires one auto-scroll tick deterministically, exactly
// as the armed timer would.
func (fixture *selectionFixture) tickSelectionScroll(t *testing.T) {
	t.Helper()
	fixture.ui.renderMu.Lock()
	generation, armed := fixture.ui.selectionScroll.generation, fixture.ui.selectionScroll.timer != nil
	fixture.ui.renderMu.Unlock()
	if !armed {
		t.Fatal("auto-scroll timer is not armed")
	}
	fixture.ui.selectionScrollTick(generation)
}

func (fixture *selectionFixture) copiedText(t *testing.T) string {
	t.Helper()
	select {
	case text := <-fixture.copied:
		return text
	case <-time.After(2 * time.Second):
		t.Fatal("selection release did not reach the selection handler")
		return ""
	}
}

// TestSelectionE2EEdgeDragAutoScrollsAndSpansScroll is the headline flow: a
// drag from mid-thread past the bottom edge scrolls the viewport on the real
// timer, the content-anchored selection extends as it flows, and the released
// copy spans pre- and post-scroll content.
func TestSelectionE2EEdgeDragAutoScrollsAndSpansScroll(t *testing.T) {
	fixture := newSelectionFixture(t)
	fixture.setScrollInterval(2 * time.Millisecond)
	height := fixture.bodyHeight()

	// Scroll away from the live tail so there is room to auto-scroll down.
	for range 4 {
		fixture.terminal.deliver(sgr(65, 5, 2, false)) // wheel down is 65; up is 64
	}
	for range 8 {
		fixture.terminal.deliver(sgr(64, 5, 2, false))
	}
	startEnd := fixture.viewportEndNow()
	if fixture.following() || startEnd >= 100 {
		t.Fatalf("setup did not detach the viewport: end=%d follow=%v", startEnd, fixture.following())
	}
	anchorRow := startEnd - height + 2

	fixture.terminal.deliver(sgr(0, 0, 2, false))          // press mid-thread
	fixture.terminal.deliver(sgr(32, 30, height+1, false)) // drag past the bottom edge, over the editor
	deadline := time.Now().Add(2 * time.Second)
	for fixture.viewportEndNow() <= startEnd+2 {
		if time.Now().After(deadline) {
			t.Fatalf("edge drag did not auto-scroll: end still %d", fixture.viewportEndNow())
		}
		time.Sleep(time.Millisecond)
	}

	fixture.terminal.deliver(sgr(0, 30, height+1, true)) // release
	selection, scroll := fixture.selectionState()
	if scroll.timer != nil || scroll.rows != 0 {
		t.Fatalf("release left the auto-scroll timer armed: %+v", scroll)
	}
	restingEnd := fixture.viewportEndNow()
	if restingEnd <= startEnd+2 {
		t.Fatalf("edge drag did not scroll the viewport: end %d -> %d", startEnd, restingEnd)
	}
	if selection.anchor.row != anchorRow {
		t.Fatalf("anchor moved during auto-scroll: row %d, want %d", selection.anchor.row, anchorRow)
	}
	if selection.focus.row != restingEnd-1 {
		t.Fatalf("focus did not extend with the scroll: row %d, end %d", selection.focus.row, restingEnd)
	}
	time.Sleep(20 * time.Millisecond)
	if got := fixture.viewportEndNow(); got != restingEnd {
		t.Fatalf("viewport kept scrolling after release: %d -> %d", restingEnd, got)
	}

	text := fixture.copiedText(t)
	if !strings.HasPrefix(text, fmt.Sprintf("line %d", anchorRow)) {
		t.Fatalf("copy does not start at the pre-scroll anchor: %q", text)
	}
	if !strings.Contains(text, fmt.Sprintf("line %d\n", startEnd)) {
		t.Fatalf("copy does not span content scrolled in after the drag began: %q", text)
	}
	if !strings.HasSuffix(text, fmt.Sprintf("line %d", restingEnd-1)) {
		t.Fatalf("copy does not end at the post-scroll focus: %q", text)
	}
}

// TestSelectionE2EScrollRateScalesWithOvershoot pins the rate curve: a
// pointer resting on the edge row scrolls gently, a pointer far past it
// scrolls faster, one deterministic tick each.
func TestSelectionE2EScrollRateScalesWithOvershoot(t *testing.T) {
	fixture := newSelectionFixture(t)
	fixture.setScrollInterval(time.Hour) // ticks only fire manually
	height := fixture.bodyHeight()

	for range 4 {
		fixture.terminal.deliver(sgr(65, 5, 2, false))
	}
	for range 12 {
		fixture.terminal.deliver(sgr(64, 5, 2, false))
	}
	fixture.terminal.deliver(sgr(0, 0, 2, false))

	fixture.terminal.deliver(sgr(32, 3, height-1, false)) // rest on the edge row
	before := fixture.viewportEndNow()
	fixture.tickSelectionScroll(t)
	edgeDelta := fixture.viewportEndNow() - before
	if edgeDelta != selectionScrollRate(0) {
		t.Fatalf("edge tick scrolled %d rows, want %d", edgeDelta, selectionScrollRate(0))
	}

	overshoot := 11 - (height - 1) // bottom screen row, well past the edge
	fixture.terminal.deliver(sgr(32, 3, 11, false))
	before = fixture.viewportEndNow()
	fixture.tickSelectionScroll(t)
	farDelta := fixture.viewportEndNow() - before
	if farDelta != selectionScrollRate(overshoot) || farDelta <= edgeDelta {
		t.Fatalf("far-overshoot tick scrolled %d rows (edge %d), want %d and faster than the edge",
			farDelta, edgeDelta, selectionScrollRate(overshoot))
	}

	// Dragging back inside the thread stops the timer without ending the drag.
	fixture.terminal.deliver(sgr(32, 3, 2, false))
	selection, scroll := fixture.selectionState()
	if !selection.active || scroll.timer != nil || scroll.rows != 0 {
		t.Fatalf("re-entering the thread did not idle auto-scroll: selection=%+v scroll=%+v", selection, scroll)
	}
	fixture.terminal.deliver(sgr(0, 3, 2, true))
	fixture.copiedText(t)
}

// TestSelectionE2EReleaseAndEscapeStopTicker asserts both drag terminators
// disarm the auto-scroll timer and that a stale tick cannot scroll.
func TestSelectionE2EReleaseAndEscapeStopTicker(t *testing.T) {
	fixture := newSelectionFixture(t)
	fixture.setScrollInterval(time.Hour)
	height := fixture.bodyHeight()

	// Each press lands on a different cell so back-to-back drags never read
	// as a double click.
	armPastEdge := func(row int) {
		fixture.terminal.deliver(sgr(0, 0, row, false))
		fixture.terminal.deliver(sgr(32, 3, height+1, false))
		if _, scroll := fixture.selectionState(); scroll.timer == nil {
			t.Fatal("edge drag did not arm the auto-scroll timer")
		}
	}
	assertDisarmed := func(context string) {
		fixture.ui.renderMu.Lock()
		staleGeneration := fixture.ui.selectionScroll.generation
		timer := fixture.ui.selectionScroll.timer
		fixture.ui.renderMu.Unlock()
		if timer != nil {
			t.Fatalf("%s left the auto-scroll timer armed", context)
		}
		before := fixture.viewportEndNow()
		fixture.ui.selectionScrollTick(staleGeneration)
		if got := fixture.viewportEndNow(); got != before {
			t.Fatalf("stale tick after %s scrolled the viewport: %d -> %d", context, before, got)
		}
	}

	armPastEdge(2)
	fixture.terminal.deliver(sgr(0, 3, height+1, true)) // release
	assertDisarmed("release")
	fixture.copiedText(t)

	armPastEdge(3)
	fixture.terminal.send("\x1b") // escape cancels the drag
	selection, _ := fixture.selectionState()
	if selection.active {
		t.Fatal("escape did not cancel the active selection")
	}
	assertDisarmed("escape")
}

// TestSelectionE2EChromePressStartsNothing pins the thread constraint: a
// press over the editor chrome (or the filler below a short transcript)
// never starts a text selection.
func TestSelectionE2EChromePressStartsNothing(t *testing.T) {
	fixture := newSelectionFixture(t)
	height := fixture.bodyHeight()

	// The editor's top border declines the press; constrained selection must
	// not pick it up either.
	fixture.terminal.deliver(sgr(0, 3, height, false))
	fixture.terminal.deliver(sgr(32, 5, height, false))
	fixture.terminal.deliver(sgr(0, 5, height, true))
	if selection, _ := fixture.selectionState(); selection.active || selection.moved {
		t.Fatalf("chrome press started a selection: %+v", selection)
	}
	// Only the transcript rows matter: the editor legitimately paints its
	// block cursor with the same reverse-video attribute.
	fixture.ui.RenderNow()
	fixture.ui.renderMu.Lock()
	body := strings.Join(fixture.ui.previousLines[:height], "\n")
	fixture.ui.renderMu.Unlock()
	if strings.Contains(body, "\x1b[7m") {
		t.Fatalf("chrome drag painted a selection highlight in the thread: %q", body)
	}
	select {
	case text := <-fixture.copied:
		t.Fatalf("chrome drag copied %q", text)
	default:
	}

	// Filler rows below a short transcript are not the thread either.
	fixture.body.lines = fixture.body.lines[:2]
	fixture.ui.RenderNow()
	fixture.terminal.deliver(sgr(0, 3, 4, false))
	if selection, _ := fixture.selectionState(); selection.active {
		t.Fatalf("press on filler rows started a selection: %+v", selection)
	}
}

// TestSelectionE2EWheelDuringSelectionKeepsAnchor pins that wheel scrolling
// mid-drag keeps the content anchor and extends the focus with the content
// flowing under the pointer.
func TestSelectionE2EWheelDuringSelectionKeepsAnchor(t *testing.T) {
	fixture := newSelectionFixture(t)
	height := fixture.bodyHeight()

	for range 8 {
		fixture.terminal.deliver(sgr(64, 5, 2, false))
	}
	startEnd := fixture.viewportEndNow()
	anchorRow := startEnd - height + 2

	fixture.terminal.deliver(sgr(0, 0, 2, false))
	fixture.terminal.deliver(sgr(32, 30, 4, false))
	selection, _ := fixture.selectionState()
	focusBefore := selection.focus.row

	fixture.terminal.deliver(sgr(65, 30, 4, false)) // wheel down mid-drag
	selection, _ = fixture.selectionState()
	if !selection.active {
		t.Fatal("wheel mid-drag dropped the selection")
	}
	if selection.anchor.row != anchorRow {
		t.Fatalf("wheel mid-drag moved the anchor: row %d, want %d", selection.anchor.row, anchorRow)
	}
	if selection.focus.row != focusBefore+3 {
		t.Fatalf("wheel mid-drag focus = row %d, want %d", selection.focus.row, focusBefore+3)
	}

	fixture.terminal.deliver(sgr(0, 30, 4, true))
	text := fixture.copiedText(t)
	if !strings.HasPrefix(text, fmt.Sprintf("line %d", anchorRow)) {
		t.Fatalf("copy after mid-drag wheel does not start at the anchor: %q", text)
	}
	if !strings.HasSuffix(text, fmt.Sprintf("line %d", focusBefore+3)) {
		t.Fatalf("copy after mid-drag wheel does not reach the extended focus: %q", text)
	}
}

// TestSelectionE2EMultiMessageDragExtractsCleanContent drives a drag across
// chat-band-shaped messages and asserts the copy is clean joined content:
// no gutter bars, no interior padding columns, no duplicated padding rows,
// deeper content indentation preserved relative to the shared margin.
func TestSelectionE2EMultiMessageDragExtractsCleanContent(t *testing.T) {
	terminal := newTrackingTerminal(40, 12)
	ui := NewTUI(terminal)
	body := &mutableLines{lines: []string{
		"\x1b[35m┃\x1b[0m  \x1b[45mHow do I sort a map?\x1b[0m      ",
		"                                    ", // user band padding row
		"                                    ", // gap between messages
		"   Use sorted keys.",
		"     keys := maps.Keys(m)", // deeper indent is content, kept relative
	}}
	editor := NewEditor(ui, EditorTheme{})
	chrome := &Container{}
	chrome.AddChild(editor)
	ui.SetViewport(body, chrome)
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	ui.RenderNow()

	terminal.deliver(sgr(0, 0, 0, false))   // press on the user band's gutter bar
	terminal.deliver(sgr(32, 30, 4, false)) // drag across both messages
	terminal.deliver(sgr(0, 30, 4, true))

	want := "How do I sort a map?\n\nUse sorted keys.\n  keys := maps.Keys(m)"
	select {
	case text := <-copied:
		if text != want {
			t.Fatalf("multi-message copy = %q, want %q", text, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("multi-message drag did not copy")
	}
}
