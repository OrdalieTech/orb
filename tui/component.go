package tui

// CursorMarker is a zero-width APC sequence. Focused text components place it
// at their logical cursor so the renderer can position the hardware cursor for
// IME candidate windows without displaying it.
const CursorMarker = "\x1b_pi:c\x07"

// Component is the stable rendering contract shared with extension UIs.
type Component interface {
	Render(width int) []string
}

// Invalidatable clears render state derived from theme or size changes.
type Invalidatable interface {
	Invalidate()
}

// KeyEvent is one terminal input sequence after protocol-aware parsing.
type KeyEvent struct {
	Raw  string
	Key  string
	Type KeyEventType
}

// InputHandler receives input while its component has focus.
type InputHandler interface {
	HandleInput(KeyEvent)
}

// Focusable receives input and exposes focus state for cursor-marker emission.
type Focusable interface {
	Component
	InputHandler
	SetFocused(bool)
}

// KeyReleaseConsumer opts into Kitty key-release events, which are otherwise
// filtered before dispatch just as upstream does.
type KeyReleaseConsumer interface {
	WantsKeyRelease() bool
}

// RenderRequester is the narrow seam animated components need from TUI.
type RenderRequester interface {
	RequestRender()
}

// StyleFunc applies ANSI styling or a background to a complete string.
type StyleFunc func(string) string

func invalidate(component Component) {
	if component, ok := component.(Invalidatable); ok {
		component.Invalidate()
	}
}
