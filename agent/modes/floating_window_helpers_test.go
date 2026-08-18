package modes

import "github.com/OrdalieTech/orb/tui"

// floatingWindowChild finds the topmost floating window whose content is a T.
// Menus and dialogs float as Frame-wrapped overlays; tests locate them here
// instead of peeking into the editor slot.
func floatingWindowChild[T any](mode *InteractiveMode) (T, bool) {
	var zero T
	if mode.ui == nil {
		return zero, false
	}
	components := mode.ui.VisibleOverlayComponents()
	for index := len(components) - 1; index >= 0; index-- {
		if frame, ok := components[index].(*tui.Frame); ok {
			if child, ok := any(frame.Child).(T); ok {
				return child, true
			}
		}
	}
	return zero, false
}

// renderTopFloatingWindow renders the topmost visible overlay.
func renderTopFloatingWindow(mode *InteractiveMode, width int) []string {
	components := mode.ui.VisibleOverlayComponents()
	if len(components) == 0 {
		return nil
	}
	return components[len(components)-1].Render(width)
}
