package tui

// This file is the single implementation of the pointer semantic every
// list-like component shares (SelectList, SettingsList, and the modes
// selectors): hovering or clicking a row moves the selection highlight to it
// IN PLACE — the visible window must not re-anchor — a double click confirms
// the current selection, and the wheel moves the selection exactly as the
// component's keyboard scrolling does. Keeping the window still under the
// pointer is what makes hover safe on scrollable lists: a recentring window
// would shift rows under the stationary cursor and feed back into
// hit-testing.

// ListWindow anchors the visible window of a selection-windowed list.
// Non-pointer selection changes (keyboard, wheel, filtering) recenter the
// window on the selection; pointer selection freezes it. Start must be the
// only source of the render window so hit-testing and rendering agree.
type ListWindow struct {
	start  int
	frozen bool
}

// Recenter makes the next Start recenter the window on the selection. Every
// non-pointer selection change goes through it.
func (window *ListWindow) Recenter() { window.frozen = false }

// Freeze keeps the current window across pointer selection changes.
func (window *ListWindow) Freeze() { window.frozen = true }

// Start returns the first visible index. The unfrozen arm is the historical
// center-anchor formula, byte-identical for keyboard-only flows; the frozen
// arm only clamps the stored window to the current item count.
func (window *ListWindow) Start(selected, count, maxVisible int) int {
	if window.frozen {
		window.start = max(0, min(window.start, count-maxVisible))
	} else {
		window.start = ListWindowStart(selected, count, maxVisible)
	}
	return window.start
}

// ListWindowStart is the center-anchor formula for a selection-windowed list:
// the window centers on the selection and clamps to the item count. ListWindow
// applies it when unfrozen; window-less lists call it directly.
func ListWindowStart(selected, count, maxVisible int) int {
	return max(0, min(selected-maxVisible/2, count-maxVisible))
}

// ListRowIndex maps a component-local row to its item index for a windowed
// list whose first row rendered at line top and which shows the items
// [start, start+count); rows outside the window or past the item count miss.
func ListRowIndex(row, top, start, count, itemCount int) (int, bool) {
	if row < top || row >= top+count {
		return 0, false
	}
	index := start + row - top
	if index >= itemCount {
		return 0, false
	}
	return index, true
}

// ListMouseTarget is the contract HandleListMouse drives. Implementations
// own their locking; the handler never calls two methods concurrently.
type ListMouseTarget interface {
	// ListRowAt maps a component-local row to the item index it renders.
	ListRowAt(row int) (int, bool)
	// ListSelectRow moves the selection to index while preserving the
	// visible window (hover and single click).
	ListSelectRow(index int)
	// ListScroll moves the selection direction (-1 or +1) wheel ticks; the
	// component applies its own per-tick step and recenters as its keyboard
	// scrolling does.
	ListScroll(direction int)
	// ListConfirm confirms the current selection (double click).
	ListConfirm()
}

// ListRowClicker optionally refines what a single click does beyond
// selecting the row, for components with click targets inside a row (the
// tree selector's fold markers).
type ListRowClicker interface {
	ListClickRow(index int, event MouseEvent)
}

// HandleListMouse dispatches one mouse event with the shared list pointer
// semantic. Rows that miss (borders, scroll-info lines) are not consumed so
// the event can fall through to the viewport.
func HandleListMouse(target ListMouseTarget, event MouseEvent) bool {
	switch {
	case event.Type == MouseMove:
		// Hover is opt-in: a list that does not advertise motion tracking
		// never gets any-motion reports enabled for it, so a stray one (the
		// mode lingering from another component) must not move its selection.
		if motion, ok := target.(MouseMotionHandler); !ok || !motion.WantsMouseMotion() {
			return false
		}
		index, ok := target.ListRowAt(event.Row)
		if !ok {
			return false
		}
		target.ListSelectRow(index)
		return true
	case event.Type == MouseWheelUp || event.Type == MouseWheelDown:
		direction := -1
		if event.Type == MouseWheelDown {
			direction = 1
		}
		target.ListScroll(direction)
		return true
	case event.Type == MousePress && event.Button == 0:
		index, ok := target.ListRowAt(event.Row)
		if !ok {
			return false
		}
		// The first press of a double click already selected this cell, and
		// the frozen window kept it under the cursor; confirm it as-is.
		if event.Clicks >= 2 {
			target.ListConfirm()
			return true
		}
		if clicker, ok := target.(ListRowClicker); ok {
			clicker.ListClickRow(index, event)
		} else {
			target.ListSelectRow(index)
		}
		return true
	}
	return false
}
