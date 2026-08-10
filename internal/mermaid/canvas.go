package mermaid

import "strings"

// cont is the sentinel occupying the trailing column of a wide glyph. Never
// emitted: the line builder skips it so a CJK character claims two cells of
// layout but contributes one character of output.
const cont = "\x00"

// Connection direction bits, combined into a box-drawing glyph by maskChar.
const (
	bitU = 1
	bitD = 2
	bitL = 4
	bitR = 8
)

// Line styles, tracked per cell so crossing edges keep their own stroke.
const (
	styDot   = 1
	styThick = 2
	stySolid = 4
)

// canvas is a grid of cells. Edges accumulate as direction bits rather than
// glyphs so that crossings and junctions resolve correctly whatever order
// they are drawn in; finalizeMask turns the accumulated bits into characters
// at the end.
//
// occupied marks cells claimed by a box, which edge bits must not overwrite.
type canvas struct {
	w        int
	h        int
	ch       []string
	cls      []string
	mask     []uint8
	style    []uint8
	occupied []uint8
	curStyle uint8
}

func newCanvas(w, h int) *canvas {
	n := w * h
	c := &canvas{
		w: w, h: h,
		ch:       make([]string, n),
		cls:      make([]string, n),
		mask:     make([]uint8, n),
		style:    make([]uint8, n),
		occupied: make([]uint8, n),
		curStyle: stySolid,
	}
	for i := range n {
		c.ch[i] = " "
		c.cls[i] = clsNone
	}
	return c
}

func (c *canvas) idx(x, y int) int { return y*c.w + x }

func (c *canvas) set(x, y int, ch, cls string) {
	if x >= c.w || y >= c.h {
		return
	}
	i := c.idx(x, y)
	c.ch[i] = ch
	c.cls[i] = cls
}

// addBits accumulates direction bits on a free cell.
//
// cls is the class to claim the cell for; border cells are never
// reclassified, so a connector meeting a box keeps the box's styling.
func (c *canvas) addBits(x, y int, bits uint8, cls string) {
	if x >= c.w || y >= c.h {
		return
	}
	i := c.idx(x, y)
	if c.occupied[i] != 0 {
		return
	}
	c.mask[i] |= bits
	c.style[i] |= c.curStyle
	if c.cls[i] != clsBorder {
		c.cls[i] = cls
	}
}

// blit stamps a finished sub-canvas (a subgraph frame's contents) at an
// offset.
func (c *canvas) blit(sub *canvas, ox, oy int) {
	for sy := 0; sy < sub.h; sy++ {
		for sx := 0; sx < sub.w; sx++ {
			x := ox + sx
			y := oy + sy
			if x >= c.w || y >= c.h {
				continue
			}
			si := sub.idx(sx, sy)
			di := c.idx(x, y)
			c.ch[di] = sub.ch[si]
			c.cls[di] = sub.cls[si]
			c.style[di] = sub.style[si]
			c.occupied[di] = 1
		}
	}
}

// junction adds direction bits even to an occupied cell, so an edge can meet
// a border.
func (c *canvas) junction(x, y int, bits uint8) {
	if x >= c.w || y >= c.h {
		return
	}
	i := c.idx(x, y)
	c.mask[i] |= bits
	if c.cls[i] != clsBorder {
		c.cls[i] = clsEdge
	}
}

func (c *canvas) segV(x, y0, y1 int) {
	a, b := min(y0, y1), max(y0, y1)
	for y := a; y <= b; y++ {
		var bits uint8
		if y > a {
			bits |= bitU
		}
		if y < b {
			bits |= bitD
		}
		c.addBits(x, y, bits, clsEdge)
	}
}

func (c *canvas) segH(y, x0, x1 int) {
	a, b := min(x0, x1), max(x0, x1)
	for x := a; x <= b; x++ {
		var bits uint8
		if x > a {
			bits |= bitL
		}
		if x < b {
			bits |= bitR
		}
		c.addBits(x, y, bits, clsEdge)
	}
}

// finalizeMask resolves accumulated direction bits into glyphs, honouring
// line style.
func (c *canvas) finalizeMask() {
	for i := range c.ch {
		if c.mask[i] != 0 && c.ch[i] == " " {
			g := maskChar(c.mask[i])
			switch c.style[i] {
			case styDot:
				g = dottedChar(g)
			case styThick:
				g = thickChar(g)
			}
			c.ch[i] = g
		}
	}
}

// flipVertical mirrors top-to-bottom for `BT`. Rows reorder but within-row
// text does not, so labels stay readable; box-drawing glyphs flip to match.
func (c *canvas) flipVertical() {
	for y := 0; y < c.h/2; y++ {
		y2 := c.h - 1 - y
		for x := 0; x < c.w; x++ {
			i := c.idx(x, y)
			j := c.idx(x, y2)
			c.ch[i], c.ch[j] = c.ch[j], c.ch[i]
			c.cls[i], c.cls[j] = c.cls[j], c.cls[i]
		}
	}
	for i := range c.ch {
		c.ch[i] = flipGlyphV(c.ch[i])
	}
}

// flipHorizontal mirrors left-to-right for `RL`. Mirroring reverses each row,
// so after flipping glyphs each text/label run is reversed back to reading
// order.
func (c *canvas) flipHorizontal() {
	for y := 0; y < c.h; y++ {
		for x := 0; x < c.w/2; x++ {
			x2 := c.w - 1 - x
			i := c.idx(x, y)
			j := c.idx(x2, y)
			c.ch[i], c.ch[j] = c.ch[j], c.ch[i]
			c.cls[i], c.cls[j] = c.cls[j], c.cls[i]
		}
	}
	for i := range c.ch {
		c.ch[i] = flipGlyphH(c.ch[i])
	}
	for y := 0; y < c.h; y++ {
		x := 0
		for x < c.w {
			cls := c.cls[c.idx(x, y)]
			if cls == clsText || cls == clsEdgeLabel {
				start := c.idx(x, y)
				for x < c.w && c.cls[c.idx(x, y)] == cls {
					x++
				}
				end := c.idx(x, y)
				reverseSlice(c.ch, start, end)
			} else {
				x++
			}
		}
	}
}

// toLines groups each row into runs of one class, dropping wide-glyph
// continuations.
func (c *canvas) toLines() (plain []string, styled [][]Span, width int) {
	plain = []string{}
	styled = [][]Span{}
	for y := 0; y < c.h; y++ {
		// A trailing cont counts as painted: it is the second cell of a wide
		// glyph, so the row really does reach that column.
		last := 0
		for x := c.w - 1; x >= 0; x-- {
			if c.ch[c.idx(x, y)] != " " {
				last = x + 1
				break
			}
		}
		width = max(width, last)
		spans := []Span{}
		var plainRow strings.Builder
		var run strings.Builder
		runCls := clsNone
		for x := 0; x < last; x++ {
			i := c.idx(x, y)
			ch := c.ch[i]
			if ch == cont {
				continue
			}
			cls := c.cls[i]
			plainRow.WriteString(ch)
			if cls != runCls && run.Len() > 0 {
				spans = append(spans, Span{Cls: runCls, Text: run.String()})
				run.Reset()
			}
			runCls = cls
			run.WriteString(ch)
		}
		if run.Len() > 0 {
			spans = append(spans, Span{Cls: runCls, Text: run.String()})
		}
		styled = append(styled, spans)
		// Only ASCII spaces, which is all a blank cell ever holds. Trimming
		// all whitespace would eat a trailing NBSP that styled keeps,
		// desyncing the two.
		plain = append(plain, strings.TrimRight(plainRow.String(), " "))
	}
	first := 0
	for first < len(plain) && plain[first] == "" {
		first++
	}
	end := len(plain)
	for end > first && plain[end-1] == "" {
		end--
	}
	return plain[first:end], styled[first:end], width
}

func reverseSlice(arr []string, start, end int) {
	for i, j := start, end-1; i < j; i, j = i+1, j-1 {
		arr[i], arr[j] = arr[j], arr[i]
	}
}

// drawText paints text at x, y, one grapheme cluster per cell.
//
// A wide cluster claims a second cell, marked with cont so the line builder
// emits one character for it rather than a stray space.
func drawText(c *canvas, text string, x, y int, cls string) {
	cur := x
	for _, mc := range measured(text) {
		if mc.width == 0 {
			continue
		}
		c.set(cur, y, mc.cluster, cls)
		for k := 1; k < mc.width; k++ {
			c.set(cur+k, y, cont, cls)
		}
		cur += mc.width
	}
}

// drawTextOverEdges paints text at x, y, clearing any edge bits underneath
// first. Used where text sits on top of a drawn line (sequence messages,
// dividers, compartment rows) and must win over it.
func drawTextOverEdges(c *canvas, text string, x, y int, cls string) {
	cur := x
	for _, mc := range measured(text) {
		if mc.width == 0 {
			continue
		}
		for k := 0; k < mc.width; k++ {
			if cur+k < c.w && y < c.h {
				c.mask[c.idx(cur+k, y)] = 0
			}
			if k == 0 {
				c.set(cur+k, y, mc.cluster, cls)
			} else {
				c.set(cur+k, y, cont, cls)
			}
		}
		cur += mc.width
	}
}

func maskChar(mask uint8) string {
	switch mask {
	case 0:
		return " "
	case bitU, bitD, bitU | bitD:
		return "│"
	case bitL, bitR, bitL | bitR:
		return "─"
	case bitD | bitR:
		return "┌"
	case bitD | bitL:
		return "┐"
	case bitU | bitR:
		return "└"
	case bitU | bitL:
		return "┘"
	case bitU | bitD | bitR:
		return "├"
	case bitU | bitD | bitL:
		return "┤"
	case bitD | bitL | bitR:
		return "┬"
	case bitU | bitL | bitR:
		return "┴"
	default:
		return "┼"
	}
}

var dotted = map[string]string{"─": "╌", "│": "╎"}

var thick = map[string]string{
	"─": "━", "│": "┃", "┌": "┏", "┐": "┓", "└": "┗", "┘": "┛",
	"├": "┣", "┤": "┫", "┬": "┳", "┴": "┻", "┼": "╋",
}

var flipV = map[string]string{
	"┌": "└", "└": "┌", "┐": "┘", "┘": "┐",
	"┏": "┗", "┗": "┏", "┓": "┛", "┛": "┓",
	"╭": "╰", "╰": "╭", "╮": "╯", "╯": "╮",
	"┬": "┴", "┴": "┬", "┳": "┻", "┻": "┳",
	"▼": "▲", "▲": "▼", "▽": "△", "△": "▽",
}

var flipH = map[string]string{
	"┌": "┐", "┐": "┌", "└": "┘", "┘": "└",
	"┏": "┓", "┓": "┏", "┗": "┛", "┛": "┗",
	"╭": "╮", "╮": "╭", "╰": "╯", "╯": "╰",
	"├": "┤", "┤": "├", "┣": "┫", "┫": "┣",
	"▶": "◄", "◄": "▶", "▷": "◁", "◁": "▷",
}

func mapOr(m map[string]string, c string) string {
	if v, ok := m[c]; ok {
		return v
	}
	return c
}

func dottedChar(c string) string { return mapOr(dotted, c) }
func thickChar(c string) string  { return mapOr(thick, c) }
func flipGlyphV(c string) string { return mapOr(flipV, c) }
func flipGlyphH(c string) string { return mapOr(flipH, c) }
