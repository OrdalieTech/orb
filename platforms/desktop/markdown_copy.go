package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// mdPart records the Markdown structure of one selectable text element rendered inside a turn,
// so CopyData can reconstruct exact Markdown from any partial or multi-block selection.
type mdPart struct {
	kind       string // "p", "h", "li", "code", "art", "cell", "you"
	level      int
	mark       string
	depth      int
	quoteDepth int
	lang       string
	spans      []Span
	text       string // raw text for code/art/you
	tableID    int
	row, col   int
}

type copyRecorder struct {
	parts     map[uint64]mdPart
	nextTable int
}

func newCopyRecorder() *copyRecorder {
	return &copyRecorder{parts: make(map[uint64]mdPart)}
}

func (r *copyRecorder) record(e ui.Element, p mdPart) ui.Element {
	if r != nil {
		r.parts[e.ID()] = p
	}
	return e
}

func (r *copyRecorder) copyData(selected []ui.SelectedText) transfer.Data {
	return transfer.TextData(r.format(selected))
}

func (r *copyRecorder) format(selected []ui.SelectedText) string {
	if len(selected) == 0 {
		return ""
	}
	type outBlock struct {
		kind string
		text string
	}
	var out []outBlock

	for i := 0; i < len(selected); {
		s := selected[i]
		p, ok := r.parts[s.ID]
		if !ok {
			if txt := sliceRunes(s.Text, s.Start, s.End); txt != "" {
				out = append(out, outBlock{kind: "raw", text: txt})
			}
			i++
			continue
		}

		// Group consecutive cells from the same table.
		if p.kind == "cell" {
			j := i
			for j < len(selected) {
				if next, ok := r.parts[selected[j].ID]; ok && next.kind == "cell" && next.tableID == p.tableID {
					j++
				} else {
					break
				}
			}
			cells := selected[i:j]
			if len(cells) == 1 {
				if txt := spansSliceToMarkdown(p.spans, s.Start, s.End); txt != "" {
					out = append(out, outBlock{kind: "p", text: quotePrefix(txt, p.quoteDepth)})
				}
			} else if tbl := r.formatTableSelection(cells); tbl != "" {
				out = append(out, outBlock{kind: "table", text: quotePrefix(tbl, p.quoteDepth)})
			}
			i = j
			continue
		}

		multi := len(selected) > 1
		switch p.kind {
		case "code", "art":
			sub := sliceRunes(s.Text, s.Start, s.End)
			total := len([]rune(s.Text))
			full := s.Start <= 0 && s.End >= total
			var body string
			if multi || full {
				lang := p.lang
				if p.kind == "art" && lang == "" {
					lang = "text"
				}
				body = fenceCode(lang, strings.TrimSuffix(sub, "\n"))
			} else {
				body = sub
			}
			out = append(out, outBlock{kind: "code", text: quotePrefix(body, p.quoteDepth)})
		case "h":
			body := spansSliceToMarkdown(p.spans, s.Start, s.End)
			if s.Start <= 0 || multi {
				lvl := max(1, min(6, p.level))
				body = strings.Repeat("#", lvl) + " " + body
			}
			out = append(out, outBlock{kind: "h", text: quotePrefix(body, p.quoteDepth)})
		case "li":
			body := spansSliceToMarkdown(p.spans, s.Start, s.End)
			if s.Start <= 0 || multi {
				mark := p.mark
				if mark == "" || mark == "·" {
					mark = "-"
				}
				body = strings.Repeat("  ", p.depth) + mark + " " + body
			}
			out = append(out, outBlock{kind: "li", text: quotePrefix(body, p.quoteDepth)})
		case "you":
			out = append(out, outBlock{kind: "p", text: sliceRunes(s.Text, s.Start, s.End)})
		default:
			body := spansSliceToMarkdown(p.spans, s.Start, s.End)
			out = append(out, outBlock{kind: "p", text: quotePrefix(body, p.quoteDepth)})
		}
		i++
	}

	var b strings.Builder
	for i, blk := range out {
		if i > 0 {
			if blk.kind == "li" && out[i-1].kind == "li" {
				b.WriteString("\n")
			} else {
				b.WriteString("\n\n")
			}
		}
		b.WriteString(blk.text)
	}
	return b.String()
}

func (r *copyRecorder) formatTableSelection(cells []ui.SelectedText) string {
	rowSet := map[int]bool{}
	colSet := map[int]bool{}
	grid := map[[2]int]string{}
	for _, c := range cells {
		p := r.parts[c.ID]
		rowSet[p.row] = true
		colSet[p.col] = true
		grid[[2]int{p.row, p.col}] = escapeTableCell(spansSliceToMarkdown(p.spans, c.Start, c.End))
	}
	rows := make([]int, 0, len(rowSet))
	for row := range rowSet {
		rows = append(rows, row)
	}
	slices.Sort(rows)
	cols := make([]int, 0, len(colSet))
	for col := range colSet {
		cols = append(cols, col)
	}
	slices.Sort(cols)
	if len(rows) == 0 || len(cols) == 0 {
		return ""
	}
	var lines []string
	for ri, row := range rows {
		vals := make([]string, len(cols))
		for ci, col := range cols {
			vals[ci] = grid[[2]int{row, col}]
		}
		lines = append(lines, "| "+strings.Join(vals, " | ")+" |")
		if ri == 0 && len(rows) > 1 {
			sep := make([]string, len(cols))
			for ci := range sep {
				sep[ci] = "---"
			}
			lines = append(lines, "| "+strings.Join(sep, " | ")+" |")
		}
	}
	return strings.Join(lines, "\n")
}

// tableToMarkdown formats a full table block as a clean GFM Markdown table.
func tableToMarkdown(rows [][][]Span) string {
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
	}
	var lines []string
	for ri, row := range rows {
		vals := make([]string, cols)
		for ci := range cols {
			if ci < len(row) {
				vals[ci] = escapeTableCell(spansToMarkdown(row[ci]))
			}
		}
		lines = append(lines, "| "+strings.Join(vals, " | ")+" |")
		if ri == 0 {
			sep := make([]string, cols)
			for ci := range sep {
				sep[ci] = "---"
			}
			lines = append(lines, "| "+strings.Join(sep, " | ")+" |")
		}
	}
	return strings.Join(lines, "\n")
}

// turnToMarkdown reconstructs clean Markdown for all prose blocks in a turn.
func turnToMarkdown(rows []Row) string {
	var blocks []Block
	for _, r := range rows {
		if r.Kind == "md" && r.Block != nil {
			blocks = append(blocks, *r.Block)
		}
	}
	return blocksToMarkdown(blocks)
}

func blocksToMarkdown(blocks []Block) string {
	var out []struct{ kind, text string }
	for _, b := range blocks {
		if txt := blockToMarkdown(b); txt != "" {
			out = append(out, struct{ kind, text string }{b.Type, txt})
		}
	}
	var buf strings.Builder
	for i, item := range out {
		if i > 0 {
			if item.kind == "li" && out[i-1].kind == "li" {
				buf.WriteString("\n")
			} else {
				buf.WriteString("\n\n")
			}
		}
		buf.WriteString(item.text)
	}
	return buf.String()
}

func blockToMarkdown(b Block) string {
	switch b.Type {
	case "p":
		return spansToMarkdown(b.Spans)
	case "h":
		lvl := max(1, min(6, b.Level))
		return strings.Repeat("#", lvl) + " " + spansToMarkdown(b.Spans)
	case "li":
		mark := b.Mark
		if mark == "" || mark == "·" {
			mark = "-"
		}
		return strings.Repeat("  ", b.Depth) + mark + " " + spansToMarkdown(b.Spans)
	case "quote":
		return quotePrefix(blocksToMarkdown(b.Blocks), 1)
	case "code", "art":
		lang := b.Lang
		if b.Type == "art" && lang == "" {
			lang = "text"
		}
		return fenceCode(lang, b.Text)
	case "table":
		return tableToMarkdown(b.Rows)
	case "rule":
		return "---"
	}
	return ""
}

func spansToMarkdown(ss []Span) string {
	total := 0
	for _, s := range ss {
		total += len([]rune(s.Text))
	}
	return spansSliceToMarkdown(ss, 0, total)
}

// spansSliceToMarkdown formats the rune range [start, end) across ss as clean Markdown.
func spansSliceToMarkdown(ss []Span, start, end int) string {
	if end <= start {
		return ""
	}
	var out strings.Builder
	pos := 0
	for _, s := range ss {
		runes := []rune(s.Text)
		next := pos + len(runes)
		lo := max(start, pos)
		hi := min(end, next)
		if lo < hi {
			out.WriteString(formatSpanMarkdown(s, string(runes[lo-pos:hi-pos])))
		}
		pos = next
	}
	return out.String()
}

func formatSpanMarkdown(s Span, text string) string {
	if text == "" {
		return ""
	}
	if s.Code {
		ticks := "`"
		for strings.Contains(text, ticks) {
			ticks += "`"
		}
		if strings.HasPrefix(text, "`") || strings.HasSuffix(text, "`") {
			text = " " + text + " "
		}
		inner := ticks + text + ticks
		if s.Href != "" {
			return fmt.Sprintf("[%s](%s)", inner, s.Href)
		}
		return inner
	}
	// Keep surrounding whitespace outside emphasis delimiters so Markdown remains valid.
	lead := len(text) - len(strings.TrimLeft(text, " \t\n"))
	trail := len(text) - len(strings.TrimRight(text, " \t\n"))
	if lead+trail >= len(text) {
		return text
	}
	core := text[lead : len(text)-trail]
	switch {
	case s.Bold && s.Italic:
		core = "***" + core + "***"
	case s.Bold:
		core = "**" + core + "**"
	case s.Italic:
		core = "*" + core + "*"
	}
	if s.Strike {
		core = "~~" + core + "~~"
	}
	if s.Href != "" {
		core = fmt.Sprintf("[%s](%s)", core, s.Href)
	}
	return text[:lead] + core + text[len(text)-trail:]
}

func fenceCode(lang, text string) string {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	return fence + lang + "\n" + text + "\n" + fence
}

func escapeTableCell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	s = strings.ReplaceAll(s, "\n", "<br>")
	return strings.TrimSpace(s)
}

func quotePrefix(text string, depth int) string {
	if depth <= 0 || text == "" {
		return text
	}
	prefix := strings.Repeat("> ", depth)
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if line == "" {
			lines[i] = strings.TrimRight(prefix, " ")
		} else {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func sliceRunes(s string, start, end int) string {
	runes := []rune(s)
	lo := max(0, min(start, len(runes)))
	hi := max(lo, min(end, len(runes)))
	return string(runes[lo:hi])
}
