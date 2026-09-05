package tui

import (
	"strings"
	"unicode"
)

// GridList is the shared list-with-columns primitive behind every floating
// menu: rows of pre-styled cells aligned on an invisible grid, unselectable
// section headers, a fixed detail area, and optional type-to-filter search.
// Styling is injected (GridListTheme) like every other tui component; cells
// arrive already styled and are measured ANSI-aware.
type GridRow struct {
	Cells  []string // pre-styled cells; the last column truncates to fit
	Detail []string // shown in the fixed detail area while selected
	Header bool     // unselectable section header; Cells[0] spans the width
	Value  string   // identity handed to callbacks
	Search string   // filter text; defaults to the cells' visible text
}

type GridListTheme struct {
	SelectedBg StyleFunc // background for the selected row
	Detail     StyleFunc // detail lines (applied on top of any cell styling)
	ScrollInfo StyleFunc // the (i/n) counter, rules, and search placeholder
	Query      StyleFunc // the typed filter text
	Cursor     string    // selection prefix, e.g. "› "
}

type GridList struct {
	rows       []GridRow // master rows
	view       []GridRow // filtered view (== rows while the query is empty)
	selected   int       // index into view
	maxVisible int
	fixedRows  int // stable row-region height, so filtering never resizes
	theme      GridListTheme
	window     ListWindow
	focused    bool
	query      string
	rowLines   []int // screen line of each view row in the last render (mouse)
	widths     []int // column widths, measured once per SetRows

	// Searchable enables type-to-filter: printable keys build a fuzzy query,
	// backspace edits it, and escape clears it before it closes the menu.
	Searchable bool
	// DetailHeight reserves a fixed detail area under the rows (separated by
	// a rule) showing the selection's Detail lines. Fixed means the component
	// height never changes with the selection, so an overlay window neither
	// grows nor moves while hovering. Zero disables the area.
	DetailHeight int

	// OnConfirm fires on enter/space/double-click with the selected Value.
	OnConfirm func(value string)
	// OnCancel fires on escape.
	OnCancel func()
	// OnChange fires whenever the selection moves to a new Value.
	OnChange func(value string)
	// OnKey sees unhandled keys first; return true to consume (screen-specific
	// action keys like m or r — avoid plain letters on Searchable lists).
	OnKey func(event KeyEvent, value string) bool
}

func NewGridList(rows []GridRow, maxVisible int, theme GridListTheme) *GridList {
	if theme.Cursor == "" {
		theme.Cursor = "> "
	}
	if maxVisible < 3 {
		maxVisible = 3
	}
	list := &GridList{maxVisible: maxVisible, theme: theme}
	list.SetRows(rows)
	return list
}

// SetRows replaces the master rows, keeping the selection on the same Value
// when it still exists.
func (list *GridList) SetRows(rows []GridRow) {
	value := list.SelectedValue()
	list.rows = rows
	list.widths = list.columnWidths()
	list.fixedRows = min(list.maxVisible, len(rows))
	list.rebuildView(value)
}

// SetQuery programmatically seeds the filter (menus opened with an initial
// search string).
func (list *GridList) SetQuery(query string) {
	list.query = query
	list.rebuildView("")
}

func (list *GridList) rebuildView(keepValue string) {
	if list.query == "" {
		list.view = list.rows
	} else {
		// Headers disappear while filtering: matches read as one flat result
		// list. Columns keep the master measurement, so nothing jitters.
		candidates := make([]GridRow, 0, len(list.rows))
		for _, row := range list.rows {
			if !row.Header {
				candidates = append(candidates, row)
			}
		}
		list.view = FuzzyFilter(candidates, list.query, func(row GridRow) string {
			if row.Search != "" {
				return row.Search
			}
			return StripANSI(strings.Join(row.Cells, " "))
		})
	}
	list.selected = list.nearestSelectable(0, 1)
	if keepValue != "" {
		for index, row := range list.view {
			if !row.Header && row.Value == keepValue {
				list.selected = index
				break
			}
		}
	}
	list.window.Recenter()
}

func (list *GridList) SelectedValue() string {
	if list.selected >= 0 && list.selected < len(list.view) {
		return list.view[list.selected].Value
	}
	return ""
}

func (list *GridList) SetFocused(focused bool) { list.focused = focused }

func (list *GridList) nearestSelectable(from, direction int) int {
	for index := from; index >= 0 && index < len(list.view); index += direction {
		if !list.view[index].Header {
			return index
		}
	}
	return -1
}

func (list *GridList) move(direction int) {
	before := list.SelectedValue()
	if next := list.nearestSelectable(list.selected+direction, direction); next >= 0 {
		list.selected = next
	}
	list.window.Recenter()
	if after := list.SelectedValue(); after != before && list.OnChange != nil {
		list.OnChange(after)
	}
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
	case bindings.Matches(event.Raw, "tui.select.confirm"):
		if list.OnConfirm != nil {
			list.OnConfirm(list.SelectedValue())
		}
	case bindings.Matches(event.Raw, "tui.select.cancel"):
		if list.Searchable && list.query != "" {
			list.query = ""
			list.rebuildView("")
			return
		}
		if list.OnCancel != nil {
			list.OnCancel()
		}
	case list.Searchable && (event.Raw == "\x7f" || event.Raw == "\b"):
		if list.query != "" {
			runes := []rune(list.query)
			list.query = string(runes[:len(runes)-1])
			list.rebuildView("")
		}
	default:
		if list.OnKey != nil && list.OnKey(event, list.SelectedValue()) {
			return
		}
		if event.Raw == " " && (!list.Searchable || list.query == "") {
			if list.OnConfirm != nil {
				list.OnConfirm(list.SelectedValue())
			}
			return
		}
		if list.Searchable {
			runes := []rune(event.Raw)
			if len(runes) == 1 && unicode.IsPrint(runes[0]) {
				list.query += event.Raw
				list.rebuildView("")
			}
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
			if list.view[index].Header {
				return 0, false
			}
			return index, true
		}
	}
	return 0, false
}

func (list *GridList) ListSelectRow(index int) {
	if index >= 0 && index < len(list.view) && !list.view[index].Header {
		before := list.SelectedValue()
		list.selected = index
		list.window.Freeze()
		if after := list.SelectedValue(); after != before && list.OnChange != nil {
			list.OnChange(after)
		}
	}
}

func (list *GridList) ListScroll(direction int) { list.move(direction) }

func (list *GridList) ListConfirm() {
	if list.OnConfirm != nil {
		list.OnConfirm(list.SelectedValue())
	}
}

const gridColumnGap = 2

// columnWidths measures every non-header row; each row's last cell is not
// measured because it flexes into the remaining width, so rows with fewer
// cells never inflate the shared columns.
func (list *GridList) columnWidths() []int {
	var widths []int
	for _, row := range list.rows {
		if row.Header {
			continue
		}
		for index, cell := range row.Cells {
			if index == len(row.Cells)-1 {
				continue
			}
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
	widths := list.widths
	cursorWidth := VisibleWidth(list.theme.Cursor)
	style := func(fn StyleFunc, text string) string {
		if fn == nil {
			return text
		}
		return fn(text)
	}
	lines := make([]string, 0, list.fixedRows+3+list.DetailHeight)
	if list.Searchable {
		search := style(list.theme.ScrollInfo, "⌕ ") + style(list.theme.Query, list.query)
		if list.query == "" {
			search += style(list.theme.ScrollInfo, "type to filter")
		}
		lines = append(lines, TruncateToWidth(search, width, "…", false), "")
	}
	start := list.window.Start(list.selected, len(list.view), list.maxVisible)
	end := min(start+list.maxVisible, len(list.view))
	list.rowLines = make([]int, len(list.view))
	for index := range list.rowLines {
		list.rowLines[index] = -1
	}
	for index := start; index < end; index++ {
		row := list.view[index]
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
	}
	if list.Searchable {
		// Stable geometry while typing: the row region always occupies
		// fixedRows lines, matches or not.
		if end-start == 0 {
			lines = append(lines, style(list.theme.ScrollInfo, strings.Repeat(" ", cursorWidth)+"no matches"))
		}
		for len(lines) < list.searchChromeLines()+list.fixedRows {
			lines = append(lines, "")
		}
	}
	if list.DetailHeight > 0 {
		lines = append(lines, list.separator(width, style))
		var detail []string
		if list.selected >= 0 && list.selected < len(list.view) {
			detail = list.view[list.selected].Detail
		}
		for index := range list.DetailHeight {
			text := ""
			if index < len(detail) {
				text = style(list.theme.Detail, detail[index])
			}
			lines = append(lines, TruncateToWidth("  "+text, width, "…", false))
		}
	} else if len(list.view) > list.maxVisible {
		lines = append(lines, style(list.theme.ScrollInfo, strings.Repeat(" ", cursorWidth)+"("+list.counter()+")"))
	} else if list.Searchable && len(list.rows) > list.maxVisible {
		// Keep the counter slot while a filter shrinks the list under the
		// window, so the component height never changes while typing.
		lines = append(lines, "")
	}
	return lines
}

func (list *GridList) searchChromeLines() int {
	if list.Searchable {
		return 2 // the filter line and its spacer
	}
	return 0
}

// separator draws the rule between rows and the detail area, carrying the
// scroll counter near its right edge when the list overflows.
func (list *GridList) separator(width int, style func(StyleFunc, string) string) string {
	if len(list.view) <= list.maxVisible {
		return style(list.theme.ScrollInfo, strings.Repeat("─", width))
	}
	counter := " " + list.counter() + " "
	ruleWidth := max(0, width-VisibleWidth(counter)-2)
	return style(list.theme.ScrollInfo, strings.Repeat("─", ruleWidth)+counter+"──")
}

func (list *GridList) counter() string {
	position, total := 0, 0
	for index, row := range list.view {
		if row.Header {
			continue
		}
		total++
		if index <= list.selected {
			position++
		}
	}
	return itoa(position) + "/" + itoa(total)
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

// Frame wraps a component in the shared configuration-window chrome: an
// unbroken rounded border containing a title heading, the content, and a
// quiet hint line — everything inside the box. Interior lines are padded to
// the full width so an overlay fully covers what is beneath it.
type Frame struct {
	Title      string
	TitleStyle StyleFunc // defaults to Border
	Footer     string
	Border     StyleFunc
	Hint       StyleFunc
	Child      Component
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

func (frame *Frame) Render(width int) []string {
	if width < 8 {
		return nil
	}
	interior := width - 4 // border + one padding cell each side
	side := frame.style(frame.Border, "│")
	wrap := func(content string) string {
		return side + " " + TruncateToWidth(content, interior, "…", true) + " " + side
	}
	lines := []string{frame.style(frame.Border, "╭"+strings.Repeat("─", width-2)+"╮")}
	if frame.Title != "" {
		titleStyle := frame.TitleStyle
		if titleStyle == nil {
			titleStyle = frame.Border
		}
		lines = append(lines, wrap(frame.style(titleStyle, frame.Title)), wrap(""))
	}
	if frame.Child != nil {
		for _, line := range frame.Child.Render(interior) {
			lines = append(lines, wrap(line))
		}
	}
	if frame.Footer != "" {
		lines = append(lines, wrap(""), wrap(frame.style(frame.Hint, frame.Footer)))
	}
	lines = append(lines, frame.style(frame.Border, "╰"+strings.Repeat("─", width-2)+"╯"))
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

// HandleMouse translates to the child's coordinate space (chrome rows above
// the child, border + padding columns) before forwarding.
func (frame *Frame) HandleMouse(event MouseEvent) bool {
	handler, ok := frame.Child.(MouseHandler)
	if !ok {
		return false
	}
	event.Row-- // top border
	if frame.Title != "" {
		event.Row -= 2 // heading and spacer
	}
	event.Column -= 2
	return handler.HandleMouse(event)
}

var _ Focusable = (*Frame)(nil)
var _ Focusable = (*GridList)(nil)
