package modes

import (
	"strconv"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/codingagent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

// editDiffSplitWidth is the content-width breakpoint above which an edit diff
// renders side by side (opencode's chat diffs split past 120 columns). At or
// below it the unified view stays byte-identical to the pre-split renderer.
const editDiffSplitWidth = 120

// editDiffView renders one edit-tool display diff. The wire-format diff
// string (tools.GenerateDiffString) is the input and is never altered: narrow
// widths reuse the existing unified highlighting; wide widths re-lay the same
// rows into old|new panes with line numbers and red/green marks. Parsing and
// unified styling happen once at construction, and split frames cache per
// width, so the per-frame path is a lookup.
type editDiffView struct {
	mu         sync.Mutex
	unified    *tui.Text
	rows       []editDiffRow
	numberPad  int
	added      int
	removed    int
	cacheWidth int
	cacheLines []string
}

type editDiffRow struct {
	kind   byte // '+', '-', ' ' (context), or '.' (elided-context marker)
	number int
	text   string
}

// NewEditDiffView parses the display diff produced by the edit tool. The
// row format is fixed upstream: marker byte, right-aligned line number
// (space-padded), one separator space, then the line text; elided context is
// a numberless "..." row.
func NewEditDiffView(diff string) *editDiffView {
	// The unified view is the exact pre-split component (highlight + wrapping
	// text), so frames at or below the breakpoint cannot drift.
	view := &editDiffView{unified: tui.NewText(strings.Join(theme.Highlight(diff, "diff", theme.Current()), "\n"), 0, 0, nil)}
	for _, line := range strings.Split(diff, "\n") {
		if line == "" {
			continue
		}
		kind := line[0]
		if kind != '+' && kind != '-' && kind != ' ' {
			view.rows = nil
			break
		}
		rest := line[1:]
		start := 0
		for start < len(rest) && rest[start] == ' ' {
			start++
		}
		end := start
		for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
			end++
		}
		row := editDiffRow{kind: kind}
		if end > start {
			row.number, _ = strconv.Atoi(rest[start:end])
			if end > view.numberPad {
				view.numberPad = end
			}
			if end < len(rest) {
				row.text = rest[end+1:]
			}
		} else {
			row.kind = '.'
			row.text = strings.TrimLeft(rest, " ")
		}
		switch kind {
		case '+':
			view.added++
		case '-':
			view.removed++
		}
		view.rows = append(view.rows, row)
	}
	return view
}

func (view *editDiffView) Invalidate() { view.unified.Invalidate() }

func (view *editDiffView) Render(width int) []string {
	if width <= editDiffSplitWidth || len(view.rows) == 0 {
		return view.unified.Render(width)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.cacheWidth == width {
		return view.cacheLines
	}
	view.cacheWidth, view.cacheLines = width, view.renderSplit(width)
	return view.cacheLines
}

func (view *editDiffView) renderSplit(width int) []string {
	paneWidth := (width - 1) / 2
	textWidth := paneWidth - view.numberPad - 3
	if textWidth < 8 {
		return view.unified.Render(width)
	}
	divider := theme.FG("borderMuted", "│")
	lines := make([]string, 0, len(view.rows)+1)
	lines = append(lines,
		theme.FG("toolDiffAdded", "+"+strconv.Itoa(view.added))+" "+
			theme.FG("toolDiffRemoved", "-"+strconv.Itoa(view.removed)))

	// Change blocks pair consecutive removals with the additions that follow
	// them; context and elided rows flush the pending block and span both panes.
	var removedRows, addedRows []editDiffRow
	flush := func() {
		for index := 0; index < len(removedRows) || index < len(addedRows); index++ {
			left, right := "", ""
			if index < len(removedRows) {
				left = view.paneCell(removedRows[index], textWidth)
			} else {
				left = view.blankCell(textWidth)
			}
			if index < len(addedRows) {
				right = view.paneCell(addedRows[index], textWidth)
			} else {
				right = view.blankCell(textWidth)
			}
			lines = append(lines, left+divider+right)
		}
		removedRows, addedRows = removedRows[:0], addedRows[:0]
	}
	delta := 0
	for _, row := range view.rows {
		switch row.kind {
		case '-':
			removedRows = append(removedRows, row)
			delta--
		case '+':
			addedRows = append(addedRows, row)
			delta++
		default:
			flush()
			right := row
			if row.kind == ' ' {
				// Context rows carry the old-file number; the new-file side is
				// offset by the additions minus removals seen so far.
				right.number = row.number + delta
			}
			lines = append(lines, view.paneCell(row, textWidth)+divider+view.paneCell(right, textWidth))
		}
	}
	flush()
	return lines
}

// paneCell lays out one pane row: right-aligned line number, red/green mark,
// then the (truncated, padded) line text, styled by row kind.
func (view *editDiffView) paneCell(row editDiffRow, textWidth int) string {
	var number, mark string
	switch row.kind {
	case '.':
		number = strings.Repeat(" ", view.numberPad)
		mark = " "
	default:
		digits := strconv.Itoa(row.number)
		number = strings.Repeat(" ", view.numberPad-len(digits)) + digits
		switch row.kind {
		case '+':
			mark = "+"
		case '-':
			mark = "-"
		default:
			mark = " "
		}
	}
	text := tui.TruncateToWidth(row.text, textWidth, "...", true)
	switch row.kind {
	case '+':
		return theme.FG("muted", number) + " " + theme.FG("toolDiffAdded", mark+" "+text)
	case '-':
		return theme.FG("muted", number) + " " + theme.FG("toolDiffRemoved", mark+" "+text)
	case '.':
		return theme.FG("muted", number+" "+mark+" "+tui.TruncateToWidth(row.text, textWidth, "...", true))
	default:
		return theme.FG("muted", number) + " " + mark + " " + text
	}
}

func (view *editDiffView) blankCell(textWidth int) string {
	return strings.Repeat(" ", view.numberPad+3+textWidth)
}
