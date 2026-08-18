package tui

import "strings"

// GridList is the shared list-with-columns primitive behind the configuration
// screens (/plugins, /permissions, /mcp): rows of pre-styled cells aligned on
// an invisible grid, unselectable section headers, and detail lines expanded
// inline under the selection. Styling is injected (GridListTheme) like every
// other tui component; cells arrive already styled and are measured
// ANSI-aware.
type GridRow struct {
	Cells  []string // pre-styled cells; the last column truncates to fit
	Detail []string // rendered indented under the row while it is selected
	Header bool     // unselectable section header; Cells[0] spans the width
	Value  string   // identity handed to callbacks
}

type GridListTheme struct {
	SelectedBg StyleFunc // background for the selected row
	Detail     StyleFunc // detail lines (applied on top of any cell styling)
	ScrollInfo StyleFunc // the (i/n) overflow counter
	Cursor     string    // selection prefix, e.g. "› "
}

type GridList struct {
	rows       []GridRow
	selected   int
	maxVisible int
	theme      GridListTheme
	window     ListWindow
	focused    bool
	rowLines   []int // screen line of each row in the last render, for mouse mapping

	// OnConfirm fires on enter/space/double-click with the selected Value.
	OnConfirm func(value string)
	// OnCancel fires on escape.
	OnCancel func()
	// OnKey sees unhandled keys first; return true to consume (screen-specific
	// action keys like m or r).
	OnKey func(event KeyEvent, value string) bool
}

func NewGridList(rows []GridRow, maxVisible int, theme GridListTheme) *GridList {
	if theme.Cursor == "" {
		theme.Cursor = "> "
	}
	if maxVisible < 3 {
		maxVisible = 3
	}
	list := &GridList{rows: rows, maxVisible: maxVisible, theme: theme}
	list.selected = list.nearestSelectable(0, 1)
	return list
}

// SetRows replaces the rows, keeping the selection on the same Value when it
// still exists.
func (list *GridList) SetRows(rows []GridRow) {
	value := list.SelectedValue()
	list.rows = rows
	list.selected = list.nearestSelectable(0, 1)
	if value != "" {
		for index, row := range rows {
			if !row.Header && row.Value == value {
				list.selected = index
				break
			}
		}
	}
	list.window.Recenter()
}

func (list *GridList) SelectedValue() string {
	if list.selected >= 0 && list.selected < len(list.rows) {
		return list.rows[list.selected].Value
	}
	return ""
}

func (list *GridList) SetFocused(focused bool) { list.focused = focused }

func (list *GridList) nearestSelectable(from, direction int) int {
	for index := from; index >= 0 && index < len(list.rows); index += direction {
		if !list.rows[index].Header {
			return index
		}
	}
	return -1
}

func (list *GridList) move(direction int) {
	if next := list.nearestSelectable(list.selected+direction, direction); next >= 0 {
		list.selected = next
	}
	list.window.Recenter()
}

func (list *GridList) HandleInput(event KeyEvent) {
	bindings := GetKeybindings()
	switch {
	case bindings.Matches(event.Raw, "tui.select.up"):
		list.move(-1)
	case bindings.Matches(event.Raw, "tui.select.down"):
		list.move(1)
	case bindings.Matches(event.Raw, "tui.select.pageUp"):
		for range list.maxVisible {
			list.move(-1)
		}
	case bindings.Matches(event.Raw, "tui.select.pageDown"):
		for range list.maxVisible {
			list.move(1)
		}
	case bindings.Matches(event.Raw, "tui.select.confirm") || event.Raw == " ":
		if list.OnConfirm != nil {
			list.OnConfirm(list.SelectedValue())
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		if list.OnCancel != nil {
			list.OnCancel()
		}
	default:
		if list.OnKey != nil {
			list.OnKey(event, list.SelectedValue())
		}
	}
}

// Mouse: the shared list pointer semantic (hover keeps the window still,
// wheel scrolls the selection, double click confirms).
func (list *GridList) WantsMouseMotion() bool { return true }
func (list *GridList) HandleMouse(event MouseEvent) bool {
	return HandleListMouse(list, event)
}

func (list *GridList) ListRowAt(row int) (int, bool) {
	for index, line := range list.rowLines {
		if line == row {
			if list.rows[index].Header {
				return 0, false
			}
			return index, true
		}
	}
	return 0, false
}

func (list *GridList) ListSelectRow(index int) {
	if index >= 0 && index < len(list.rows) && !list.rows[index].Header {
		list.selected = index
		list.window.Freeze()
	}
}

func (list *GridList) ListScroll(direction int) { list.move(direction) }

func (list *GridList) ListConfirm() {
	if list.OnConfirm != nil {
		list.OnConfirm(list.SelectedValue())
	}
}

const gridColumnGap = 2

// columnWidths measures every non-header row; the last column is not measured
// because it flexes into the remaining width.
func (list *GridList) columnWidths() []int {
	var widths []int
	for _, row := range list.rows {
		if row.Header {
			continue
		}
		for index, cell := range row.Cells {
			if index >= len(widths) {
				widths = append(widths, 0)
			}
			widths[index] = max(widths[index], min(VisibleWidth(cell), 48))
		}
	}
	return widths
}

func (list *GridList) Render(width int) []string {
	if width < 8 || len(list.rows) == 0 {
		return nil
	}
	widths := list.columnWidths()
	cursorWidth := VisibleWidth(list.theme.Cursor)
	style := func(fn StyleFunc, text string) string {
		if fn == nil {
			return text
		}
		return fn(text)
	}
	start := list.window.Start(list.selected, len(list.rows), list.maxVisible)
	end := min(start+list.maxVisible, len(list.rows))
	lines := make([]string, 0, end-start+3)
	list.rowLines = make([]int, len(list.rows))
	for index := range list.rowLines {
		list.rowLines[index] = -1
	}
	for index := start; index < end; index++ {
		row := list.rows[index]
		list.rowLines[index] = len(lines)
		if row.Header {
			header := ""
			if len(row.Cells) > 0 {
				header = row.Cells[0]
			}
			lines = append(lines, TruncateToWidth(header, width, "…", false))
			continue
		}
		var builder strings.Builder
		if index == list.selected {
			builder.WriteString(list.theme.Cursor)
		} else {
			builder.WriteString(strings.Repeat(" ", cursorWidth))
		}
		for column, cell := range row.Cells {
			if column == len(row.Cells)-1 {
				remaining := width - VisibleWidth(builder.String())
				if remaining > 1 {
					builder.WriteString(TruncateToWidth(cell, remaining, "…", false))
				}
				break
			}
			pad := 0
			if column < len(widths) {
				pad = widths[column] - VisibleWidth(cell)
			}
			builder.WriteString(TruncateToWidth(cell, min(48, VisibleWidth(cell)), "…", false))
			builder.WriteString(strings.Repeat(" ", max(0, pad)+gridColumnGap))
		}
		line := TruncateToWidth(builder.String(), width, "…", true)
		if index == list.selected {
			line = ApplyBackgroundToLine(line, width, list.theme.SelectedBg)
		}
		lines = append(lines, line)
		if index == list.selected {
			for _, detail := range row.Detail {
				text := "  " + strings.Repeat(" ", cursorWidth) + style(list.theme.Detail, detail)
				lines = append(lines, TruncateToWidth(text, width, "…", false))
			}
		}
	}
	if len(list.rows) > list.maxVisible {
		position := 0
		for index := 0; index <= list.selected && index < len(list.rows); index++ {
			if !list.rows[index].Header {
				position++
			}
		}
		total := 0
		for _, row := range list.rows {
			if !row.Header {
				total++
			}
		}
		lines = append(lines, style(list.theme.ScrollInfo, strings.Repeat(" ", cursorWidth)+"("+itoa(position)+"/"+itoa(total)+")"))
	}
	return lines
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// Frame wraps a component in the shared configuration-window chrome: a
// rounded border with the title in the top edge and dim key hints in the
// bottom edge. Overlays need every cell painted, so interior lines are padded
// to the full width.
type Frame struct {
	Title  string
	Footer string
	Border StyleFunc
	Hint   StyleFunc
	Child  Component
	width  int
}

func NewFrame(title, footer string, border, hint StyleFunc, child Component) *Frame {
	return &Frame{Title: title, Footer: footer, Border: border, Hint: hint, Child: child}
}

func (frame *Frame) style(fn StyleFunc, text string) string {
	if fn == nil {
		return text
	}
	return fn(text)
}

func (frame *Frame) buildEdge(left, fill, right, label string, labelStyle StyleFunc, width int) string {
	interior := width - 2
	var builder strings.Builder
	builder.WriteString(frame.style(frame.Border, left))
	if label != "" {
		text := " " + label + " "
		text = TruncateToWidth(text, max(0, interior-2), "…", false)
		builder.WriteString(frame.style(frame.Border, fill))
		builder.WriteString(frame.style(labelStyle, text))
		used := 1 + VisibleWidth(text)
		builder.WriteString(frame.style(frame.Border, strings.Repeat(fill, max(0, interior-used))))
	} else {
		builder.WriteString(frame.style(frame.Border, strings.Repeat(fill, max(0, interior))))
	}
	builder.WriteString(frame.style(frame.Border, right))
	return builder.String()
}

// width is captured during Render so edge helpers agree with the body.
func (frame *Frame) Render(width int) []string {
	frame.width = width
	if width < 8 {
		return nil
	}
	interior := width - 4 // border + one padding cell each side
	lines := []string{frame.buildEdge("╭", "─", "╮", frame.Title, frame.Border, width)}
	if frame.Child != nil {
		for _, line := range frame.Child.Render(interior) {
			padded := TruncateToWidth(line, interior, "…", true)
			lines = append(lines, frame.style(frame.Border, "│")+" "+padded+" "+frame.style(frame.Border, "│"))
		}
	}
	lines = append(lines, frame.buildEdge("╰", "─", "╯", frame.Footer, frame.Hint, width))
	return lines
}

// Frame forwards focus and input to its child so it can wrap Focusable
// components directly.
func (frame *Frame) HandleInput(event KeyEvent) {
	if handler, ok := frame.Child.(InputHandler); ok {
		handler.HandleInput(event)
	}
}

func (frame *Frame) SetFocused(focused bool) {
	if focusable, ok := frame.Child.(Focusable); ok {
		focusable.SetFocused(focused)
	}
}

func (frame *Frame) WantsMouseMotion() bool {
	if motion, ok := frame.Child.(MouseMotionHandler); ok {
		return motion.WantsMouseMotion()
	}
	return false
}

// HandleMouse translates to the child's coordinate space (border + padding
// offset) before forwarding.
func (frame *Frame) HandleMouse(event MouseEvent) bool {
	handler, ok := frame.Child.(MouseHandler)
	if !ok {
		return false
	}
	event.Row--
	event.Column -= 2
	return handler.HandleMouse(event)
}

var _ Focusable = (*Frame)(nil)
var _ Focusable = (*GridList)(nil)
