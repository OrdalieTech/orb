package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeTerminal struct {
	mu               sync.Mutex
	columns, rows    int
	writes           []string
	onInput          func(string)
	onResize         func()
	started, stopped bool
	hidden           bool
}

func newFakeTerminal(columns, rows int) *fakeTerminal {
	return &fakeTerminal{columns: columns, rows: rows}
}
func (terminal *fakeTerminal) Start(input func(string), resize func()) error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.onInput, terminal.onResize, terminal.started = input, resize, true
	return nil
}
func (terminal *fakeTerminal) Stop() error {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	terminal.stopped = true
	return nil
}
func (terminal *fakeTerminal) DrainInput(time.Duration, time.Duration) {}
func (terminal *fakeTerminal) Write(data string) {
	terminal.mu.Lock()
	terminal.writes = append(terminal.writes, data)
	terminal.mu.Unlock()
}
func (terminal *fakeTerminal) Columns() int {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.columns
}
func (terminal *fakeTerminal) Rows() int {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.rows
}
func (terminal *fakeTerminal) KittyProtocolActive() bool { return false }
func (terminal *fakeTerminal) MoveBy(lines int) {
	if lines > 0 {
		terminal.Write("DOWN")
	} else if lines < 0 {
		terminal.Write("UP")
	}
}
func (terminal *fakeTerminal) HideCursor() {
	terminal.mu.Lock()
	terminal.hidden = true
	terminal.mu.Unlock()
}
func (terminal *fakeTerminal) ShowCursor() {
	terminal.mu.Lock()
	terminal.hidden = false
	terminal.mu.Unlock()
}
func (terminal *fakeTerminal) ClearLine()              { terminal.Write("CLEAR_LINE") }
func (terminal *fakeTerminal) ClearFromCursor()        { terminal.Write("CLEAR_REST") }
func (terminal *fakeTerminal) ClearScreen()            { terminal.Write("CLEAR_SCREEN") }
func (terminal *fakeTerminal) SetTitle(title string)   { terminal.Write(title) }
func (terminal *fakeTerminal) SetProgress(active bool) {}
func (terminal *fakeTerminal) output() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return strings.Join(terminal.writes, "")
}
func (terminal *fakeTerminal) resetOutput() {
	terminal.mu.Lock()
	terminal.writes = nil
	terminal.mu.Unlock()
}
func (terminal *fakeTerminal) send(data string) {
	terminal.mu.Lock()
	input := terminal.onInput
	terminal.mu.Unlock()
	input(data)
}

type mutableLines struct{ lines []string }

func (component *mutableLines) Render(int) []string { return component.lines }

type windowedLines struct {
	lines       []string
	fullRenders int
}

func (component *windowedLines) Render(int) []string {
	component.fullRenders++
	return component.lines
}

func (component *windowedLines) LineCount(int) int { return len(component.lines) }

func (component *windowedLines) RenderLines(_ int, start, end int) []string {
	return component.lines[start:end]
}

func TestTUIViewportDoesNotRenderHiddenHistory(t *testing.T) {
	body := &windowedLines{lines: []string{"one", "two", "three", "four", "five"}}
	ui := NewTUI(newFakeTerminal(20, 4))
	ui.SetViewport(body, &mutableLines{lines: []string{"input"}})

	if got := ui.renderViewport(20, 4); strings.Join(got, ",") != "three,four,five"+scrollbarThumb+",input" {
		t.Fatalf("viewport = %#v", got)
	}
	if body.fullRenders != 0 {
		t.Fatalf("full history rendered %d times", body.fullRenders)
	}
}

type scrollTrackingTerminal struct {
	*fakeTerminal
	mu             sync.Mutex
	scrollOnOutput bool
	userScrolled   bool
}

func newScrollTrackingTerminal(columns, rows int) *scrollTrackingTerminal {
	return &scrollTrackingTerminal{fakeTerminal: newFakeTerminal(columns, rows), scrollOnOutput: true}
}

func (terminal *scrollTrackingTerminal) Write(data string) {
	terminal.mu.Lock()
	if strings.Contains(data, scrollOnOutputOff) {
		terminal.scrollOnOutput = false
	}
	if terminal.userScrolled && strings.Contains(data, "\x1b[2J\x1b[H\x1b[3J") {
		terminal.userScrolled = false
	}
	if terminal.userScrolled && terminal.scrollOnOutput && data != "" {
		terminal.userScrolled = false
	}
	if strings.Contains(data, scrollOnOutputOn) {
		terminal.scrollOnOutput = true
	}
	terminal.mu.Unlock()
	terminal.fakeTerminal.Write(data)
}

type invalidationRequester struct{ ui RenderRequester }

func (*invalidationRequester) Render(int) []string { return nil }
func (component *invalidationRequester) Invalidate() {
	component.ui.RequestRender()
}

func TestTUIInvalidationMayRequestRender(t *testing.T) {
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.AddChild(&invalidationRequester{ui: ui})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		ui.Invalidate()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Invalidate deadlocked when the component requested a render")
	}
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
}

type recordingLines struct {
	lines []string
	width int
}

func (component *recordingLines) Render(width int) []string {
	component.width = width
	return component.lines
}

func TestTUIOverlayCompositionMatchesUpstreamLayoutAndStacking(t *testing.T) {
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.AddChild(&mutableLines{lines: []string{"base"}})
	first := &recordingLines{lines: []string{"FIRST-OVERLAY"}}
	ui.AddOverlay(first, func(int, int) OverlayLayout {
		return OverlayLayout{Width: 12, Anchor: "top-left"}
	})
	second := ui.AddOverlay(&mutableLines{lines: []string{"SECOND"}}, func(int, int) OverlayLayout {
		return OverlayLayout{Width: 6, Anchor: "top-left"}
	})
	ui.AddOverlay(&mutableLines{lines: []string{"BOTTOM", "hidden"}}, func(int, int) OverlayLayout {
		return OverlayLayout{Width: 8, MaxHeight: 1, Anchor: "bottom-right"}
	})

	lines := ui.renderWithOverlays(20, 6)
	if len(lines) != 6 {
		t.Fatalf("overlay buffer height = %d, want 6: %#v", len(lines), lines)
	}
	if first.width != 12 {
		t.Fatalf("overlay render width = %d, want 12", first.width)
	}
	if want := segmentReset + "SECOND" + segmentReset + "OVERLA" + segmentReset + "        "; lines[0] != want {
		t.Fatalf("stacked top-left overlay = %q, want %q", lines[0], want)
	}
	if want := "            " + segmentReset + "BOTTOM  " + segmentReset; lines[5] != want {
		t.Fatalf("bottom-right overlay = %q, want %q", lines[5], want)
	}
	if strings.Contains(strings.Join(lines, "\n"), "hidden") {
		t.Fatalf("max-height overlay leaked truncated row: %#v", lines)
	}

	second.SetHidden(true)
	lines = ui.renderWithOverlays(20, 6)
	if want := segmentReset + "FIRST-OVERLA" + segmentReset + "        "; lines[0] != want {
		t.Fatalf("hidden top overlay = %q, want %q", lines[0], want)
	}
	second.Remove()
	if second.IsHidden() != true {
		t.Fatal("removed overlay changed its explicit hidden state")
	}
}

func TestTUIDifferentialRenderingAndResize(t *testing.T) {
	terminal := newFakeTerminal(40, 10)
	ui := NewTUI(terminal)
	component := &mutableLines{lines: []string{"Header", "Working 1", "Footer"}}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	if output := terminal.output(); !strings.Contains(output, "Header") || !strings.Contains(output, "Footer") || strings.Contains(output, "\x1b[2J") {
		t.Fatalf("first render = %q", output)
	}
	terminal.resetOutput()
	component.lines[1] = "Working 2"
	ui.RenderNow()
	output := terminal.output()
	if !strings.Contains(output, "Working 2") || strings.Contains(output, "Header") || strings.Contains(output, "Footer") || strings.Contains(output, "\x1b[2J") {
		t.Fatalf("differential render = %q", output)
	}
	terminal.resetOutput()
	terminal.mu.Lock()
	terminal.columns = 50
	terminal.mu.Unlock()
	ui.RenderNow()
	if !strings.Contains(terminal.output(), "\x1b[2J\x1b[H\x1b[3J") {
		t.Fatalf("resize did not full redraw: %q", terminal.output())
	}
}

func TestTUIStreamingRenderPreservesTerminalScrollbackPosition(t *testing.T) {
	terminal := newScrollTrackingTerminal(40, 10)
	ui := NewTUI(terminal)
	component := &mutableLines{lines: []string{"Header", "Loading 1", "Footer"}}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}

	terminal.mu.Lock()
	terminal.userScrolled = true
	terminal.mu.Unlock()
	component.lines[1] = "Loading 2"
	ui.RenderNow()

	terminal.mu.Lock()
	stillScrolled := terminal.userScrolled
	terminal.mu.Unlock()
	if !stillScrolled {
		t.Fatal("streaming render forced terminal scrollback back to the active cursor")
	}
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	terminal.mu.Lock()
	scrollRestored := terminal.scrollOnOutput
	terminal.mu.Unlock()
	if !scrollRestored {
		t.Fatal("stopping the TUI did not restore terminal scroll-on-output mode")
	}
}

func TestTUIViewportPinsChromeAndKeepsDetachedBodyStable(t *testing.T) {
	terminal := newFakeTerminal(20, 6)
	ui := NewTUI(terminal)
	body := &mutableLines{lines: []string{"body 0", "body 1", "body 2", "body 3", "body 4", "body 5"}}
	chrome := &mutableLines{lines: []string{"editor", "footer"}}
	ui.AddChild(body)
	ui.AddChild(chrome)
	ui.SetViewport(body, chrome)
	body.lines = []string{"short body"}
	if frame := ui.renderViewport(20, 6); frame[0] != "short body" || frame[3] != "" || frame[4] != "editor" {
		t.Fatalf("short viewport is not top-aligned: %#v", frame)
	}
	body.lines = []string{"body 0", "body 1", "body 2", "body 3", "body 4", "body 5"}
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}

	initial := terminal.output()
	if !strings.Contains(initial, "\x1b[?1049h") || strings.Contains(initial, "body 1") || !strings.Contains(initial, "body 2") || strings.Index(initial, "body 5") > strings.Index(initial, "editor") || strings.Index(initial, "editor") > strings.Index(initial, "footer") {
		t.Fatalf("initial viewport = %q", initial)
	}

	terminal.send("\x1b[<64;10;3M") // SGR wheel up
	ui.RenderNow()
	detached := append([]string(nil), ui.previousLines...)
	if joined := strings.Join(detached, "\n"); !strings.Contains(joined, "body 0") || strings.Contains(joined, "body 5") || !strings.HasSuffix(joined, "editor"+segmentReset+"\nfooter"+segmentReset) {
		t.Fatalf("detached viewport = %q", joined)
	}

	terminal.resetOutput()
	body.lines = append(body.lines, "loading frame")
	chrome.lines[0] = "loader frame"
	ui.RenderNow()
	if got := ui.previousLines; !equalLines(got[:4], detached[:4]) {
		t.Fatalf("streaming moved detached body:\n before=%q\n  after=%q", detached[:4], got[:4])
	}
	if output := terminal.output(); !strings.Contains(output, "loader frame") || strings.Contains(output, "loading frame") || strings.Contains(output, "\x1b[2J") {
		t.Fatalf("detached streaming output = %q", output)
	}

	terminal.send("\x1b[6;5~") // ctrl+PageDown
	ui.RenderNow()
	terminal.send("\x1b[5;5~") // ctrl+PageUp
	ui.RenderNow()
	terminal.send("\x1b[1;5F") // ctrl+End
	ui.RenderNow()
	if joined := strings.Join(ui.previousLines, "\n"); !strings.Contains(joined, "loading frame") || !strings.HasSuffix(joined, "loader frame"+segmentReset+"\nfooter"+segmentReset) {
		t.Fatalf("follow viewport = %q", joined)
	}
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if output := terminal.output(); !strings.Contains(output, alternateScreenOff) {
		t.Fatalf("stop did not restore mouse and alternate-screen modes: %q", output)
	}
}

func TestTUIViewportNeverScrollsAboveFirstFullPage(t *testing.T) {
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.viewportBodyLines, ui.viewportBodyHeight, ui.viewportFollow = 10, 4, true
	ui.scrollViewportLocked(-100)
	if ui.viewportEnd != 4 || ui.viewportFollow {
		t.Fatalf("long history scroll = end %d follow %v, want 4 false", ui.viewportEnd, ui.viewportFollow)
	}
	ui.viewportBodyLines, ui.viewportBodyHeight, ui.viewportFollow = 3, 4, true
	ui.scrollViewportLocked(-3)
	if ui.viewportEnd != 3 || !ui.viewportFollow {
		t.Fatalf("short history scroll = end %d follow %v, want 3 true", ui.viewportEnd, ui.viewportFollow)
	}
}

func TestTUIViewportScrollbarClick(t *testing.T) {
	body := &mutableLines{}
	for index := range 100 {
		body.lines = append(body.lines, fmt.Sprintf("line %d", index))
	}
	ui := NewTUI(newFakeTerminal(10, 6))
	ui.SetViewport(body, &mutableLines{lines: []string{"input"}})

	frame := ui.renderViewport(10, 6)
	if strings.HasSuffix(frame[0], "┃") || !strings.HasSuffix(frame[4], "┃") {
		t.Fatalf("scrollbar = %#v", frame[:5])
	}
	if !ui.handleViewportInput("\x1b[<0;10;1M") || ui.viewportEnd != 5 || ui.viewportFollow {
		t.Fatalf("top click = end %d follow %v", ui.viewportEnd, ui.viewportFollow)
	}
	if !ui.handleViewportInput("\x1b[<0;10;5M") || ui.viewportEnd != 100 || !ui.viewportFollow {
		t.Fatalf("bottom click = end %d follow %v", ui.viewportEnd, ui.viewportFollow)
	}
}

func TestTUIViewportDragCopiesVisibleText(t *testing.T) {
	terminal := newFakeTerminal(12, 4)
	ui := NewTUI(terminal)
	body := &mutableLines{lines: []string{"old 0", "old 1", "alpha beta", "gamma delta", "third"}}
	ui.SetViewport(
		body,
		&mutableLines{lines: []string{"input"}},
	)
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()

	terminal.send("\x1b[<0;1;1M")
	terminal.send("\x1b[<32;5;2M")
	body.lines = append(body.lines, "streamed")
	ui.RenderNow()
	if frame := strings.Join(ui.previousLines, "\n"); !strings.Contains(frame, "\x1b[7m") || strings.Contains(frame, "streamed") {
		t.Fatalf("selection did not remain highlighted over a stable viewport: %q", frame)
	}
	terminal.send("\x1b[<0;5;2m")
	ui.RenderNow()
	if frame := strings.Join(ui.previousLines, "\n"); strings.Contains(frame, "\x1b[7m") {
		t.Fatalf("selection highlight remained after release: %q", frame)
	}

	select {
	case got := <-copied:
		if got != "alpha beta\ngamma" {
			t.Fatalf("selection = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("mouse drag did not copy the visible selection")
	}
	if output := terminal.output(); !strings.Contains(output, "\x1b[?1002h") {
		t.Fatalf("button-motion tracking was not enabled: %q", output)
	}
}

func TestTUISelectionPreservesWideStyledText(t *testing.T) {
	ui := NewTUI(newFakeTerminal(10, 2))
	ui.previousLines = []string{"\x1b[31mA界B\x1b[0m" + scrollbarThumb}
	ui.selection = mouseSelection{
		anchor: mousePoint{row: 0, column: 3},
		focus:  mousePoint{row: 0, column: 2},
		active: true,
		moved:  true,
	}
	if got := ui.selectedTextLocked(); got != "界B" {
		t.Fatalf("wide styled selection = %q", got)
	}
}

func TestTUIViewportDefersDirtyHiddenTailWhileDetached(t *testing.T) {
	body := NewWindowedContainer()
	children := make([]*countedLines, 100)
	for index := range children {
		children[index] = &countedLines{lines: []string{"line"}}
		body.AddChild(children[index])
	}
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.SetViewport(body, &mutableLines{lines: []string{"input"}})
	_ = ui.renderViewport(20, 6)
	ui.viewportFollow, ui.viewportEnd = false, 10
	children[99].lines = []string{"changed", "extra"}
	body.ChildChanged(children[99])
	_ = ui.renderViewport(20, 6)
	if children[99].renders != 1 {
		t.Fatalf("hidden dirty tail rendered while detached: %d", children[99].renders)
	}
	ui.viewportFollow = true
	_ = ui.renderViewport(20, 6)
	if children[99].renders != 2 {
		t.Fatalf("dirty tail renders after follow = %d, want 2", children[99].renders)
	}
}

func TestTUIClearOnShrinkPreservesTerminalScrollbackPosition(t *testing.T) {
	terminal := newScrollTrackingTerminal(40, 3)
	ui := NewTUI(terminal)
	ui.SetClearOnShrink(true)
	component := &mutableLines{lines: []string{"Header", "One", "Two", "Loading", "Footer"}}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()

	terminal.mu.Lock()
	terminal.userScrolled = true
	terminal.mu.Unlock()
	terminal.resetOutput()
	component.lines = []string{"Header", "One", "Loading", "Footer"}
	ui.RenderNow()

	terminal.mu.Lock()
	stillScrolled := terminal.userScrolled
	terminal.mu.Unlock()
	if !stillScrolled {
		t.Fatal("clear-on-shrink forced terminal scrollback back to the active cursor")
	}
	if output := terminal.output(); strings.Contains(output, "\x1b[2J\x1b[H\x1b[3J") {
		t.Fatalf("clear-on-shrink used a destructive full-screen redraw: %q", output)
	} else if !strings.Contains(output, "\x1b[2K") {
		t.Fatalf("clear-on-shrink did not erase the vacated row: %q", output)
	}
}

func TestTUIHardwareCursorMarkerAndReleaseFiltering(t *testing.T) {
	terminal := newFakeTerminal(20, 5)
	ui := NewTUI(terminal)
	input := &focusRecorder{}
	ui.AddChild(input)
	ui.SetFocus(input)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	output := terminal.output()
	if strings.Contains(output, CursorMarker) || !strings.Contains(output, "\x1b[3G") {
		t.Fatalf("cursor output = %q", output)
	}
	terminal.send("a")
	terminal.send("\x1b[97;1:3u")
	if len(input.events) != 1 || input.events[0].Key != "a" {
		t.Fatalf("events = %#v", input.events)
	}
	if !input.focused {
		t.Fatal("focus state was not propagated")
	}
}

func TestTUIStopClearsInvertedCursor(t *testing.T) {
	terminal := newFakeTerminal(20, 5)
	ui := NewTUI(terminal)
	editor := NewEditor(ui, EditorTheme{})
	ui.AddChild(editor)
	ui.SetFocus(editor)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	terminal.resetOutput()
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if output := terminal.output(); !strings.HasPrefix(output, " ") {
		t.Fatalf("stop output = %q, want cursor-clearing space first", output)
	}
}

type focusRecorder struct {
	focused bool
	events  []KeyEvent
}

func (recorder *focusRecorder) Render(int) []string {
	marker := ""
	if recorder.focused {
		marker = CursorMarker
	}
	return []string{"ab" + marker + "c"}
}
func (recorder *focusRecorder) HandleInput(event KeyEvent) {
	recorder.events = append(recorder.events, event)
}
func (recorder *focusRecorder) SetFocused(focused bool) { recorder.focused = focused }

func TestTUITenThousandLineReplayStaysDifferential(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	lines := make([]string, 10_000)
	for index := range lines {
		lines[index] = "session line"
	}
	component := &mutableLines{lines: lines}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	redraws := ui.FullRedraws()
	terminal.resetOutput()
	component.lines[len(component.lines)-1] = "session changed"
	started := time.Now()
	ui.RenderNow()
	elapsed := time.Since(started)
	output := terminal.output()
	if ui.FullRedraws() != redraws || strings.Contains(output, "\x1b[2J") {
		t.Fatalf("tail update used full redraw: %q", output)
	}
	if len(output) > 256 {
		t.Fatalf("tail update wrote %d bytes", len(output))
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("tail update took %s", elapsed)
	}
	t.Logf("10k-line tail update: %s, %d terminal bytes", elapsed, len(output))
}

func TestTUIStopsTerminalBeforeLineOverflowPanic(t *testing.T) {
	terminal := newFakeTerminal(3, 3)
	ui := NewTUI(terminal)
	ui.AddChild(&mutableLines{lines: []string{"toolong"}})
	ui.setStopped(false)
	ui.renderMu.Lock()
	ui.previousLines = []string{"old"}
	ui.previousWidth, ui.previousHeight = 3, 3
	ui.renderMu.Unlock()
	defer func() {
		if recover() == nil {
			t.Fatal("expected line overflow panic")
		}
		if !terminal.stopped {
			t.Fatal("terminal was not restored before panic")
		}
	}()
	ui.RenderNow()
}

func TestTUIImageCellQueryAndReservedRows(t *testing.T) {
	SetCapabilities(TerminalCapabilities{Images: ImageProtocolKitty})
	SetCellDimensions(CellDimensions{WidthPx: 9, HeightPx: 18})
	t.Cleanup(func() {
		ResetCapabilitiesCache()
		SetCellDimensions(CellDimensions{WidthPx: 9, HeightPx: 18})
	})
	terminal := newFakeTerminal(40, 10)
	ui := NewTUI(terminal)
	component := &mutableLines{lines: []string{EncodeKitty("QUJD", 8, 3, 27, false), "", ""}}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	output := terminal.output()
	if !strings.Contains(output, "\x1b[16t") || !strings.Contains(output, "\r\n\r\n\x1b[2A\x1b_G") || strings.Contains(output, segmentReset) {
		t.Fatalf("initial image output = %q", output)
	}
	terminal.send("\x1b[6;22;11t")
	if got := GetCellDimensions(); got != (CellDimensions{WidthPx: 11, HeightPx: 22}) {
		t.Fatalf("cell dimensions = %#v", got)
	}
	terminal.resetOutput()
	component.lines = []string{"replacement"}
	ui.RenderNow()
	if output := terminal.output(); !strings.Contains(output, DeleteKittyImage(27)) {
		t.Fatalf("changed image was not deleted: %q", output)
	}
}

func TestTUIExpandsAppendedKittyContinuationBeforeChoosingAppendMode(t *testing.T) {
	terminal := newFakeTerminal(40, 10)
	ui := NewTUI(terminal)
	imageLine := EncodeKitty("QUJD", 8, 3, 27, false)
	component := &mutableLines{lines: []string{imageLine, ""}}
	ui.AddChild(component)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	terminal.resetOutput()
	component.lines = append(component.lines, "")
	ui.RenderNow()
	output := terminal.output()
	if !strings.Contains(output, DeleteKittyImage(27)) || !strings.Contains(output, "\r\x1b[2K") {
		t.Fatalf("expanded image repaint = %q", output)
	}
	if strings.Contains(output, "\x1b[1A\r\n") {
		t.Fatalf("expanded image repaint incorrectly used append mode: %q", output)
	}
}
