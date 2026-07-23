package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	minRenderInterval  = 16 * time.Millisecond
	segmentReset       = "\x1b[0m\x1b]8;;\x07"
	scrollbarThumb     = segmentReset + "\x1b[999C┃"
	scrollOnOutputOff  = "\x1b[?1010l"
	scrollOnOutputOn   = "\x1b[?1010h"
	alternateScreenOn  = "\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	alternateScreenOff = "\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?1049l"
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

type mousePoint struct{ row, column int }
type mouseSelection struct {
	anchor, focus mousePoint
	active, moved bool
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
	previousImageIDs    []uint32
	clearOnShrink       bool
	showHardwareCursor  bool
	viewportBody        Component
	viewportChrome      Component
	viewportEnd         int
	viewportBodyLines   int
	viewportBodyHeight  int
	viewportFollow      bool
	selection           mouseSelection
	selectionHandler    func(string)

	lifecycleMu sync.RWMutex
	stopped     bool
	hasStarted  bool

	focusMu      sync.RWMutex
	focused      Component
	listeners    []inputListenerEntry
	nextListener uint64
	OnDebug      func()

	focusOrderCounter   uint64
	overlayStack        []*overlayStackEntry
	overlayFocusRestore overlayFocusRestoreState

	colorMu                                 sync.Mutex
	pendingOsc11BackgroundReplies           int
	pendingOsc11BackgroundQueries           []*pendingOsc11BackgroundQuery
	terminalColorSchemeListeners            []terminalColorSchemeListenerEntry
	nextTerminalColorSchemeListener         uint64
	terminalColorSchemeNotificationsEnabled bool
	notificationMu                          sync.Mutex

	scheduleMu       sync.Mutex
	renderDispatchMu sync.Mutex
	renderRequested  bool
	renderTimer      *time.Timer
	renderGeneration uint64
	lastRender       time.Time
}

func NewTUI(terminal Terminal) *TUI {
	return &TUI{terminal: terminal, clearOnShrink: os.Getenv("PI_CLEAR_ON_SHRINK") == "1", showHardwareCursor: os.Getenv("PI_HARDWARE_CURSOR") == "1", stopped: true}
}

func (ui *TUI) setStopped(stopped bool) {
	ui.lifecycleMu.Lock()
	ui.stopped = stopped
	ui.lifecycleMu.Unlock()
}

func (ui *TUI) isStopped() bool {
	ui.lifecycleMu.RLock()
	defer ui.lifecycleMu.RUnlock()
	return ui.stopped
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

// SetViewport keeps chrome pinned below a scrollable body. It is opt-in so
// embedders retain upstream's inline renderer unless they explicitly own the screen.
func (ui *TUI) SetViewport(body, chrome Component) {
	ui.renderMu.Lock()
	ui.viewportBody, ui.viewportChrome, ui.viewportFollow = body, chrome, true
	ui.renderMu.Unlock()
}

func (ui *TUI) SetSelectionHandler(handler func(string)) {
	ui.renderMu.Lock()
	ui.selectionHandler = handler
	ui.renderMu.Unlock()
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
	ui.setStopped(false)
	if err := ui.terminal.Start(ui.handleInput, ui.RequestRender); err != nil {
		ui.setStopped(true)
		return err
	}
	ui.lifecycleMu.Lock()
	ui.hasStarted = true
	ui.lifecycleMu.Unlock()
	ui.renderMu.Lock()
	viewport := ui.viewportBody != nil
	ui.renderMu.Unlock()
	if viewport {
		ui.terminal.Write(alternateScreenOn)
	}
	// Keep terminal scrollback stationary while live output updates the active cursor.
	ui.terminal.Write(scrollOnOutputOff)
	ui.terminal.HideCursor()
	ui.notificationMu.Lock()
	ui.colorMu.Lock()
	notificationsEnabled := ui.terminalColorSchemeNotificationsEnabled
	ui.colorMu.Unlock()
	if notificationsEnabled {
		ui.terminal.Write(terminalColorSchemeNotificationsOn)
	}
	ui.notificationMu.Unlock()
	if GetCapabilities().Images != "" {
		ui.terminal.Write("\x1b[16t")
	}
	ui.RenderNow()
	return nil
}

func (ui *TUI) Stop() error {
	ui.setStopped(true)
	ui.renderDispatchMu.Lock()
	ui.scheduleMu.Lock()
	ui.renderGeneration++
	if ui.renderTimer != nil {
		ui.renderTimer.Stop()
		ui.renderTimer = nil
	}
	ui.renderRequested = false
	ui.scheduleMu.Unlock()
	ui.renderDispatchMu.Unlock()
	ui.renderMu.Lock()
	lines, row, viewport := len(ui.previousLines), ui.hardwareCursorRow, ui.viewportBody != nil
	ui.renderMu.Unlock()
	ui.notificationMu.Lock()
	ui.colorMu.Lock()
	notificationsEnabled := ui.terminalColorSchemeNotificationsEnabled
	ui.colorMu.Unlock()
	if notificationsEnabled {
		ui.terminal.Write(terminalColorSchemeNotificationsOff)
	}
	ui.notificationMu.Unlock()
	if lines > 0 && !viewport {
		ui.terminal.Write(" ")
		target := lines
		if difference := target - row; difference > 0 {
			ui.terminal.MoveBy(difference)
		} else if difference < 0 {
			ui.terminal.MoveBy(difference)
		}
		ui.terminal.Write("\r\n")
	}
	ui.terminal.ShowCursor()
	ui.terminal.Write(scrollOnOutputOn)
	if viewport {
		ui.terminal.Write(alternateScreenOff)
	}
	return ui.terminal.Stop()
}

func (ui *TUI) Invalidate() {
	ui.renderMu.Lock()
	ui.Container.Invalidate()
	ui.focusMu.RLock()
	overlays := append([]*overlayStackEntry(nil), ui.overlayStack...)
	ui.focusMu.RUnlock()
	for _, overlay := range overlays {
		invalidate(overlay.component)
	}
	ui.renderMu.Unlock()
	ui.RequestRender()
}

func (ui *TUI) RequestRender() {
	if ui.isStopped() {
		return
	}
	ui.scheduleMu.Lock()
	if ui.renderRequested {
		ui.scheduleMu.Unlock()
		return
	}
	ui.renderRequested = true
	ui.renderGeneration++
	generation := ui.renderGeneration
	delay := max(time.Duration(0), minRenderInterval-time.Since(ui.lastRender))
	ui.renderTimer = time.AfterFunc(delay, func() {
		ui.renderDispatchMu.Lock()
		defer ui.renderDispatchMu.Unlock()
		ui.scheduleMu.Lock()
		if generation != ui.renderGeneration || !ui.renderRequested {
			ui.scheduleMu.Unlock()
			return
		}
		ui.renderRequested, ui.renderTimer, ui.lastRender = false, nil, time.Now()
		ui.scheduleMu.Unlock()
		ui.RenderNow()
	})
	ui.scheduleMu.Unlock()
}

func (ui *TUI) ForceRender() {
	ui.renderDispatchMu.Lock()
	defer ui.renderDispatchMu.Unlock()
	ui.scheduleMu.Lock()
	ui.renderGeneration++
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
	if ui.consumeOsc11BackgroundResponse(data) {
		return
	}
	if ui.consumeTerminalColorSchemeReport(data) {
		return
	}
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
	if height, width, ok := parseCellSizeResponse(data); ok {
		if height > 0 && width > 0 {
			SetCellDimensions(CellDimensions{WidthPx: width, HeightPx: height})
			ui.Invalidate()
		}
		return
	}
	if MatchesKey(data, "shift+ctrl+d") && ui.OnDebug != nil {
		ui.OnDebug()
		return
	}
	if ui.handleViewportInput(data) {
		ui.RequestRender()
		return
	}
	ui.focusMu.Lock()
	if focusedOverlay := ui.overlayForComponentLocked(ui.focused); focusedOverlay != nil && !ui.isOverlayVisibleLocked(focusedOverlay) {
		if top := ui.topmostVisibleOverlayLocked(); top != nil {
			ui.setFocusLocked(top.component, overlayFocusRestoreClear)
		} else {
			ui.setFocusLocked(focusedOverlay.preFocus, overlayFocusRestorePreserve)
		}
	}
	if ui.overlayForComponentLocked(ui.focused) == nil {
		restoreState := ui.visibleOverlayFocusRestoreLocked()
		if restoreState.status == overlayFocusRestoreEligible {
			ui.setFocusLocked(restoreState.overlay.component, overlayFocusRestoreClear)
		} else if restoreState.status == overlayFocusRestoreBlocked && restoreState.blockedBy != ui.focused {
			if restoreState.resume.kind == overlayFocusResumeOverlay {
				ui.setFocusLocked(restoreState.overlay.component, overlayFocusRestoreClear)
			} else {
				ui.clearOverlayFocusRestoreLocked()
				ui.setFocusLocked(restoreState.resume.target, overlayFocusRestoreClear)
			}
		}
	}
	focused := ui.focused
	ui.focusMu.Unlock()
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

func (ui *TUI) handleViewportInput(data string) bool {
	ui.renderMu.Lock()
	if ui.viewportBody == nil {
		ui.renderMu.Unlock()
		return false
	}
	var selected string
	consumed := true
	step := max(1, ui.viewportBodyHeight)
	switch {
	case MatchesKey(data, "ctrl+pageup"):
		ui.selection.active = false
		ui.scrollViewportLocked(-step)
	case MatchesKey(data, "ctrl+pagedown"):
		ui.selection.active = false
		ui.scrollViewportLocked(step)
	case MatchesKey(data, "ctrl+end"):
		ui.selection.active = false
		ui.viewportFollow = true
	case strings.HasPrefix(data, "\x1b[<") && (strings.HasSuffix(data, "M") || strings.HasSuffix(data, "m")):
		var button, column, row int
		if _, err := fmt.Sscanf(data[:len(data)-1], "\x1b[<%d;%d;%d", &button, &column, &row); err != nil {
			break
		}
		switch {
		case button&64 != 0 && button&3 < 2:
			ui.selection.active = false
			if button&1 == 0 {
				ui.scrollViewportLocked(-3)
			} else {
				ui.scrollViewportLocked(3)
			}
		case data[len(data)-1] == 'm' && ui.selection.active:
			if point, ok := ui.mousePointLocked(column, row); ok {
				ui.selection.focus = point
				ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
			}
			if ui.selection.moved {
				selected = ui.selectedTextLocked()
			}
			ui.selection.active = false
		case button&32 != 0 && ui.selection.active:
			if point, ok := ui.mousePointLocked(column, row); ok {
				ui.selection.focus = point
				ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
			}
		case data[len(data)-1] == 'M' && button&3 == 0 && column == ui.terminal.Columns() && row > 0 && row <= ui.viewportBodyHeight:
			ui.selection.active = false
			ui.scrollViewportToLocked(row - 1)
		case data[len(data)-1] == 'M' && button&3 == 0 && ui.selectionHandler != nil:
			if point, ok := ui.mousePointLocked(column, row); ok {
				ui.selection = mouseSelection{anchor: point, focus: point, active: true}
				if point.row < ui.viewportBodyHeight && ui.viewportFollow && ui.viewportBodyLines > ui.viewportBodyHeight {
					ui.viewportEnd, ui.viewportFollow = ui.viewportBodyLines, false
				}
			}
		}
	default:
		consumed = false
	}
	handler := ui.selectionHandler
	ui.renderMu.Unlock()
	if selected != "" && handler != nil {
		handler(selected)
	}
	return consumed
}

func (ui *TUI) mousePointLocked(column, row int) (mousePoint, bool) {
	if column < 1 || row < 1 || row > len(ui.previousLines) {
		return mousePoint{}, false
	}
	return mousePoint{row: row - 1, column: min(column-1, max(0, ui.terminal.Columns()-1))}, true
}

func (selection mouseSelection) bounds() (mousePoint, mousePoint) {
	start, end := selection.anchor, selection.focus
	if end.row < start.row || end.row == start.row && end.column < start.column {
		start, end = end, start
	}
	return start, end
}

func selectionColumns(row int, start, end mousePoint, width int) (int, int) {
	from, to := 0, width
	if row == start.row {
		from = start.column
	}
	if row == end.row {
		to = end.column + 1
	}
	return min(from, width), min(to, width)
}

func selectionColumnStart(line string, column int) int {
	current := 0
	for pos := 0; pos < len(line) && current < column; {
		if _, next, ok := extractANSI(line, pos); ok {
			pos = next
			continue
		}
		end := pos
		for end < len(line) {
			if _, _, ok := extractANSI(line, end); ok {
				break
			}
			_, size := utf8.DecodeRuneInString(line[end:])
			end += size
		}
		found := false
		forEachGrapheme(line[pos:end], func(grapheme string) bool {
			width := graphemeWidth(grapheme)
			if current < column && column < current+width {
				column, found = current, true
				return false
			}
			current += width
			return current < column
		})
		if found {
			return column
		}
		pos = end
	}
	return column
}

func plainTerminalText(text string) string {
	text = NormalizeTerminalOutput(text)
	var result strings.Builder
	for pos := 0; pos < len(text); {
		if _, next, ok := extractANSI(text, pos); ok {
			pos = next
			continue
		}
		_, size := utf8.DecodeRuneInString(text[pos:])
		result.WriteString(text[pos : pos+size])
		pos += size
	}
	return result.String()
}

func (ui *TUI) selectedTextLocked() string {
	start, end := ui.selection.bounds()
	end.row = min(end.row, len(ui.previousLines)-1)
	lines := make([]string, 0, end.row-start.row+1)
	for row := start.row; row <= end.row; row++ {
		line := strings.Replace(ui.previousLines[row], scrollbarThumb, "", 1)
		width := VisibleWidth(line)
		from, to := selectionColumns(row, start, end, width)
		from = selectionColumnStart(line, from)
		lines = append(lines, plainTerminalText(SliceByColumn(line, from, max(0, to-from), false)))
	}
	return strings.Join(lines, "\n")
}

func (ui *TUI) scrollViewportToLocked(row int) {
	scrollable := ui.viewportBodyLines - ui.viewportBodyHeight
	if scrollable <= 0 {
		return
	}
	row = max(0, min(row, ui.viewportBodyHeight-1))
	ui.viewportEnd = ui.viewportBodyHeight + row*scrollable/max(1, ui.viewportBodyHeight-1)
	ui.viewportFollow = ui.viewportEnd == ui.viewportBodyLines
}

func (ui *TUI) scrollViewportLocked(delta int) {
	end := ui.viewportEnd
	if ui.viewportFollow {
		end = ui.viewportBodyLines
	}
	end = max(min(ui.viewportBodyHeight, ui.viewportBodyLines), min(ui.viewportBodyLines, end+delta))
	ui.viewportEnd, ui.viewportFollow = end, end == ui.viewportBodyLines
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
		if !IsImageLine(line) {
			lines[index] = NormalizeTerminalOutput(line) + segmentReset
		}
	}
	return lines
}

func parseCellSizeResponse(data string) (height, width int, ok bool) {
	if !strings.HasPrefix(data, "\x1b[6;") || !strings.HasSuffix(data, "t") {
		return 0, 0, false
	}
	parts := strings.Split(data[len("\x1b[6;"):len(data)-1], ";")
	if len(parts) != 2 {
		return 0, 0, false
	}
	height, heightErr := strconv.Atoi(parts[0])
	width, widthErr := strconv.Atoi(parts[1])
	return height, width, heightErr == nil && widthErr == nil
}

func collectKittyImageIDs(lines []string) []uint32 {
	ids := make([]uint32, 0)
	seen := make(map[uint32]struct{})
	for _, line := range lines {
		lineIDs, _ := parseKittyImageHeader(line)
		for _, id := range lineIDs {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func kittyImageReservedRows(lines []string, index, maxIndex int) int {
	_, rows := parseKittyImageHeader(lines[index])
	if rows <= 1 {
		return 1
	}
	maxRows := min(rows, maxIndex-index+1, len(lines)-index)
	reserved := 1
	for reserved < maxRows {
		line := lines[index+reserved]
		if IsImageLine(line) || VisibleWidth(line) > 0 {
			break
		}
		reserved++
	}
	return reserved
}

func deleteKittyImages(ids []uint32) string {
	var output strings.Builder
	for _, id := range ids {
		output.WriteString(DeleteKittyImage(id))
	}
	return output.String()
}

func changedKittyImageIDs(lines []string, first, last int) []uint32 {
	ids := make([]uint32, 0)
	seen := make(map[uint32]struct{})
	last = min(last, len(lines)-1)
	for index := max(0, first); index <= last; index++ {
		lineIDs, _ := parseKittyImageHeader(lines[index])
		for _, id := range lineIDs {
			if _, exists := seen[id]; !exists {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func expandChangedRangeForKittyImages(first, last int, previous, next []string) (int, int) {
	expandedFirst, expandedLast := first, last
	for _, lines := range [][]string{previous, next} {
		for index, line := range lines {
			ids, _ := parseKittyImageHeader(line)
			if len(ids) == 0 {
				continue
			}
			blockEnd := index + kittyImageReservedRows(lines, index, len(lines)-1) - 1
			if index >= first || index <= last && blockEnd >= first {
				expandedFirst = min(expandedFirst, index)
				expandedLast = max(expandedLast, blockEnd)
			}
		}
	}
	return expandedFirst, expandedLast
}

func (ui *TUI) RenderNow() {
	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	if ui.isStopped() {
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
	newLines := ui.renderViewport(width, height)
	if ui.overlayCount() > 0 {
		newLines = ui.compositeOverlays(newLines, width, height)
	}
	newLines = ui.renderSelection(newLines)
	cursorRow, cursorColumn, hasCursor := ui.extractCursor(newLines, height)
	newLines = applyLineResets(newLines)
	fullRender := func(clear bool) {
		ui.fullRedraws++
		var output strings.Builder
		output.WriteString("\x1b[?2026h")
		if clear {
			output.WriteString(deleteKittyImages(ui.previousImageIDs))
			output.WriteString("\x1b[2J\x1b[H\x1b[3J")
		}
		for index := 0; index < len(newLines); index++ {
			if index > 0 {
				output.WriteString("\r\n")
			}
			line := newLines[index]
			reserved := 1
			if IsImageLine(line) {
				reserved = kittyImageReservedRows(newLines, index, len(newLines)-1)
			}
			if reserved > 1 && reserved <= height {
				output.WriteString(strings.Repeat("\r\n", reserved-1))
				fmt.Fprintf(&output, "\x1b[%dA", reserved-1)
				output.WriteString(line)
				fmt.Fprintf(&output, "\x1b[%dB", reserved-1)
				index += reserved - 1
				continue
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
		ui.previousImageIDs = collectKittyImageIDs(newLines)
	}
	if len(ui.previousLines) == 0 && !widthChanged && !heightChanged {
		fullRender(false)
		return
	}
	if widthChanged || (heightChanged && os.Getenv("TERMUX_VERSION") == "") {
		fullRender(true)
		return
	}
	clearShrunkRows := ui.clearOnShrink && len(newLines) < ui.maxLinesRendered && ui.overlayCount() == 0
	settleRenderedHeight := func() {
		if clearShrunkRows {
			ui.maxLinesRendered = len(newLines)
		} else {
			ui.maxLinesRendered = max(ui.maxLinesRendered, len(newLines))
		}
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
	if firstChanged < 0 {
		ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
		ui.previousViewportTop, ui.previousHeight = previousViewportTop, height
		settleRenderedHeight()
		return
	}
	firstChanged, lastChanged = expandChangedRangeForKittyImages(firstChanged, lastChanged, ui.previousLines, newLines)
	appendStart := appended && firstChanged == len(ui.previousLines) && firstChanged > 0
	if firstChanged >= len(newLines) {
		if len(ui.previousLines) > len(newLines) {
			target := max(0, len(newLines)-1)
			if target < previousViewportTop {
				fullRender(true)
				return
			}
			var output strings.Builder
			output.WriteString("\x1b[?2026h")
			output.WriteString(deleteKittyImages(changedKittyImageIDs(ui.previousLines, firstChanged, lastChanged)))
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
		ui.previousImageIDs = collectKittyImageIDs(newLines)
		settleRenderedHeight()
		return
	}
	if firstChanged < previousViewportTop {
		fullRender(true)
		return
	}
	var output strings.Builder
	output.WriteString("\x1b[?2026h")
	output.WriteString(deleteKittyImages(changedKittyImageIDs(ui.previousLines, firstChanged, lastChanged)))
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
		line := newLines[index]
		reserved := 1
		if IsImageLine(line) {
			reserved = kittyImageReservedRows(newLines, index, renderEnd)
		}
		if reserved > 1 {
			imageStartScreenRow := index - viewportTop
			if imageStartScreenRow < 0 || imageStartScreenRow+reserved > height {
				fullRender(true)
				return
			}
			output.WriteString("\x1b[2K")
			for range reserved - 1 {
				output.WriteString("\r\n\x1b[2K")
			}
			fmt.Fprintf(&output, "\x1b[%dA", reserved-1)
			output.WriteString(line)
			fmt.Fprintf(&output, "\x1b[%dB", reserved-1)
			index += reserved - 1
			continue
		}
		output.WriteString("\x1b[2K")
		if !IsImageLine(line) && VisibleWidth(line) > width {
			ui.setStopped(true)
			ui.terminal.ShowCursor()
			_ = ui.terminal.Stop()
			panic(fmt.Sprintf("rendered line %d exceeds terminal width (%d > %d)", index, VisibleWidth(newLines[index]), width))
		}
		output.WriteString(line)
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
	settleRenderedHeight()
	ui.previousViewportTop = max(previousViewportTop, finalCursorRow-height+1)
	ui.positionCursor(cursorRow, cursorColumn, hasCursor, len(newLines))
	ui.previousLines, ui.previousWidth, ui.previousHeight = newLines, width, height
	ui.previousImageIDs = collectKittyImageIDs(newLines)
}

func (ui *TUI) renderViewport(width, height int) []string {
	if ui.viewportBody == nil {
		return append([]string(nil), ui.Render(width)...)
	}
	chrome := ui.viewportChrome.Render(width)
	if len(chrome) > height {
		chrome = chrome[len(chrome)-height:]
	}
	bodyHeight := height - len(chrome)
	bodyWidth := width
	if width > 1 {
		bodyWidth--
	}
	body := buildLineLayout(ui.viewportBody, bodyWidth)
	bodyLines := body.total
	end := bodyLines
	if !ui.viewportFollow {
		end = min(ui.viewportEnd, bodyLines)
	}
	start := max(0, end-bodyHeight)
	body.refreshRange(bodyWidth, start, end, ui.viewportFollow)
	bodyLines = body.total
	ui.viewportBodyLines, ui.viewportBodyHeight = bodyLines, bodyHeight
	end = bodyLines
	if !ui.viewportFollow {
		end = min(ui.viewportEnd, bodyLines)
		ui.viewportEnd = end
	}
	start = max(0, end-bodyHeight)
	lines := make([]string, 0, height)
	lines = body.appendRange(lines, bodyWidth, start, end)
	lines = append(lines, make([]string, bodyHeight-len(lines))...)
	if top, size := scrollbar(bodyLines, bodyHeight, end); width > 1 && size > 0 {
		for row := top; row < top+size; row++ {
			if IsImageLine(lines[row]) {
				continue
			}
			lines[row] += scrollbarThumb
		}
	}
	return append(lines, chrome...)
}

func scrollbar(total, height, end int) (top, size int) {
	if height <= 0 || total <= height {
		return 0, 0
	}
	size = max(1, height*height/total)
	start := max(0, min(total-height, end-height))
	top = start * (height - size) / (total - height)
	return top, size
}

func (ui *TUI) renderSelection(lines []string) []string {
	if !ui.selection.active || !ui.selection.moved {
		return lines
	}
	start, end := ui.selection.bounds()
	end.row = min(end.row, len(lines)-1)
	result := append([]string(nil), lines...)
	for row := start.row; row <= end.row; row++ {
		line := result[row]
		hasThumb := strings.Contains(line, scrollbarThumb)
		line = strings.Replace(line, scrollbarThumb, "", 1)
		width := VisibleWidth(line)
		from, to := selectionColumns(row, start, end, width)
		from = selectionColumnStart(line, from)
		if to > from && !IsImageLine(line) {
			before := SliceByColumn(line, 0, from, false)
			selected := plainTerminalText(SliceByColumn(line, from, to-from, false))
			after := SliceByColumn(line, to, width-to, false)
			line = before + segmentReset + "\x1b[7m" + selected + segmentReset + after
		}
		if hasThumb {
			line += scrollbarThumb
		}
		result[row] = line
	}
	return result
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
