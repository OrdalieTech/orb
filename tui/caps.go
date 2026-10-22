package tui

import (
	"strings"
	"time"
)

// capsGlyphs draws each capital on square pixels, a cell wide and half a row
// tall, so eight rows of '#' and '.' fill four terminal rows.
var capsGlyphs = map[rune]string{
	'A': ".#./#.#/#.#/###/#.#/#.#/#.#/#.#", 'B': "##./#.#/#.#/##./#.#/#.#/#.#/##.",
	'C': ".##/#../#../#../#../#../#../.##", 'D': "##./#.#/#.#/#.#/#.#/#.#/#.#/##.",
	'E': "###/#../#../###/#../#../#../###", 'F': "###/#../#../###/#../#../#../#..",
	'G': ".##/#../#../#../###/#.#/#.#/.##", 'H': "#.#/#.#/#.#/###/#.#/#.#/#.#/#.#",
	'I': "#/#/#/#/#/#/#/#", 'J': "..#/..#/..#/..#/..#/..#/#.#/.#.",
	'K': "#.#/#.#/#.#/##./##./#.#/#.#/#.#", 'L': "#../#../#../#../#../#../#../###",
	'M': "##.##/#.#.#/#.#.#/#...#/#...#/#...#/#...#/#...#", 'N': "#..#/##.#/##.#/##.#/#.##/#.##/#.##/#..#",
	'O': ".#./#.#/#.#/#.#/#.#/#.#/#.#/.#.", 'P': "##./#.#/#.#/##./#../#../#../#..",
	'Q': ".#../#.#./#.#./#.#./#.#./#.#./#.#./.###", 'R': "##./#.#/#.#/##./#.#/#.#/#.#/#.#",
	'S': ".##/#../#../.#./..#/..#/..#/##.", 'T': "###/.#./.#./.#./.#./.#./.#./.#.",
	'U': "#.#/#.#/#.#/#.#/#.#/#.#/#.#/###", 'V': "#.#/#.#/#.#/#.#/#.#/#.#/#.#/.#.",
	'W': "#...#/#...#/#...#/#...#/#.#.#/#.#.#/#.#.#/.#.#.", 'X': "#.#/#.#/#.#/.#./.#./#.#/#.#/#.#",
	'Y': "#.#/#.#/#.#/.#./.#./.#./.#./.#.", 'Z': "###/..#/..#/.#./.#./#../#../###",
	'0': "###/#.#/#.#/#.#/#.#/#.#/#.#/###", '1': ".#/##/.#/.#/.#/.#/.#/.#",
	'2': "###/..#/..#/###/#../#../#../###", '3': "###/..#/..#/###/..#/..#/..#/###",
	'4': "#.#/#.#/#.#/###/..#/..#/..#/..#", '5': "###/#../#../###/..#/..#/..#/###",
	'6': "###/#../#../###/#.#/#.#/#.#/###", '7': "###/..#/..#/..#/..#/..#/..#/..#",
	'8': "###/#.#/#.#/###/#.#/#.#/#.#/###", '9': "###/#.#/#.#/###/..#/..#/..#/###",
	'-': "../../../##/../../../..", '.': "./././././././#", ' ': "./././././././.",
}

// Caps draws text in capitals four rows tall and about a third as wide: a
// stem stays a whole cell and a lone horizontal stroke thins to an eighth
// block, as heavy verticals and hairlines do in a Mincho title. Runes without
// a glyph are dropped; it returns nil when nothing is left or the word is
// wider than width.
func Caps(text string, width int) []string {
	var pixels [][8]bool // columns
	for _, r := range strings.ToUpper(text) {
		glyph, ok := capsGlyphs[r]
		if !ok {
			continue
		}
		if len(pixels) > 0 {
			pixels = append(pixels, [8]bool{})
		}
		rows := strings.Split(glyph, "/")
		for x := range rows[0] {
			var column [8]bool
			for y, row := range rows {
				column[y] = row[x] == '#'
			}
			pixels = append(pixels, column)
		}
	}
	if len(pixels) == 0 || len(pixels) > width {
		return nil
	}
	at := func(x, y int) bool { return x >= 0 && x < len(pixels) && y >= 0 && y < 8 && pixels[x][y] }
	thin := func(x, y int) bool {
		if at(x, y-1) || at(x, y+1) || !at(x-1, y) && !at(x+1, y) {
			return false
		}
		// A stroke reaching its neighbor only corner to corner keeps its weight.
		for _, d := range [][2]int{{-1, -1}, {1, -1}, {-1, 1}, {1, 1}} {
			if at(x+d[0], y+d[1]) && !at(x+d[0], y) && !at(x, y+d[1]) {
				return false
			}
		}
		return true
	}
	lines := make([]string, 4)
	for row := range lines {
		var line strings.Builder
		for x := range pixels {
			top, bottom := at(x, 2*row), at(x, 2*row+1)
			switch {
			case top && bottom:
				line.WriteRune('█')
			case top && thin(x, 2*row):
				line.WriteRune('▔')
			case top:
				line.WriteRune('▀')
			case bottom && thin(x, 2*row+1):
				line.WriteRune('▁')
			case bottom:
				line.WriteRune('▄')
			default:
				line.WriteByte(' ')
			}
		}
		lines[row] = line.String()
	}
	return lines
}

const (
	capsReveal = 480 * time.Millisecond
	capsFrame  = 40 * time.Millisecond
	// A terminal shorter than capsMinRows keeps a title's rows for what it heads.
	capsMinRows = 40
)

// CapsTitle is a Caps word with a lowered hook mark after it. It first
// resolves from scrambled blocks, left to right, over capsReveal.
type CapsTitle struct {
	text      string
	ink, hook StyleFunc
	rows      func() int
	start     time.Time
}

// NewCapsTitle starts the reveal, asking request for each of its frames; with
// no request it shows the word at once. rows is the terminal's height.
func NewCapsTitle(text string, ink, hook StyleFunc, rows func() int, request func()) *CapsTitle {
	title := &CapsTitle{text: text, ink: ink, hook: hook, rows: rows, start: time.Now().Add(-capsReveal)}
	if request != nil {
		title.start = time.Now()
		go guarded(func() {
			for time.Since(title.start) < capsReveal {
				time.Sleep(capsFrame)
				request()
			}
		})()
	}
	return title
}

func (title *CapsTitle) Invalidate() {}

// Render falls back to one line of plain capitals where the word is too wide,
// and draws nothing on a short terminal.
func (title *CapsTitle) Render(width int) []string {
	if title.rows() < capsMinRows {
		return nil
	}
	lines := Caps(title.text, width-2)
	if lines == nil {
		return []string{title.ink(strings.ToUpper(title.text)) + " " + title.hook("▖")}
	}
	elapsed := time.Since(title.start)
	revealed := int(float64(len([]rune(lines[0]))) * float64(elapsed) / float64(capsReveal))
	frame := int(elapsed / capsFrame)
	const noise = "▖▗▘▝▚▞▙▛▜▟▀▄█"
	for row, line := range lines {
		cells := []rune(line)
		for x := revealed; x < len(cells); x++ {
			if cells[x] != ' ' {
				cells[x] = []rune(noise)[(x*7+row*13+frame*31)%len([]rune(noise))]
			}
		}
		lines[row] = title.ink(string(cells))
	}
	lines[3] += " " + title.hook("▖")
	return lines
}
