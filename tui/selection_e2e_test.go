package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSelectionCopiesLogicalLines(t *testing.T) {
	for _, test := range []struct {
		name, text, want string
		markdown         bool
	}{
		{name: "words", text: "alpha beta gamma delta epsilon"},
		{name: "spacing", text: "alpha   beta gamma    delta"},
		{name: "long-word", text: "https://example.com/averylongpathwithoutspaces"},
		{name: "wide", text: "你好世界你好世界 👩‍💻 café"},
		{name: "newlines", text: "alpha beta gamma\nsecond line\n\nnew paragraph"},
		{name: "styled", text: "\x1b[31malpha beta gamma delta\x1b[0m", want: "alpha beta gamma delta"},
		{name: "paragraph", text: "alpha **beta** gamma delta\n\nnext paragraph", want: "alpha beta gamma delta\n\nnext paragraph", markdown: true},
		{name: "list", text: "- alpha beta gamma delta\n- second item", want: "- alpha beta gamma delta\n- second item", markdown: true},
		{name: "code", text: "```\nalpha beta gamma delta\n  indented line\n```", want: "alpha beta gamma delta\n  indented line", markdown: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			want := test.want
			if want == "" {
				want = test.text
			}
			for _, width := range []int{12, 20, 80} {
				var body Component = NewText(test.text, 2, 1, nil)
				if test.markdown {
					body = NewMarkdown(test.text, 2, 1, MarkdownTheme{CodeBlockIndent: "\x1b[0m"}, nil, nil)
				}
				ui := NewTUI(newFakeTerminal(width, 40))
				ui.SetViewport(body, &Container{})
				frame := ui.renderViewport(width, 40)
				start, end := mousePoint{}, mousePoint{row: ui.viewportBodyLines - 1, column: width - 1}
				for range 2 {
					ui.selection = mouseSelection{anchor: start, focus: end, active: true, moved: true}
					if got := ui.selectedTextLocked(); got != want {
						t.Fatalf("width %d: got %q, want %q, rows %#v", width, got, want, body.Render(width))
					}
					for _, line := range ui.renderSelection(frame) {
						if _, highlight, ok := strings.Cut(line, "\x1b[7m"); ok {
							highlight, _, _ = strings.Cut(highlight, segmentReset)
							if strings.TrimSpace(highlight) != highlight {
								t.Fatalf("highlight includes padding: %q", highlight)
							}
						}
					}
					start, end = end, start
				}
				for _, line := range applyLineResets(append([]string(nil), frame...)) {
					if strings.Contains(line, softWrapMarker) {
						t.Fatal("wrap metadata escaped into terminal output")
					}
				}
			}
		})
	}
}

func TestSelectionHighlightPreservesWideCellsAndChrome(t *testing.T) {
	ui := NewTUI(newFakeTerminal(24, 8))
	zones := "\x1b]133;A\a\x1b]133;B\a\x1b]133;C\a"
	ui.SetViewport(NewText(zones+"hello 你好 👩‍💻 café\nsecond row", 2, 1, nil), NewText("INPUT DRAFT", 0, 0, nil))
	ui.SetSelectionStyle(func(text string) string { return "\x1b[48;2;230;228;215m" + text + "\x1b[49m" })
	frame := ui.renderViewport(24, 8)
	for anchor := range 24 {
		for focus := range 24 {
			ui.selection = mouseSelection{anchor: mousePoint{row: 1, column: anchor}, focus: mousePoint{row: 1, column: focus}, active: true, moved: true}
			painted := ui.renderSelection(frame)
			for row, line := range painted {
				if plainTerminalText(line) != plainTerminalText(frame[row]) || VisibleWidth(line) != VisibleWidth(frame[row]) {
					t.Fatalf("selection %d..%d altered row %d: %q -> %q", anchor, focus, row, frame[row], line)
				}
				if strings.Count(line, zones) != strings.Count(frame[row], zones) {
					t.Fatal("selection duplicated or dropped user-message terminal controls")
				}
			}
		}
	}
}

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
func TestSelectionE2EChromeDragSelectsWhatIsDrawn(t *testing.T) {
	fixture := newSelectionFixture(t)
	height := fixture.bodyHeight()

	// A click on chrome copies nothing: only a drag selects.
	fixture.terminal.deliver(sgr(0, 3, height, false))
	fixture.terminal.deliver(sgr(0, 3, height, true))
	select {
	case text := <-fixture.copied:
		t.Fatalf("a chrome click copied %q", text)
	default:
	}
	// A drag across the editor's rows copies the cells as drawn there, not transcript text.
	fixture.ui.RenderNow()
	fixture.ui.renderMu.Lock()
	want := strings.TrimRight(plainTerminalText(SliceByColumn(fixture.ui.frame[height], 3, 3, false)), " ")
	fixture.ui.renderMu.Unlock()
	fixture.terminal.deliver(sgr(0, 3, height, false))
	fixture.terminal.deliver(sgr(32, 5, height, false))
	fixture.terminal.deliver(sgr(0, 5, height, true))
	select {
	case text := <-fixture.copied:
		if text != want {
			t.Fatalf("chrome drag copied %q, want %q", text, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("chrome drag copied nothing")
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

func TestSelectionE2EDragInsideADialogCopiesTheDialog(t *testing.T) {
	fixture := newSelectionFixture(t)
	fixture.ui.ShowOverlay(&overlayLines{lines: []string{"https://example.test/login"}}, OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(30)})
	fixture.ui.RenderNow()
	fixture.terminal.deliver(sgr(0, 0, 0, false))
	fixture.terminal.deliver(sgr(32, 25, 0, false))
	fixture.terminal.deliver(sgr(0, 25, 0, true))
	select {
	case text := <-fixture.copied:
		if text != "https://example.test/login" {
			t.Fatalf("copied %q, want the dialog's link", text)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a drag in a dialog copied nothing")
	}
}

// dragCopy drags from one screen cell to another and returns the copy and
// the highlighted cells as painted, in order.
func dragCopy(t *testing.T, terminal *trackingTerminal, ui *TUI, copied chan string, from, to mousePoint) (text, highlighted string) {
	t.Helper()
	terminal.deliver(sgr(0, from.column, from.row, false))
	terminal.deliver(sgr(32, to.column, to.row, false))
	terminal.deliver(sgr(0, to.column, to.row, true))
	select {
	case text = <-copied:
	case <-time.After(2 * time.Second):
		t.Fatal("the drag copied nothing")
	}
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	for _, line := range ui.renderSelection(ui.frame) {
		for _, run := range strings.Split(line, "\x1b[7m")[1:] {
			run, _, _ = strings.Cut(run, segmentReset)
			highlighted += plainTerminalText(run)
		}
	}
	return text, highlighted
}

const wrappedLoginURL = "https://auth.example.test/oauth/authorize?client_id=orb&state=abc123xyz"

// A drag over a link the dialog wrapped over three lines copies the link and
// highlights it alone: no border glyphs, padding or wrap breaks, from any
// start cell inside the dialog.
func TestSelectionE2EDragInADialogCopiesItsWrappedContent(t *testing.T) {
	terminal := newTrackingTerminal(60, 16)
	ui := NewTUI(terminal)
	ui.SetViewport(&mutableLines{lines: []string{"transcript"}}, &Container{})
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	frame := NewFrame("", "", nil, nil, NewText(wrappedLoginURL+"\nnext line", 1, 0, nil))
	ui.ShowOverlay(frame, OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(30)})
	ui.RenderNow()
	ui.renderMu.Lock()
	rows := ui.frame[1:5]
	ui.renderMu.Unlock()
	if plain := plainTerminalText(rows[2]); !strings.HasPrefix(plain, "│ ") || !strings.Contains(plain, " │") || len(rows) != 4 {
		t.Fatalf("fixture is not a bordered, padded dialog wrapping the link over three rows: %q", rows)
	}
	for _, from := range []mousePoint{{row: 1, column: 0}, {row: 1, column: 3}} {
		text, highlighted := dragCopy(t, terminal, ui, copied, from, mousePoint{row: 3, column: 29})
		if text != wrappedLoginURL || highlighted != wrappedLoginURL {
			t.Fatalf("from %v: copied %q, highlighted %q, want the link alone", from, text, highlighted)
		}
	}
	// A real line break stays one.
	if text, _ := dragCopy(t, terminal, ui, copied, mousePoint{row: 3, column: 3}, mousePoint{row: 4, column: 29}); text != wrappedLoginURL[48:]+"\nnext line" {
		t.Fatalf("copied %q across a real line break", text)
	}
}

// The login dialog's layout, rules around padded text in the editor area,
// copies its wrapped link exactly.
func TestSelectionE2EDragInChromeJoinsSoftWraps(t *testing.T) {
	terminal := newTrackingTerminal(30, 12)
	ui := NewTUI(terminal)
	chrome := &Container{}
	for _, child := range []Component{&mutableLines{lines: []string{strings.Repeat("─", 30)}}, NewText("Login to Example", 1, 0, nil), NewText(wrappedLoginURL, 1, 0, nil), NewText("Click to copy", 1, 0, nil)} {
		chrome.AddChild(child)
	}
	ui.SetViewport(&mutableLines{lines: []string{"transcript"}}, chrome)
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	ui.RenderNow()
	ui.renderMu.Lock()
	top := len(ui.frame) - 6
	ui.renderMu.Unlock()
	text, highlighted := dragCopy(t, terminal, ui, copied, mousePoint{row: top + 2, column: 0}, mousePoint{row: top + 4, column: 29})
	if text != wrappedLoginURL || highlighted != wrappedLoginURL {
		t.Fatalf("copied %q, highlighted %q, want the link alone", text, highlighted)
	}
}

// A drag over a list item in a dialog selects its label, though the list
// takes presses; a click without motion still picks the item.
func TestSelectionE2EDragOverAListSelectsTextAndAClickPicks(t *testing.T) {
	terminal := newTrackingTerminal(60, 16)
	ui := NewTUI(terminal)
	ui.SetViewport(&mutableLines{lines: []string{"transcript"}}, &Container{})
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	list := NewSelectList([]SelectItem{{Value: "a", Label: "alpha model"}, {Value: "b", Label: "beta model"}}, 5, testSelectTheme, SelectListLayoutOptions{})
	picked := make(chan string, 2)
	list.OnSelect = func(item SelectItem) { picked <- item.Value }
	ui.ShowOverlay(NewPanel("Pick a model", "", nil, nil, nil, list), OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(40)})
	ui.RenderNow()
	ui.renderMu.Lock()
	row, column := -1, -1
	for index, line := range ui.frame {
		if at := strings.Index(plainTerminalText(line), "beta model"); at >= 0 {
			row, column = index, at
		}
	}
	ui.renderMu.Unlock()
	if row < 0 {
		t.Fatal("the list item is not on screen")
	}
	text, highlighted := dragCopy(t, terminal, ui, copied, mousePoint{row: row, column: column}, mousePoint{row: row, column: 39})
	if text != "beta model" || highlighted != "beta model" {
		t.Fatalf("copied %q, highlighted %q, want the label alone", text, highlighted)
	}
	select {
	case value := <-picked:
		t.Fatalf("a drag picked %q", value)
	default:
	}
	terminal.deliver(sgr(0, column, row, false))
	terminal.deliver(sgr(0, column, row, true))
	select {
	case value := <-picked:
		if value != "b" {
			t.Fatalf("a click picked %q", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a click did not pick the item")
	}
}
