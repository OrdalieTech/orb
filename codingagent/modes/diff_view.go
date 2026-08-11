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
// below it the rows lay out as a single unified column.
const editDiffSplitWidth = 120

// editDiffView renders one edit-tool display diff. The wire-format diff
// string (tools.GenerateDiffString) is the input and is never altered: the
// rows are re-laid opencode-fashion in both layouts — line numbers in a
// darker gutter band, bright +/- signs, and syntax-highlighted content over
// red/green background tints spanning the full row width; context rows stay
// on the surrounding tool-band background. Parsing and highlighting happen
// once at construction, and frames cache per width, so the per-frame path is
// a lookup.
type editDiffView struct {
	mu         sync.Mutex
	fallback   *tui.Text // unparsable diffs keep the legacy unified rendering
	rows       []editDiffRow
	styled     []string // highlighted row text, aligned with rows
	numberPad  int
	added      int
	removed    int
	bandBG     string // ANSI backgrounds captured at construction
	gutterBG   string
	addedBG    string
	removedBG  string
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
// a numberless "..." row. path picks the syntax-highlight language; bandRole
// names the tool-band background the view sits on — tinted spans re-open it
// instead of emitting \x1b[49m, because the band wrap only paints from column
// zero and a bare background reset would hole-punch it (the frame's final
// reset comes from that outer wrap).
func NewEditDiffView(diff, path, bandRole string) *editDiffView {
	view := &editDiffView{
		bandBG:    theme.BGANSI(bandRole),
		gutterBG:  theme.BGANSI("diffGutterBg"),
		addedBG:   theme.BGANSI("diffAddedBg"),
		removedBG: theme.BGANSI("diffRemovedBg"),
	}
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
	if len(view.rows) == 0 {
		view.fallback = tui.NewText(strings.Join(theme.Highlight(diff, "diff", theme.Current()), "\n"), 0, 0, nil)
		return view
	}
	view.styled = make([]string, len(view.rows))
	for index, row := range view.rows {
		view.styled[index] = row.text
	}
	// One highlight pass over the joined rows keeps lexer state across lines
	// (multi-line strings, block comments) and costs one tokenization.
	if language := theme.LanguageFromPath(path); language != "" && theme.Current() != nil {
		if styled := theme.Highlight(strings.Join(view.styled, "\n"), language, theme.Current()); len(styled) == len(view.rows) {
			view.styled = styled
		}
	}
	return view
}

func (view *editDiffView) Invalidate() {
	if view.fallback != nil {
		view.fallback.Invalidate()
	}
	view.mu.Lock()
	view.cacheWidth, view.cacheLines = 0, nil
	view.mu.Unlock()
}

func (view *editDiffView) Render(width int) []string {
	if view.fallback != nil {
		return view.fallback.Render(width)
	}
	view.mu.Lock()
	defer view.mu.Unlock()
	if view.cacheWidth == width {
		return view.cacheLines
	}
	lines := []string(nil)
	if width > editDiffSplitWidth {
		lines = view.renderSplit(width)
	}
	if lines == nil {
		lines = view.renderUnified(width)
	}
	view.cacheWidth, view.cacheLines = width, lines
	return view.cacheLines
}

func (view *editDiffView) header() string {
	return theme.FG("toolDiffAdded", "+"+strconv.Itoa(view.added)) + " " +
		theme.FG("toolDiffRemoved", "-"+strconv.Itoa(view.removed))
}

// bandTrail keeps the surrounding band alive to the frame edge; without a
// band it must close the tint itself so nothing bleeds into the next line.
func (view *editDiffView) bandTrail() string {
	if view.bandBG != "" {
		return view.bandBG
	}
	return "\x1b[49m"
}

// tintSpan opens a background under already fg-styled content and keeps it
// open across the full SGR resets that styling may contain, so the tint
// survives to the end of the span including padding cells.
func tintSpan(background, content string) string {
	if background == "" {
		return content
	}
	return background + tui.ReopenAfterReset(background, content)
}

// gutterCell renders the line-number column (numberPad plus one pad cell) on
// the gutter band; number <= 0 leaves the band blank.
func (view *editDiffView) gutterCell(number int) string {
	if number <= 0 {
		return tintSpan(view.gutterBG, strings.Repeat(" ", view.numberPad+1))
	}
	digits := strconv.Itoa(number)
	label := strings.Repeat(" ", view.numberPad-len(digits)) + digits + " "
	return tintSpan(view.gutterBG, theme.FG("muted", label))
}

func (view *editDiffView) rowBackground(kind byte) string {
	switch kind {
	case '+':
		return view.addedBG
	case '-':
		return view.removedBG
	default:
		return view.bandBG
	}
}

func rowSign(kind byte) string {
	switch kind {
	case '+':
		return theme.FG("toolDiffAdded", "+")
	case '-':
		return theme.FG("toolDiffRemoved", "-")
	default:
		return " "
	}
}

// paneCell lays out one row cell: gutter number, bright sign, then the
// truncated, padded content over the row background. Both layouts and the
// blank split filler share it so narrow and wide frames read identically.
func (view *editDiffView) paneCell(row editDiffRow, styled string, textWidth int) string {
	if row.kind == '.' {
		content := "  " + theme.FG("muted", tui.TruncateToWidth(row.text, textWidth, "...", true))
		return view.gutterCell(0) + tintSpan(view.bandBG, content)
	}
	content := rowSign(row.kind) + " " + tui.TruncateToWidth(styled, textWidth, "...", true)
	return view.gutterCell(row.number) + tintSpan(view.rowBackground(row.kind), content)
}

func (view *editDiffView) blankCell(textWidth int) string {
	return view.gutterCell(0) + tintSpan(view.bandBG, strings.Repeat(" ", textWidth+2))
}

// renderUnified lays the rows out as one full-width column; long content
// wraps onto continuation lines that keep the gutter band and the row tint.
func (view *editDiffView) renderUnified(width int) []string {
	textWidth := max(1, width-view.numberPad-3)
	trail := view.bandTrail()
	lines := make([]string, 0, len(view.rows)+1)
	lines = append(lines, view.header())
	for index, row := range view.rows {
		if row.kind == '.' {
			lines = append(lines, view.paneCell(row, "", textWidth)+trail)
			continue
		}
		background := view.rowBackground(row.kind)
		for part, wrapped := range tui.WrapTextWithANSI(view.styled[index], textWidth) {
			gutter, sign := view.gutterCell(0), " "
			if part == 0 {
				gutter, sign = view.gutterCell(row.number), rowSign(row.kind)
			}
			// Pads to the row width, and clamps the over-width line the wrapper
			// can emit for an unbreakable wide grapheme.
			cell := tui.TruncateToWidth(wrapped, textWidth, "", true)
			lines = append(lines, gutter+tintSpan(background, sign+" "+cell)+trail)
		}
	}
	return lines
}

// renderSplit pairs consecutive removals with the additions that follow them
// into old|new panes; context and elided rows flush the pending block and
// span both panes. It returns nil when the panes would be too narrow.
func (view *editDiffView) renderSplit(width int) []string {
	paneWidth := (width - 1) / 2
	textWidth := paneWidth - view.numberPad - 3
	if textWidth < 8 {
		return nil
	}
	divider := tintSpan(view.bandBG, theme.FG("borderMuted", "│"))
	trail := view.bandTrail()
	lines := make([]string, 0, len(view.rows)+1)
	lines = append(lines, view.header())

	var removedRows, addedRows []int
	flush := func() {
		for index := 0; index < len(removedRows) || index < len(addedRows); index++ {
			left, right := "", ""
			if index < len(removedRows) {
				left = view.paneCell(view.rows[removedRows[index]], view.styled[removedRows[index]], textWidth)
			} else {
				left = view.blankCell(textWidth)
			}
			if index < len(addedRows) {
				right = view.paneCell(view.rows[addedRows[index]], view.styled[addedRows[index]], textWidth)
			} else {
				right = view.blankCell(textWidth)
			}
			lines = append(lines, left+divider+right+trail)
		}
		removedRows, addedRows = removedRows[:0], addedRows[:0]
	}
	delta := 0
	for index, row := range view.rows {
		switch row.kind {
		case '-':
			removedRows = append(removedRows, index)
			delta--
		case '+':
			addedRows = append(addedRows, index)
			delta++
		default:
			flush()
			right := row
			if row.kind == ' ' {
				// Context rows carry the old-file number; the new-file side is
				// offset by the additions minus removals seen so far.
				right.number = row.number + delta
			}
			lines = append(lines, view.paneCell(row, view.styled[index], textWidth)+divider+view.paneCell(right, view.styled[index], textWidth)+trail)
		}
	}
	flush()
	return lines
}
