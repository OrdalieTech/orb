package modes

import (
	"sync"

	"github.com/OrdalieTech/orb/tui"
)

// listRowOffsets is the row-offset machinery shared by the windowed modes
// selectors: it records where each visible row landed during Render and maps
// pointer rows back to item indices for tui.HandleListMouse. State is guarded
// because renders and hit-tests can overlap.
type listRowOffsets struct {
	mu           sync.Mutex
	offsets      []int
	visibleStart int
	visibleCount int
}

// setWindow records the visible window [start, start+count) the next render
// will show.
func (rows *listRowOffsets) setWindow(start, count int) {
	rows.mu.Lock()
	rows.visibleStart, rows.visibleCount = start, count
	rows.mu.Unlock()
}

// setOffsets stores a row-offset table recorded by a bespoke render walk (the
// extension selector, whose options wrap at narrow widths).
func (rows *listRowOffsets) setOffsets(offsets []int) {
	rows.mu.Lock()
	rows.offsets = offsets
	rows.mu.Unlock()
}

// renderRecordingRows renders container's children in order and records where
// each visible row of list landed (plus one trailing boundary) so rowAt can
// map pointer rows back to items; the emitted lines are the container's own.
func (rows *listRowOffsets) renderRecordingRows(container, list *tui.Container, width int) []string {
	rows.mu.Lock()
	count := rows.visibleCount
	rows.mu.Unlock()
	lines, offsets := make([]string, 0), make([]int, 0, count+1)
	for _, child := range container.Children() {
		if child != list {
			lines = append(lines, child.Render(width)...)
			continue
		}
		for index, row := range list.Children() {
			if index <= count {
				offsets = append(offsets, len(lines))
			}
			lines = append(lines, row.Render(width)...)
		}
	}
	if len(offsets) == count {
		offsets = append(offsets, len(lines))
	}
	rows.setOffsets(offsets)
	return lines
}

// rowAt maps a component-local row to the item index it renders.
func (rows *listRowOffsets) rowAt(row int) (int, bool) {
	rows.mu.Lock()
	offsets, start := rows.offsets, rows.visibleStart
	rows.mu.Unlock()
	for index := range max(0, len(offsets)-1) {
		if row >= offsets[index] && row < offsets[index+1] {
			return start + index, true
		}
	}
	return 0, false
}

// listSelectRow moves the highlight without re-anchoring the window, so hover
// can never shift rows under the cursor.
func listSelectRow(window *tui.ListWindow, selected *int, index int, update func()) {
	if index == *selected {
		return
	}
	window.Freeze()
	*selected = index
	update()
}

// listScroll moves the selection one row per wheel tick, recentring like
// keyboard navigation does.
func listScroll(window *tui.ListWindow, selected *int, direction, count int, update func()) {
	window.Recenter()
	*selected = max(0, min(*selected+direction, count-1))
	update()
}
