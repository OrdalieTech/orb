package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	minRenderInterval = 16 * time.Millisecond
	segmentReset      = "\x1b[0m\x1b]8;;\x07"
)

type InputListenerResult struct {
	Consume bool
	Data    *string
}

type InputListener func(string) InputListenerResult

type inputListenerEntry struct {
	id       uint64
	listener InputListener
}

// TUI owns focus and performs synchronized line-level differential rendering.
type TUI struct {
	Container
	terminal Terminal

	renderMu            sync.Mutex
	previousLines       []string
	previousWidth       int
	previousHeight      int
	cursorRow           int
	hardwareCursorRow   int
	maxLinesRendered    int
	previousViewportTop int
	fullRedraws         int
	clearOnShrink       bool
	showHardwareCursor  bool
	stopped             bool

	focusMu      sync.RWMutex
	focused      Component
	listeners    []inputListenerEntry
	nextListener uint64
	OnDebug      func()

	scheduleMu      sync.Mutex
	renderRequested bool
	renderTimer     *time.Timer
	lastRender      time.Time
}

func NewTUI(terminal Terminal) *TUI {
	return &TUI{terminal: terminal, clearOnShrink: os.Getenv("PI_CLEAR_ON_SHRINK") == "1", showHardwareCursor: os.Getenv("PI_HARDWARE_CURSOR") == "1", stopped: true}
}

func (ui *TUI) Terminal() Terminal { return ui.terminal }
func (ui *TUI) FullRedraws() int {
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	return ui.fullRedraws
}
func (ui *TUI) ClearOnShrink() bool {
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	return ui.clearOnShrink
}
func (ui *TUI) SetClearOnShrink(enabled bool) {
	ui.renderMu.Lock()
	ui.clearOnShrink = enabled
	ui.renderMu.Unlock()
}
func (ui *TUI) ShowHardwareCursor() bool {
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	return ui.showHardwareCursor
}
func (ui *TUI) SetShowHardwareCursor(enabled bool) {
	ui.renderMu.Lock()
	ui.showHardwareCursor = enabled
	ui.renderMu.Unlock()
	if !enabled {
		ui.terminal.HideCursor()
	}
	ui.RequestRender()
}

func (ui *TUI) SetFocus(component Component) {
	ui.focusMu.Lock()
	if previous, ok := ui.focused.(Focusable); ok {
		previous.SetFocused(false)
	}
	ui.focused = component
	if next, ok := component.(Focusable); ok {
		next.SetFocused(true)
	}
	ui.focusMu.Unlock()
}

func (ui *TUI) AddInputListener(listener InputListener) func() {
	ui.focusMu.Lock()
	ui.nextListener++
	id := ui.nextListener
	ui.listeners = append(ui.listeners, inputListenerEntry{id: id, listener: listener})
	ui.focusMu.Unlock()
	return func() {
		ui.focusMu.Lock()
		defer ui.focusMu.Unlock()
		for index, candidate := range ui.listeners {
			if candidate.id == id {
				ui.listeners = append(ui.listeners[:index], ui.listeners[index+1:]...)
				return
			}
		}
	}
}

func (ui *TUI) Start() error {
	ui.renderMu.Lock()
	ui.stopped = false
	ui.renderMu.Unlock()
	if err := ui.terminal.Start(ui.handleInput, ui.RequestRender); err != nil {
		ui.renderMu.Lock()
		ui.stopped = true
		ui.renderMu.Unlock()
		return err
	}
	ui.terminal.HideCursor()
	ui.RenderNow()
	return nil
}

func (ui *TUI) Stop() error {
	ui.scheduleMu.Lock()
	if ui.renderTimer != nil {
		ui.renderTimer.Stop()
		ui.renderTimer = nil
	}
	ui.renderRequested = false
	ui.scheduleMu.Unlock()
	ui.renderMu.Lock()
	ui.stopped = true
	lines, row := len(ui.previousLines), ui.hardwareCursorRow
	ui.renderMu.Unlock()
	if lines > 0 {
		target := lines
		if difference := target - row; difference > 0 {
			ui.terminal.MoveBy(difference)
		} else if difference < 0 {
			ui.terminal.MoveBy(difference)
		}
		ui.terminal.Write("\r\n")
	}
	ui.terminal.ShowCursor()
	return ui.terminal.Stop()
}

func (ui *TUI) Invalidate() { ui.Container.Invalidate(); ui.RequestRender() }

func (ui *TUI) RequestRender() {
	ui.renderMu.Lock()
	stopped := ui.stopped
	ui.renderMu.Unlock()
	if stopped {
		return
	}
	ui.scheduleMu.Lock()
	if ui.renderRequested {
		ui.scheduleMu.Unlock()
		return
	}
	ui.renderRequested = true
	delay := max(time.Duration(0), minRenderInterval-time.Since(ui.lastRender))
	ui.renderTimer = time.AfterFunc(delay, func() {
		ui.scheduleMu.Lock()
		ui.renderRequested, ui.renderTimer, ui.lastRender = false, nil, time.Now()
		ui.scheduleMu.Unlock()
		ui.RenderNow()
	})
	ui.scheduleMu.Unlock()
}

func (ui *TUI) ForceRender() {
	ui.scheduleMu.Lock()
	if ui.renderTimer != nil {
		ui.renderTimer.Stop()
		ui.renderTimer = nil
	}
	ui.renderRequested = false
	ui.lastRender = time.Now()
	ui.scheduleMu.Unlock()
	ui.renderMu.Lock()
	ui.previousLines, ui.previousWidth, ui.previousHeight = nil, -1, -1
	ui.cursorRow, ui.hardwareCursorRow, ui.maxLinesRendered, ui.previousViewportTop = 0, 0, 0, 0
	ui.renderMu.Unlock()
	ui.RenderNow()
}

func (ui *TUI) handleInput(data string) {
	ui.focusMu.RLock()
	entries := append([]inputListenerEntry(nil), ui.listeners...)
	ui.focusMu.RUnlock()
	for _, entry := range entries {
		result := entry.listener(data)
		if result.Consume {
			return
		}
		if result.Data != nil {
			data = *result.Data
		}
		if data == "" {
			return
		}
	}
	if MatchesKey(data, "shift+ctrl+d") && ui.OnDebug != nil {
		ui.OnDebug()
		return
	}
	ui.focusMu.RLock()
	focused := ui.focused
	ui.focusMu.RUnlock()
	handler, ok := focused.(InputHandler)
	if !ok {
		return
	}
	if IsKeyRelease(data) {
		if consumer, ok := focused.(KeyReleaseConsumer); !ok || !consumer.WantsKeyRelease() {
			return
		}
	}
	handler.HandleInput(KeyEvent{Raw: data, Key: ParseKey(data), Type: KeyEventTypeOf(data)})
	ui.RequestRender()
}

func (ui *TUI) extractCursor(lines []string, height int) (row, column int, found bool) {
	viewportTop := max(0, len(lines)-height)
	for row = len(lines) - 1; row >= viewportTop; row-- {
		if marker := strings.Index(lines[row], CursorMarker); marker >= 0 {
			column = VisibleWidth(lines[row][:marker])
			lines[row] = lines[row][:marker] + lines[row][marker+len(CursorMarker):]
			return row, column, true
		}
	}
	return 0, 0, false
}

func applyLineResets(lines []string) []string {
	for index, line := range lines {
		lines[index] = NormalizeTerminalOutput(line) + segmentReset
	}
	return lines
}

func (ui *TUI) RenderNow() {
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	if ui.stopped {
		return
	}
	width, height := ui.terminal.Columns(), ui.terminal.Rows()
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	widthChanged := ui.previousWidth != 0 && ui.previousWidth != width
	heightChanged := ui.previousHeight != 0 && ui.previousHeight != height
	previousBufferLength := height
	if ui.previousHeight > 0 {
		previousBufferLength = ui.previousViewportTop + ui.previousHeight
	}
	previousViewportTop := ui.previousViewportTop
	if heightChanged {
		previousViewportTop = max(0, previousBufferLength-height)
	}
	viewportTop, hardwareCursorRow := previousViewportTop, ui.hardwareCursorRow
	lineDifference := func(target int) int { return (target - viewportTop) - (hardwareCursorRow - previousViewportTop) }
	newLines := append([]string(nil), ui.Render(width)...)
	cursorRow, cursorColumn, hasCursor := ui.extractCursor(newLines, height)
	newLines = applyLineResets(newLines)
	fullRender := func(clear bool) {
		ui.fullRedraws++
		var output strings.Builder
		output.WriteString("\x1b[?2026h")
		if clear {
			output.WriteString("\x1b[2J\x1b[H\x1b[3J")
		}
		for index, line := range newLines {
			if index > 0 {
				output.WriteString("\r\n")
			}
			output.WriteString(line)
		}
		output.WriteString("\x1b[?2026l")
		ui.terminal.Write(output.String())
		ui.cursorRow, ui.hardwareCursorRow = max(0, len(newLines)-1), max(0, len(newLines)-1)
		if clear {
			ui.maxLinesRendered = len(newLines)
		} else {
			ui.maxLinesRendered = max(ui.maxLinesRendered, len(newLines))
		}
		ui.previousViewportTop = max(0, max(height, len(newLines))-height)
		ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
		ui.previousLines, ui.previousWidth, ui.previousHeight = newLines, width, height
	}
	if len(ui.previousLines) == 0 && !widthChanged && !heightChanged {
		fullRender(false)
		return
	}
	if widthChanged || (heightChanged && os.Getenv("TERMUX_VERSION") == "") {
		fullRender(true)
		return
	}
	if ui.clearOnShrink && len(newLines) < ui.maxLinesRendered {
		fullRender(true)
		return
	}

	firstChanged, lastChanged := -1, -1
	maxLines := max(len(newLines), len(ui.previousLines))
	for index := range maxLines {
		oldLine, newLine := "", ""
		if index < len(ui.previousLines) {
			oldLine = ui.previousLines[index]
		}
		if index < len(newLines) {
			newLine = newLines[index]
		}
		if oldLine != newLine {
			if firstChanged < 0 {
				firstChanged = index
			}
			lastChanged = index
		}
	}
	appended := len(newLines) > len(ui.previousLines)
	if appended {
		if firstChanged < 0 {
			firstChanged = len(ui.previousLines)
		}
		lastChanged = len(newLines) - 1
	}
	appendStart := appended && firstChanged == len(ui.previousLines) && firstChanged > 0
	if firstChanged < 0 {
		ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
		ui.previousViewportTop, ui.previousHeight = previousViewportTop, height
		return
	}
	if firstChanged >= len(newLines) {
		if len(ui.previousLines) > len(newLines) {
			target := max(0, len(newLines)-1)
			if target < previousViewportTop {
				fullRender(true)
				return
			}
			var output strings.Builder
			output.WriteString("\x1b[?2026h")
			difference := lineDifference(target)
			if difference > 0 {
				fmt.Fprintf(&output, "\x1b[%dB", difference)
			} else if difference < 0 {
				fmt.Fprintf(&output, "\x1b[%dA", -difference)
			}
			output.WriteByte('\r')
			extra, offset := len(ui.previousLines)-len(newLines), 0
			if len(newLines) > 0 {
				offset = 1
			}
			if extra > height {
				fullRender(true)
				return
			}
			if extra > 0 && offset > 0 {
				fmt.Fprintf(&output, "\x1b[%dB", offset)
			}
			for index := range extra {
				output.WriteString("\r\x1b[2K")
				if index < extra-1 {
					output.WriteString("\x1b[1B")
				}
			}
			if moveBack := max(0, extra-1+offset); moveBack > 0 {
				fmt.Fprintf(&output, "\x1b[%dA", moveBack)
			}
			output.WriteString("\x1b[?2026l")
			ui.terminal.Write(output.String())
			ui.cursorRow, ui.hardwareCursorRow = target, target
		}
		ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
		ui.previousLines, ui.previousWidth, ui.previousHeight, ui.previousViewportTop = newLines, width, height, previousViewportTop
		return
	}
	if firstChanged < previousViewportTop {
		fullRender(true)
		return
	}
	var output strings.Builder
	output.WriteString("\x1b[?2026h")
	previousViewportBottom := previousViewportTop + height - 1
	moveTarget := firstChanged
	if appendStart {
		moveTarget--
	}
	if moveTarget > previousViewportBottom {
		currentScreen := max(0, min(height-1, hardwareCursorRow-previousViewportTop))
		if down := height - 1 - currentScreen; down > 0 {
			fmt.Fprintf(&output, "\x1b[%dB", down)
		}
		scroll := moveTarget - previousViewportBottom
		output.WriteString(strings.Repeat("\r\n", scroll))
		previousViewportTop += scroll
		viewportTop += scroll
		hardwareCursorRow = moveTarget
	}
	difference := lineDifference(moveTarget)
	if difference > 0 {
		fmt.Fprintf(&output, "\x1b[%dB", difference)
	} else if difference < 0 {
		fmt.Fprintf(&output, "\x1b[%dA", -difference)
	}
	if appendStart {
		output.WriteString("\r\n")
	} else {
		output.WriteByte('\r')
	}
	renderEnd := min(lastChanged, len(newLines)-1)
	for index := firstChanged; index <= renderEnd; index++ {
		if index > firstChanged {
			output.WriteString("\r\n")
		}
		output.WriteString("\x1b[2K")
		if VisibleWidth(newLines[index]) > width {
			ui.terminal.ShowCursor()
			_ = ui.terminal.Stop()
			ui.stopped = true
			panic(fmt.Sprintf("rendered line %d exceeds terminal width (%d > %d)", index, VisibleWidth(newLines[index]), width))
		}
		output.WriteString(newLines[index])
	}
	finalCursorRow := renderEnd
	if len(ui.previousLines) > len(newLines) {
		if renderEnd < len(newLines)-1 {
			down := len(newLines) - 1 - renderEnd
			fmt.Fprintf(&output, "\x1b[%dB", down)
			finalCursorRow = len(newLines) - 1
		}
		extra := len(ui.previousLines) - len(newLines)
		for range extra {
			output.WriteString("\r\n\x1b[2K")
		}
		fmt.Fprintf(&output, "\x1b[%dA", extra)
	}
	output.WriteString("\x1b[?2026l")
	ui.terminal.Write(output.String())
	ui.cursorRow, ui.hardwareCursorRow = max(0, len(newLines)-1), finalCursorRow
	ui.maxLinesRendered = max(ui.maxLinesRendered, len(newLines))
	ui.previousViewportTop = max(previousViewportTop, finalCursorRow-height+1)
	ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
	ui.previousLines, ui.previousWidth, ui.previousHeight = newLines, width, height
}

func (ui *TUI) positionCursor(row, column int, found bool, totalLines int) {
	if !found || totalLines <= 0 {
		ui.terminal.HideCursor()
		return
	}
	row, column = max(0, min(row, totalLines-1)), max(0, column)
	delta := row - ui.hardwareCursorRow
	if delta != 0 {
		ui.terminal.MoveBy(delta)
	}
	ui.terminal.Write(fmt.Sprintf("\x1b[%dG", column+1))
	ui.hardwareCursorRow = row
	if ui.showHardwareCursor {
		ui.terminal.ShowCursor()
	} else {
		ui.terminal.HideCursor()
	}
}
