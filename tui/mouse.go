package tui

import (
	"fmt"
	"strings"
)

// MouseEventType is the gesture an SGR mouse report describes.
type MouseEventType uint8

const (
	MousePress MouseEventType = iota
	MouseRelease
	MouseDrag
	MouseWheelUp
	MouseWheelDown
	MouseMove
)

// MouseEvent is one decoded SGR mouse report. Row and Column are zero-based
// and, once dispatched, local to the receiving component.
type MouseEvent struct {
	Type   MouseEventType
	Button int // 0 left, 1 middle, 2 right
	Row    int
	Column int
	Shift  bool
	Alt    bool
	Ctrl   bool
	// Clicks is 2 for a second press on the same cell within
	// doubleClickInterval. It is only set on MousePress.
	Clicks int
}

// MouseHandler is the optional capability a component advertises to receive
// mouse input. It is discovered by type assertion so the Component contract
// stays Render-only and every existing component and extension UI keeps
// working untouched.
type MouseHandler interface {
	// HandleMouse reports whether the event was consumed.
	HandleMouse(MouseEvent) bool
}

// MouseMotionHandler additionally receives hover (MouseMove) reports.
// Any-motion tracking floods the input stream, so the TUI enables it only
// while a component advertising this holds focus and reverts to button-event
// tracking when focus moves on.
type MouseMotionHandler interface {
	MouseHandler
	WantsMouseMotion() bool
}

// IsMouseReport matches every SGR mouse report, including the ones parseMouse
// rejects, so callers swallow them instead of leaking escape bytes as text.
func IsMouseReport(data string) bool {
	return strings.HasPrefix(data, "\x1b[<") &&
		(strings.HasSuffix(data, "M") || strings.HasSuffix(data, "m"))
}

// parseMouse decodes SGR (1006) reports only. Terminals stuck on legacy X10
// reporting send \x1b[M with no release button and no column past 223, so they
// degrade to keyboard-only instead of dispatching half-known clicks.
func parseMouse(data string) (MouseEvent, bool) {
	if !IsMouseReport(data) {
		return MouseEvent{}, false
	}
	var code, column, row int
	if _, err := fmt.Sscanf(data[:len(data)-1], "\x1b[<%d;%d;%d", &code, &column, &row); err != nil {
		return MouseEvent{}, false
	}
	if code < 0 || column < 1 || row < 1 {
		return MouseEvent{}, false
	}
	event := MouseEvent{
		Button: code & 3,
		Row:    row - 1,
		Column: column - 1,
		Shift:  code&4 != 0,
		Alt:    code&8 != 0,
		Ctrl:   code&16 != 0,
	}
	switch {
	case code&64 != 0:
		if code&2 != 0 {
			return MouseEvent{}, false // horizontal wheel: nothing here scrolls sideways
		}
		event.Type = MouseWheelUp
		if code&1 != 0 {
			event.Type = MouseWheelDown
		}
	case strings.HasSuffix(data, "m"):
		event.Type = MouseRelease
	case code&32 != 0:
		// Button bits 3 mean no button is held: an any-motion (1003) hover.
		event.Type = MouseDrag
		if code&3 == 3 {
			event.Type = MouseMove
		}
	default:
		event.Type = MousePress
	}
	return event, true
}

// mouseTargetAt walks a component in the order Render composes it and returns
// the mouse-aware component covering row, with row rebased onto that
// component's own render.
func mouseTargetAt(component Component, width, row int) (MouseHandler, int, bool) {
	if container, ok := component.(*Container); ok {
		offset := 0
		for _, child := range container.Children() {
			count := componentLineCount(child, width)
			if row < offset+count {
				return mouseTargetAt(child, width, row-offset)
			}
			offset += count
		}
		return nil, 0, false
	}
	handler, ok := component.(MouseHandler)
	return handler, row, ok
}

// runeIndexAtColumn maps a visible column to the rune index of the grapheme
// covering it, so clicks land on the right character in CJK and emoji text.
func runeIndexAtColumn(text string, column int) int {
	index, current := 0, 0
	forEachGrapheme(text, func(grapheme string) bool {
		if current+graphemeWidth(grapheme) > column {
			return false
		}
		current += graphemeWidth(grapheme)
		index += runeLen(grapheme)
		return true
	})
	return index
}
