package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// trackingTerminal emulates the xterm mouse-tracking state machine on top of
// the plain fakeTerminal: DECSET 1000/1002/1003 select ONE mutually
// exclusive tracking mode (they are not independent flags), and DECRST of
// any of them turns tracking off entirely. Reports are only delivered to
// the TUI when the current mode would actually send them, which is what
// made the real-terminal regression (bare 1003l killing all reporting)
// reproducible in-tree.
type trackingTerminal struct {
	*fakeTerminal
	tracking string // "off", "button", "drag", "any"
}

func newTrackingTerminal(columns, rows int) *trackingTerminal {
	return &trackingTerminal{fakeTerminal: newFakeTerminal(columns, rows), tracking: "off"}
}

var trackingTokens = []struct{ seq, mode string }{
	{"\x1b[?1000h", "button"},
	{"\x1b[?1002h", "drag"},
	{"\x1b[?1003h", "any"},
	{"\x1b[?1000l", "off"},
	{"\x1b[?1002l", "off"},
	{"\x1b[?1003l", "off"},
}

func (terminal *trackingTerminal) Write(data string) {
	terminal.fakeTerminal.Write(data)
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	for position := 0; position < len(data); {
		nextIndex, nextLen, nextMode := -1, 0, ""
		for _, token := range trackingTokens {
			if index := strings.Index(data[position:], token.seq); index >= 0 && (nextIndex < 0 || index < nextIndex) {
				nextIndex, nextLen, nextMode = index, len(token.seq), token.mode
			}
		}
		if nextIndex < 0 {
			break
		}
		terminal.tracking = nextMode
		position += nextIndex + nextLen
	}
}

func (terminal *trackingTerminal) trackingMode() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.tracking
}

// deliver sends a raw SGR report through the terminal input path exactly
// when the emulated tracking mode would report it, and returns whether the
// terminal sent anything at all.
func (terminal *trackingTerminal) deliver(report string) bool {
	event, ok := parseMouse(report)
	if !ok {
		terminal.send(report)
		return true
	}
	mode := terminal.trackingMode()
	sends := false
	switch event.Type {
	case MouseMove:
		sends = mode == "any"
	case MouseDrag:
		sends = mode == "drag" || mode == "any"
	default: // press, release, wheel: any active tracking mode reports them
		sends = mode != "off"
	}
	if sends {
		terminal.send(report)
	}
	return sends
}

// sgr encodes one SGR mouse report from zero-based screen coordinates.
func sgr(code, column, row int, release bool) string {
	suffix := "M"
	if release {
		suffix = "m"
	}
	return fmt.Sprintf("\x1b[<%d;%d;%d%s", code, column+1, row+1, suffix)
}

// mouseE2EFixture is a chat-shaped TUI: a tall transcript body and a
// bottom-anchored chrome holding an editor area that selectors swap into,
// exactly like the interactive mode's editorContainer.
type mouseE2EFixture struct {
	terminal   *trackingTerminal
	ui         *TUI
	body       *mutableLines
	editor     *Editor
	editorArea *Container
}

func newMouseE2EFixture(t *testing.T) *mouseE2EFixture {
	t.Helper()
	terminal := newTrackingTerminal(40, 12)
	ui := NewTUI(terminal)
	body := &mutableLines{}
	for index := range 100 {
		body.lines = append(body.lines, fmt.Sprintf("line %d", index))
	}
	editor := NewEditor(ui, EditorTheme{})
	editorArea := &Container{}
	editorArea.AddChild(editor)
	chrome := &Container{}
	chrome.AddChild(editorArea)
	ui.SetViewport(body, chrome)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ui.Stop() })
	ui.SetFocus(editor)
	ui.RenderNow()
	return &mouseE2EFixture{terminal: terminal, ui: ui, body: body, editor: editor, editorArea: editorArea}
}

func (fixture *mouseE2EFixture) following() bool {
	fixture.ui.renderMu.Lock()
	defer fixture.ui.renderMu.Unlock()
	return fixture.ui.viewportFollow
}

func (fixture *mouseE2EFixture) viewportEndNow() int {
	fixture.ui.renderMu.Lock()
	defer fixture.ui.renderMu.Unlock()
	if fixture.ui.viewportFollow {
		return fixture.ui.viewportBodyLines
	}
	return fixture.ui.viewportEnd
}

// showSelector swaps the selector into the editor area and focuses it, the
// way the interactive mode presents /model and friends.
func (fixture *mouseE2EFixture) showSelector(list *SelectList) {
	fixture.editorArea.Clear()
	fixture.editorArea.AddChild(list)
	fixture.ui.SetFocus(list)
	fixture.ui.RenderNow()
}

// closeSelector restores the editor, as closing any selector does.
func (fixture *mouseE2EFixture) closeSelector() {
	fixture.editorArea.Clear()
	fixture.editorArea.AddChild(fixture.editor)
	fixture.ui.SetFocus(fixture.editor)
	fixture.ui.RenderNow()
}

// selectorScreenRow maps a selector-local row to the zero-based screen row,
// mirroring the bottom-anchored chrome layout.
func (fixture *mouseE2EFixture) selectorScreenRow(list *SelectList, localRow int) int {
	chromeLines := len(list.Render(fixture.terminal.Columns()))
	return fixture.terminal.Rows() - chromeLines + localRow
}

func e2eSelector(items int, maxVisible int) *SelectList {
	values := make([]SelectItem, items)
	for index := range values {
		values[index] = SelectItem{Value: fmt.Sprintf("item-%02d", index)}
	}
	return NewSelectList(values, maxVisible, SelectListTheme{}, SelectListLayoutOptions{})
}

// TestMouseE2EWheelScrollsTranscriptAfterSelectorVisit is the regression
// gate for the dead-transcript-scroll bug: closing a hover-capable selector
// used to emit a bare 1003l, which xterm-family terminals treat as "mouse
// tracking off", so the terminal stopped reporting wheel, clicks, and
// scrollbar presses for the rest of the session.
func TestMouseE2EWheelScrollsTranscriptAfterSelectorVisit(t *testing.T) {
	fixture := newMouseE2EFixture(t)

	// Normal chat: wheel up over the transcript detaches follow and scrolls.
	if !fixture.terminal.deliver(sgr(64, 5, 2, false)) {
		t.Fatal("terminal did not report the wheel in normal chat state")
	}
	if fixture.following() {
		t.Fatal("wheel over the transcript did not scroll it")
	}
	fixture.ui.ScrollToBottom()

	// Visit a selector (any-motion tracking turns on) and close it again.
	list := e2eSelector(4, 4)
	fixture.showSelector(list)
	if got := fixture.terminal.trackingMode(); got != "any" {
		t.Fatalf("tracking while a selector holds focus = %q, want any", got)
	}
	fixture.closeSelector()
	if got := fixture.terminal.trackingMode(); got == "off" {
		t.Fatal("closing the selector turned ALL mouse tracking off (bare 1003l regression)")
	}

	// The wheel over the transcript must still scroll it.
	if !fixture.terminal.deliver(sgr(64, 5, 2, false)) {
		t.Fatal("terminal no longer reports wheel events after a selector visit")
	}
	if fixture.following() {
		t.Fatal("wheel over the transcript stopped scrolling after a selector visit")
	}
}

func TestMouseE2EScrollbarWorksAfterSelectorVisit(t *testing.T) {
	fixture := newMouseE2EFixture(t)
	list := e2eSelector(4, 4)
	fixture.showSelector(list)
	fixture.closeSelector()

	// Press on the scrollbar column inside the body area, drag, release.
	lastColumn := fixture.terminal.Columns() - 1
	if !fixture.terminal.deliver(sgr(0, lastColumn, 2, false)) {
		t.Fatal("terminal no longer reports scrollbar presses")
	}
	fixture.ui.renderMu.Lock()
	grabbed := fixture.ui.selection.scrollbar
	fixture.ui.renderMu.Unlock()
	if !grabbed {
		t.Fatal("scrollbar press was not captured")
	}
	before := fixture.viewportEndNow()
	if !fixture.terminal.deliver(sgr(32, lastColumn, 6, false)) {
		t.Fatal("terminal no longer reports scrollbar drags")
	}
	if fixture.viewportEndNow() <= before {
		t.Fatalf("scrollbar drag did not scroll: end %d -> %d", before, fixture.viewportEndNow())
	}
	fixture.terminal.deliver(sgr(0, lastColumn, 6, true))
	fixture.ui.renderMu.Lock()
	released := !fixture.ui.selection.scrollbar
	fixture.ui.renderMu.Unlock()
	if !released {
		t.Fatal("scrollbar release did not end the drag")
	}
}

func TestMouseE2EWheelInsideFocusedSelectorStepsSelection(t *testing.T) {
	fixture := newMouseE2EFixture(t)
	list := e2eSelector(6, 3)
	fixture.showSelector(list)
	fixture.ui.ScrollToBottom()

	row := fixture.selectorScreenRow(list, 0)
	if !fixture.terminal.deliver(sgr(65, 4, row, false)) {
		t.Fatal("terminal did not report the wheel over the selector")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "item-01" {
		t.Fatalf("selector wheel selected %q, want item-01", item.Value)
	}
	// The transcript did not scroll: the selector consumed the wheel.
	if !fixture.following() {
		t.Fatal("selector wheel leaked into the transcript")
	}
}

func TestMouseE2EClickAndDoubleClickThroughBytes(t *testing.T) {
	fixture := newMouseE2EFixture(t)
	list := e2eSelector(4, 4)
	confirmed := ""
	list.OnSelect = func(item SelectItem) { confirmed = item.Value }
	fixture.showSelector(list)

	row := fixture.selectorScreenRow(list, 2)
	fixture.terminal.deliver(sgr(0, 4, row, false))
	fixture.terminal.deliver(sgr(0, 4, row, true))
	if item, _ := list.GetSelectedItem(); item.Value != "item-02" || confirmed != "" {
		t.Fatalf("click selected %q confirmed %q, want item-02 and no confirm", item.Value, confirmed)
	}
	fixture.terminal.deliver(sgr(0, 4, row, false))
	if confirmed != "item-02" {
		t.Fatalf("double click confirmed %q, want item-02", confirmed)
	}
}

func TestMouseE2EHoverMovesSelectionWithoutWindowShift(t *testing.T) {
	fixture := newMouseE2EFixture(t)
	list := e2eSelector(6, 3)
	list.SetSelectedIndex(4)
	fixture.showSelector(list)

	before := selectListRows(list, fixture.terminal.Columns())
	row := fixture.selectorScreenRow(list, 0)
	if !fixture.terminal.deliver(sgr(35, 4, row, false)) {
		t.Fatal("terminal did not report hover while the selector holds focus")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "item-03" {
		t.Fatalf("hover selected %q, want item-03", item.Value)
	}
	fixture.ui.RenderNow()
	if after := selectListRows(list, fixture.terminal.Columns()); !slices.Equal(before, after) {
		t.Fatalf("hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}

	// After the selector closes, motion reports stop: hover bytes are not
	// even sent by the terminal, and tracking still reports button events.
	fixture.closeSelector()
	if fixture.terminal.deliver(sgr(35, 4, row, false)) {
		t.Fatal("terminal still reports motion after the selector closed")
	}
	if got := fixture.terminal.trackingMode(); got != "drag" {
		t.Fatalf("tracking after the selector closed = %q, want drag", got)
	}
}
