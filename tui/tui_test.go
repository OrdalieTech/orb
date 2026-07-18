package tui

import (
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
	ui.renderMu.Lock()
	ui.stopped = false
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
