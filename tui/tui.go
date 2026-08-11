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
	minRenderInterval   = 16 * time.Millisecond
	doubleClickInterval = 500 * time.Millisecond
	// selectionScrollInterval paces edge-drag auto-scroll ticks. Each tick
	// requests a render, so it stays a multiple of minRenderInterval and the
	// 16ms throttle coalesces ticks with streaming updates.
	selectionScrollInterval = 50 * time.Millisecond
	// selectionScrollMaxRows caps the per-tick scroll rate however far past
	// the viewport edge the pointer travels.
	selectionScrollMaxRows = 8
	segmentReset           = "\x1b[0m\x1b]8;;\x07"
	scrollbarThumb         = segmentReset + "\x1b[999C┃"
	scrollOnOutputOff      = "\x1b[?1010l"
	scrollOnOutputOn       = "\x1b[?1010h"
	alternateScreenOn      = "\x1b[?1049h\x1b[?1000h\x1b[?1002h\x1b[?1006h"
	// 1003 is focus-scoped (syncMouseMotionLocked); the off sequence always
	// clears it so a crash cannot leave the terminal streaming motion.
	alternateScreenOff = "\x1b[?1003l\x1b[?1006l\x1b[?1002l\x1b[?1000l\x1b[?1049l"
	mouseMotionOn      = "\x1b[?1003h"
	// Turning any-motion off must re-assert button-event tracking in the
	// same write: xterm-family terminals implement 1000/1002/1003 as ONE
	// mutually exclusive tracking mode, so a bare 1003l switches mouse
	// reporting off entirely — killing wheel, clicks, and the scrollbar for
	// the rest of the session — instead of falling back to 1002.
	mouseMotionOff = "\x1b[?1003l\x1b[?1002h"
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

// mouseSelection anchors text selection in transcript CONTENT coordinates:
// row is the line index inside the viewport body (not a screen row), column
// the cell inside that body line. Scrolling under a held selection therefore
// never moves the anchored text.
type mouseSelection struct {
	anchor, focus, lastClick mousePoint
	active, moved, sentence  bool
	scrollbar                bool
	lastClickAt              time.Time
}

// selectionAutoScroll drives the edge-drag auto-scroll: while a selection
// drag holds the pointer at (or past) the viewport's top or bottom edge, a
// single owned timer scrolls the viewport and extends the selection until
// the pointer re-enters the body or the drag ends. All fields are guarded by
// renderMu; generation invalidates in-flight timer callbacks.
type selectionAutoScroll struct {
	timer      *time.Timer
	generation uint64
	direction  int           // -1 scroll up, +1 scroll down, 0 idle
	overshoot  int           // rows the pointer sits past the edge (0 = at the edge)
	column     int           // content column the extending focus keeps while scrolling
	interval   time.Duration // test seam; zero means selectionScrollInterval
}

// TUI owns focus and performs synchronized line-level differential rendering.
type TUI struct {
	Container
	terminal Terminal

	renderMu            sync.Mutex
	previousLines       []string
	frameScratch        []string
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
	viewportBodyWidth   int
	viewportFollow      bool
	selection           mouseSelection
	selectionScroll     selectionAutoScroll
	selectionHandler    func(string)
	chromeTop           int
	chromeTrim          int
	mouseOverlays       []mouseOverlayBox
	mouseClick          mousePoint
	mouseClickAt        time.Time

	lifecycleMu        sync.RWMutex
	stopped            bool
	hasStarted         bool
	mouseViewport      bool
	crashRestoreCancel func()

	focusMu      sync.RWMutex
	focused      Component
	mouseMotion  bool
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
	ui.renderMu.Lock()
	viewport := ui.viewportBody != nil
	ui.renderMu.Unlock()
	if viewport {
		ui.terminal.Write(alternateScreenOn)
	}
	if err := ui.terminal.Start(ui.handleInput, ui.RequestRender); err != nil {
		if viewport {
			ui.terminal.Write(alternateScreenOff)
		}
		ui.setStopped(true)
		return err
	}
	ui.lifecycleMu.Lock()
	ui.hasStarted = true
	ui.mouseViewport = viewport
	ui.crashRestoreCancel = registerCrashRestore(func() {
		ui.setStopped(true)
		// stopTerminal ends in ProcessTerminal.Stop, which blocks on
		// terminal.mu and buffer.mu; the panicking goroutine may still hold
		// either (both are locked without defer), which would turn the crash
		// into a hang. Write the UI resets directly (no UI mutexes either)
		// and leave termios restore to the terminal's own crash restore,
		// calling Stop only for terminals without one.
		ui.terminal.Write(terminalColorSchemeNotificationsOff)
		ui.terminal.ShowCursor()
		ui.terminal.Write(scrollOnOutputOn)
		if viewport {
			ui.terminal.Write(alternateScreenOff)
		}
		if _, selfRestoring := ui.terminal.(interface{ crashRestoresSelf() }); !selfRestoring {
			_ = ui.terminal.Stop()
		}
	})
	ui.lifecycleMu.Unlock()
	// A restart (external editor, crash recovery) re-enters with a motion
	// component still focused; the off sequence cleared 1003, so re-assert it.
	ui.focusMu.RLock()
	motion := ui.mouseMotion
	ui.focusMu.RUnlock()
	if viewport && motion {
		ui.terminal.Write(mouseMotionOn)
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
	ui.lifecycleMu.Lock()
	crashRestoreCancel := ui.crashRestoreCancel
	ui.crashRestoreCancel = nil
	ui.lifecycleMu.Unlock()
	if crashRestoreCancel != nil {
		crashRestoreCancel()
	}
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
	ui.stopSelectionScrollLocked()
	lines, row, viewport := len(ui.previousLines), ui.hardwareCursorRow, ui.viewportBody != nil
	ui.renderMu.Unlock()
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
	return ui.stopTerminal(viewport)
}

func (ui *TUI) stopTerminal(viewport bool) error {
	ui.notificationMu.Lock()
	ui.colorMu.Lock()
	notificationsEnabled := ui.terminalColorSchemeNotificationsEnabled
	ui.colorMu.Unlock()
	if notificationsEnabled {
		ui.terminal.Write(terminalColorSchemeNotificationsOff)
	}
	ui.notificationMu.Unlock()
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
	ui.renderTimer = time.AfterFunc(delay, guarded(func() {
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
	}))
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
	if IsMouseReport(data) {
		ui.handleMouse(data)
		return true
	}
	ui.renderMu.Lock()
	if ui.viewportBody == nil {
		ui.renderMu.Unlock()
		return false
	}
	consumed := true
	step := max(1, ui.viewportBodyHeight)
	switch {
	case MatchesKey(data, "ctrl+pageup"):
		ui.clearSelectionLocked()
		ui.scrollViewportLocked(-step)
	case MatchesKey(data, "ctrl+pagedown"):
		ui.clearSelectionLocked()
		ui.scrollViewportLocked(step)
	case MatchesKey(data, "ctrl+end"):
		ui.clearSelectionLocked()
		ui.viewportFollow = true
	// Escape cancels only a live selection drag (and its auto-scroll timer);
	// with no selection active it falls through to the focused component, so
	// keyboard flows are otherwise unchanged.
	case MatchesKey(data, "escape") && ui.selection.active:
		ui.clearSelectionLocked()
	default:
		consumed = false
	}
	ui.renderMu.Unlock()
	return consumed
}

// syncMouseMotionLocked keeps any-motion tracking scoped to the focused
// component: 1003 floods reports, so it is on only while a selector that can
// use hover holds focus. Callers hold focusMu; the terminal write is safe
// there because Write takes only the terminal's own leaf mutex.
func (ui *TUI) syncMouseMotionLocked() {
	wants := false
	if handler, ok := ui.focused.(MouseMotionHandler); ok {
		wants = handler.WantsMouseMotion()
	}
	if wants == ui.mouseMotion {
		return
	}
	ui.mouseMotion = wants
	ui.lifecycleMu.RLock()
	live := !ui.stopped && ui.mouseViewport
	ui.lifecycleMu.RUnlock()
	if !live {
		return
	}
	if wants {
		ui.terminal.Write(mouseMotionOn)
	} else {
		ui.terminal.Write(mouseMotionOff)
	}
}

// SyncMouseMotion re-evaluates the focused component's WantsMouseMotion
// answer. Components whose answer changes while they keep focus — the
// editor's autocomplete popup — call it on every transition; focus changes
// resync on their own.
func (ui *TUI) SyncMouseMotion() {
	ui.focusMu.Lock()
	ui.syncMouseMotionLocked()
	ui.focusMu.Unlock()
}

// handleMouse is the single mouse routing path, strictly position-based:
// the event goes to the component under the cursor (topmost overlay first,
// then chrome; transcript rows have no component target), and only if that
// component declines does it fall back to the TUI-owned viewport — wheel
// scroll, scrollbar, and text selection. Focus never receives mouse events;
// it only gates any-motion (?1003) tracking. So a wheel over the transcript
// scrolls the transcript no matter what holds focus, and a wheel over a
// selector steps that selector. Dispatch runs outside renderMu because
// handlers re-enter the TUI to change focus or swap the component they live
// in.
func (ui *TUI) handleMouse(data string) {
	event, ok := parseMouse(data)
	if !ok {
		return
	}
	ui.renderMu.Lock()
	// A modified click is the escape hatch for terminals that report shift
	// instead of passing it through: it always reaches text selection.
	dispatch := !ui.selection.active && !ui.selection.scrollbar && !event.Shift && !event.Alt && !event.Ctrl
	local := event
	var handler MouseHandler
	if dispatch {
		if event.Type == MousePress {
			point := mousePoint{row: event.Row, column: event.Column}
			local.Clicks = 1
			if point == ui.mouseClick && time.Since(ui.mouseClickAt) <= doubleClickInterval {
				local.Clicks = 2
			}
			ui.mouseClick, ui.mouseClickAt = point, time.Now()
		}
		handler, local, dispatch = ui.mouseTargetLocked(local)
	}
	ui.renderMu.Unlock()
	if dispatch && handler.HandleMouse(local) {
		return
	}
	ui.handleViewportMouse(event)
}

// mouseTargetLocked resolves the component under the cursor. Transcript rows
// are excluded: nothing in the body handles mouse today and walking it would
// re-render every message.
// ponytail: chrome and overlays only; cache per-child offsets in Container if
// transcript hit-testing is ever needed.
func (ui *TUI) mouseTargetLocked(event MouseEvent) (MouseHandler, MouseEvent, bool) {
	// ponytail: only the alternate-screen viewport knows its screen origin, so
	// inline TUIs stay keyboard-only; query the cursor position on Start to
	// anchor them.
	if ui.viewportBody == nil {
		return nil, event, false
	}
	for index := len(ui.mouseOverlays) - 1; index >= 0; index-- {
		box := ui.mouseOverlays[index]
		if event.Row < box.row || event.Row >= box.row+box.height ||
			event.Column < box.col || event.Column >= box.col+box.width {
			continue
		}
		handler, row, ok := mouseTargetAt(box.component, box.width, event.Row-box.row)
		event.Row, event.Column = row, event.Column-box.col
		return handler, event, ok
	}
	if event.Row < ui.chromeTop {
		return nil, event, false
	}
	handler, row, ok := mouseTargetAt(ui.viewportChrome, max(1, ui.terminal.Columns()), event.Row-ui.chromeTop+ui.chromeTrim)
	event.Row = row
	return handler, event, ok
}

func (ui *TUI) handleViewportMouse(event MouseEvent) {
	ui.renderMu.Lock()
	if ui.viewportBody == nil {
		ui.renderMu.Unlock()
		return
	}
	var selected string
	switch {
	case event.Type == MouseRelease && ui.selection.scrollbar:
		ui.selection.scrollbar = false
	case event.Type == MouseDrag && ui.selection.scrollbar:
		ui.scrollViewportToLocked(event.Row)
	case event.Type == MouseRelease && ui.selection.active:
		ui.stopSelectionScrollLocked()
		if point, ok := ui.bodyPointLocked(event.Column, event.Row, true); ok && !ui.selection.sentence {
			ui.selection.focus = point
			ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
		}
		if ui.selection.moved {
			selected = ui.selectedTextLocked()
		}
		ui.selection.active = false
	case event.Type == MouseDrag && ui.selection.active:
		if point, ok := ui.bodyPointLocked(event.Column, event.Row, true); ok && !ui.selection.sentence {
			ui.selection.focus = point
			ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
		}
		ui.updateSelectionScrollLocked(event)
	case event.Type == MouseWheelUp || event.Type == MouseWheelDown:
		delta := 3
		if event.Type == MouseWheelUp {
			delta = -3
		}
		if ui.selection.active {
			// A wheel mid-drag scrolls explicitly: the edge timer yields, the
			// content-anchored selection stays put, and the focus keeps
			// tracking whatever content flows under the stationary pointer.
			ui.stopSelectionScrollLocked()
			ui.scrollViewportLocked(delta)
			if point, ok := ui.bodyPointLocked(event.Column, event.Row, true); ok && !ui.selection.sentence {
				ui.selection.focus = point
				ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
			}
		} else {
			ui.clearSelectionLocked()
			ui.scrollViewportLocked(delta)
		}
	case event.Type == MousePress && event.Button == 0 && event.Column == ui.terminal.Columns()-1 && event.Row < ui.viewportBodyHeight && ui.viewportBodyLines > ui.viewportBodyHeight:
		ui.stopSelectionScrollLocked()
		ui.selection = mouseSelection{scrollbar: true}
		ui.scrollViewportToLocked(event.Row)
	case event.Type == MousePress && event.Button == 0 && ui.selectionHandler != nil:
		// Presses outside the transcript (editor, status chrome, filler rows
		// below a short history) start nothing: selection is constrained to
		// the thread.
		if point, ok := ui.bodyPointLocked(event.Column, event.Row, false); ok {
			ui.stopSelectionScrollLocked()
			lastClick, lastClickAt, now := ui.selection.lastClick, ui.selection.lastClickAt, time.Now()
			ui.selection = mouseSelection{anchor: point, focus: point, active: true, lastClick: point, lastClickAt: now}
			if point == lastClick && now.Sub(lastClickAt) <= doubleClickInterval {
				ui.selection.anchor, ui.selection.focus = ui.sentenceBoundsLocked(point)
				ui.selection.moved, ui.selection.sentence = true, true
			}
			if ui.viewportFollow && ui.viewportBodyLines > ui.viewportBodyHeight {
				ui.viewportEnd, ui.viewportFollow = ui.viewportBodyLines, false
			}
		}
	}
	handler := ui.selectionHandler
	ui.renderMu.Unlock()
	if selected != "" && handler != nil {
		handler(selected)
	}
}

// viewportRangeLocked returns the half-open body content range [start, end)
// the viewport currently shows, from live scroll state rather than the last
// rendered frame.
func (ui *TUI) viewportRangeLocked() (start, end int) {
	end = ui.viewportEnd
	if ui.viewportFollow {
		end = ui.viewportBodyLines
	}
	end = max(0, min(end, ui.viewportBodyLines))
	return max(0, end-ui.viewportBodyHeight), end
}

// bodyPointLocked maps a screen cell to transcript content coordinates.
// clamp=false rejects positions outside the visible transcript (selection
// starts); clamp=true snaps strays — chrome rows, the scrollbar column —
// back onto the nearest transcript cell (drag, release, wheel extension).
func (ui *TUI) bodyPointLocked(column, row int, clamp bool) (mousePoint, bool) {
	if ui.viewportBody == nil || ui.viewportBodyHeight <= 0 {
		return mousePoint{}, false
	}
	start, end := ui.viewportRangeLocked()
	visible := end - start
	if visible <= 0 {
		return mousePoint{}, false
	}
	if !clamp && (row < 0 || row >= visible) {
		return mousePoint{}, false
	}
	row = max(0, min(row, visible-1))
	column = max(0, min(column, max(0, ui.viewportBodyWidth-1)))
	return mousePoint{row: start + row, column: column}, true
}

func (ui *TUI) clearSelectionLocked() {
	ui.stopSelectionScrollLocked()
	ui.selection = mouseSelection{}
}

// updateSelectionScrollLocked re-evaluates the edge-drag zones on every drag
// report: the top and bottom visible body rows (and anything past them, into
// the chrome) drive auto-scroll in that direction, anywhere else stops it.
func (ui *TUI) updateSelectionScrollLocked(event MouseEvent) {
	if !ui.selection.active || ui.selection.sentence || ui.viewportBodyHeight <= 0 {
		ui.stopSelectionScrollLocked()
		return
	}
	direction, overshoot := 0, 0
	bottom := ui.viewportBodyHeight - 1
	switch {
	case event.Row <= 0:
		direction, overshoot = -1, -event.Row
	case event.Row >= bottom:
		direction, overshoot = 1, event.Row-bottom
	default:
		ui.stopSelectionScrollLocked()
		return
	}
	ui.selectionScroll.direction, ui.selectionScroll.overshoot = direction, overshoot
	ui.selectionScroll.column = max(0, min(event.Column, max(0, ui.viewportBodyWidth-1)))
	if ui.selectionScroll.timer == nil {
		ui.armSelectionScrollLocked()
	}
}

func (ui *TUI) armSelectionScrollLocked() {
	ui.selectionScroll.generation++
	generation := ui.selectionScroll.generation
	interval := ui.selectionScroll.interval
	if interval <= 0 {
		interval = selectionScrollInterval
	}
	ui.selectionScroll.timer = time.AfterFunc(interval, guarded(func() { ui.selectionScrollTick(generation) }))
}

func (ui *TUI) stopSelectionScrollLocked() {
	ui.selectionScroll.generation++
	if ui.selectionScroll.timer != nil {
		ui.selectionScroll.timer.Stop()
		ui.selectionScroll.timer = nil
	}
	ui.selectionScroll.direction, ui.selectionScroll.overshoot = 0, 0
}

// selectionScrollRate is the auto-scroll rate curve: one row per tick with
// the pointer resting on the edge row, one row faster for every row of
// overshoot past it, capped.
func selectionScrollRate(overshoot int) int {
	return min(1+max(0, overshoot), selectionScrollMaxRows)
}

// selectionScrollTick is the auto-scroll timer callback: scroll one rate step,
// pin the selection focus to the row that just scrolled into the edge, rearm.
// The generation check invalidates callbacks that raced a stop or a re-arm.
func (ui *TUI) selectionScrollTick(generation uint64) {
	ui.renderMu.Lock()
	scroll := &ui.selectionScroll
	if generation != scroll.generation || scroll.timer == nil {
		ui.renderMu.Unlock()
		return
	}
	if scroll.direction == 0 || !ui.selection.active || ui.selection.sentence {
		scroll.timer, scroll.direction, scroll.overshoot = nil, 0, 0
		ui.renderMu.Unlock()
		return
	}
	ui.scrollViewportLocked(scroll.direction * selectionScrollRate(scroll.overshoot))
	start, end := ui.viewportRangeLocked()
	if end > start {
		row := start
		if scroll.direction > 0 {
			row = end - 1
		}
		point := mousePoint{row: row, column: scroll.column}
		ui.selection.focus = point
		ui.selection.moved = ui.selection.moved || point != ui.selection.anchor
	}
	ui.armSelectionScrollLocked()
	ui.renderMu.Unlock()
	ui.RequestRender()
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

// selectedTextLocked extracts the selected transcript CONTENT: it re-renders
// the selected body line range (never the composed frame, so chrome and the
// scrollbar column can't leak in) and strips presentation decoration so the
// clipboard receives clean message text.
func (ui *TUI) selectedTextLocked() string {
	if ui.viewportBody == nil || ui.viewportBodyLines <= 0 {
		return ""
	}
	start, end := ui.selection.bounds()
	start.row = max(0, min(start.row, ui.viewportBodyLines-1))
	end.row = max(start.row, min(end.row, ui.viewportBodyLines-1))
	lines := componentLines(ui.viewportBody, ui.viewportBodyWidth, start.row, end.row+1)
	rows := make([]string, 0, len(lines))
	firstFull := true
	for index, line := range lines {
		row := start.row + index
		width := VisibleWidth(line)
		from, to := selectionColumns(row, start, end, width)
		from = selectionColumnStart(line, from)
		if row == start.row {
			firstFull = from == 0
		}
		rows = append(rows, plainTerminalText(SliceByColumn(line, from, max(0, to-from), false)))
	}
	return joinSelectedContent(rows, firstFull)
}

// selectionMarginWidth measures a line's presentation margin: the leading run
// of band gutter bars and padding spaces (every cell is width one).
func selectionMarginWidth(line string) int {
	margin := 0
	for _, r := range line {
		if r != ' ' && r != '┃' {
			break
		}
		margin++
	}
	return margin
}

// joinSelectedContent turns raw selected cells into paste-clean content.
// Chat bands share one left edge (gutter cell plus box padding), so the
// narrowest margin across the fully-selected rows is presentation on every
// row: stripping exactly that much removes gutter bars and interior padding
// while deeper, content-owned indentation keeps its relative depth. Band
// padding rows collapse to single blank separators between messages.
// firstFull reports whether the first row was selected from column zero;
// a mid-line start contributes no margin and is never dedented.
func joinSelectedContent(rows []string, firstFull bool) string {
	margin := -1
	for index, row := range rows {
		rows[index] = strings.TrimRight(row, " \t")
		if index == 0 && !firstFull {
			continue
		}
		if width := selectionMarginWidth(rows[index]); rows[index] != "" && (margin < 0 || width < margin) {
			margin = width
		}
	}
	joined := rows[:0]
	blankPending := false
	for index, row := range rows {
		if row == "" {
			blankPending = len(joined) > 0
			continue
		}
		if margin > 0 && (index > 0 || firstFull) {
			strip := min(margin, selectionMarginWidth(row))
			for ; strip > 0; strip-- {
				_, size := utf8.DecodeRuneInString(row)
				row = row[size:]
			}
		}
		if blankPending {
			joined = append(joined, "")
			blankPending = false
		}
		joined = append(joined, row)
	}
	return strings.Join(joined, "\n")
}

// sentenceBoundsLocked expands a double click to sentence bounds within the
// visible transcript lines, returning content-anchored points.
func (ui *TUI) sentenceBoundsLocked(point mousePoint) (mousePoint, mousePoint) {
	top, bottom := ui.viewportRangeLocked()
	if point.row < top || point.row >= bottom {
		return point, point
	}
	lines := componentLines(ui.viewportBody, ui.viewportBodyWidth, top, bottom)
	local := mousePoint{row: point.row - top, column: point.column}
	start, end := sentenceBounds(lines, local)
	start.row, end.row = start.row+top, end.row+top
	return start, end
}

// ponytail: scan visible text only; add wrap metadata if selection must cross viewport edges.
func sentenceBounds(lines []string, point mousePoint) (mousePoint, mousePoint) {
	plain, offset := make([]string, len(lines)), 0
	for row, line := range lines {
		plain[row] = plainTerminalText(strings.Replace(line, scrollbarThumb, "", 1))
		if row < point.row {
			offset += len(plain[row]) + 1
		}
	}
	column := selectionColumnStart(plain[point.row], point.column)
	offset += len(SliceByColumn(plain[point.row], 0, column, false))
	text, start, end := strings.Join(plain, "\n"), 0, 0
	offset = min(offset, len(text))
	end = len(text)
	if index := strings.LastIndexAny(text[:offset], ".!?。！？"); index >= 0 {
		_, size := utf8.DecodeRuneInString(text[index:])
		start = index + size
	}
	if index := strings.IndexAny(text[offset:], ".!?。！？"); index >= 0 {
		_, size := utf8.DecodeRuneInString(text[offset+index:])
		end = offset + index + size
	}
	segment := text[start:end]
	trimmed := strings.TrimSpace(segment)
	if trimmed == "" {
		return point, point
	}
	start += strings.Index(segment, trimmed)
	end = start + len(trimmed)
	return textOffsetPoint(plain, start, false), textOffsetPoint(plain, end, true)
}

func textOffsetPoint(lines []string, offset int, inclusive bool) mousePoint {
	cursor := 0
	for row, line := range lines {
		if offset <= cursor+len(line) {
			column := VisibleWidth(line[:max(0, min(len(line), offset-cursor))])
			if inclusive && column > 0 {
				column--
			}
			return mousePoint{row: row, column: column}
		}
		cursor += len(line) + 1
	}
	row := len(lines) - 1
	return mousePoint{row: row, column: max(0, VisibleWidth(lines[row])-1)}
}

// ScrollToBottom reattaches live follow so the newest lines are visible again.
// Scrolling away detaches follow on purpose, which keeps streaming frames from
// moving viewed history; an explicit user action reattaches it, as ctrl+end does.
func (ui *TUI) ScrollToBottom() {
	ui.renderMu.Lock()
	ui.viewportFollow = true
	ui.renderMu.Unlock()
	ui.RequestRender()
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
		ui.frameScratch = ui.previousLines[:0]
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
		ui.frameScratch = ui.previousLines[:0]
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
			_ = ui.stopTerminal(ui.viewportBody != nil)
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
	ui.frameScratch = ui.previousLines[:0]
	ui.previousLines, ui.previousWidth, ui.previousHeight = newLines, width, height
	ui.previousImageIDs = collectKittyImageIDs(newLines)
}

// renderViewport builds the frame in ui.frameScratch, which double-buffers
// against previousLines: the swap sites in RenderNow recycle the retired
// frame, so the buffer being written never aliases previousLines.
func (ui *TUI) renderViewport(width, height int) []string {
	ui.mouseOverlays = nil
	if ui.viewportBody == nil {
		return append(ui.frameScratch[:0], ui.Render(width)...)
	}
	chrome := ui.viewportChrome.Render(width)
	// Where the chrome landed on screen, so mouse events can be rebased onto
	// the component that drew the row.
	ui.chromeTrim = max(0, len(chrome)-height)
	chrome = chrome[ui.chromeTrim:]
	bodyHeight := height - len(chrome)
	ui.chromeTop = bodyHeight
	bodyWidth := width
	if width > 1 {
		bodyWidth--
	}
	ui.viewportBodyWidth = bodyWidth
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
	lines := ui.frameScratch[:0]
	if cap(lines) == 0 {
		lines = make([]string, 0, height)
	}
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

// renderSelection paints the content-anchored selection onto the frame: only
// the slice of the selection inside the visible body range is highlighted,
// mapped from content rows to screen rows, so chrome rows are never touched
// and scrolling moves the highlight with its text.
func (ui *TUI) renderSelection(lines []string) []string {
	if !ui.selection.active || !ui.selection.moved {
		return lines
	}
	start, end := ui.selection.bounds()
	viewStart, viewEnd := ui.viewportRangeLocked()
	first, last := max(start.row, viewStart), min(end.row, viewEnd-1)
	if first > last {
		return lines
	}
	result := append([]string(nil), lines...)
	for row := first; row <= last; row++ {
		screen := row - viewStart
		if screen < 0 || screen >= len(result) {
			continue
		}
		line := result[screen]
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
		result[screen] = line
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
