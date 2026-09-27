// Package qr encodes bytes as a QR code (ISO/IEC 18004: byte mode, error correction level L)
// so a phone can pair with a Bridge by photographing a terminal.
package qr

import "errors"

// Code is a square module matrix; true is dark.
type Code [][]bool

// level L per version: EC codewords per block, then (blocks, data codewords) for the two groups.
var levelL = [41][5]int{{},
	{7, 1, 19, 0, 0}, {10, 1, 34, 0, 0}, {15, 1, 55, 0, 0}, {20, 1, 80, 0, 0}, {26, 1, 108, 0, 0},
	{18, 2, 68, 0, 0}, {20, 2, 78, 0, 0}, {24, 2, 97, 0, 0}, {30, 2, 116, 0, 0}, {18, 2, 68, 2, 69},
	{20, 4, 81, 0, 0}, {24, 2, 92, 2, 93}, {26, 4, 107, 0, 0}, {30, 3, 115, 1, 116}, {22, 5, 87, 1, 88},
	{24, 5, 98, 1, 99}, {28, 1, 107, 5, 108}, {30, 5, 120, 1, 121}, {28, 3, 113, 4, 114}, {28, 3, 107, 5, 108},
	{28, 4, 116, 4, 117}, {28, 2, 111, 7, 112}, {30, 4, 121, 5, 122}, {30, 6, 117, 4, 118}, {26, 8, 106, 4, 107},
	{28, 10, 114, 2, 115}, {30, 8, 122, 4, 123}, {30, 3, 117, 10, 118}, {30, 7, 116, 7, 117}, {30, 5, 115, 10, 116},
	{30, 13, 115, 3, 116}, {30, 17, 115, 0, 0}, {30, 17, 115, 1, 116}, {30, 13, 115, 6, 116}, {30, 12, 121, 7, 122},
	{30, 6, 121, 14, 122}, {30, 17, 122, 4, 123}, {30, 4, 122, 18, 123}, {30, 20, 117, 4, 118}, {30, 19, 118, 6, 119},
}

// alignment pattern centres per version (versions 2+).
var alignment = [41][]int{2: {6, 18}, 3: {6, 22}, 4: {6, 26}, 5: {6, 30}, 6: {6, 34}, 7: {6, 22, 38}, 8: {6, 24, 42}, 9: {6, 26, 46},
	10: {6, 28, 50}, 11: {6, 30, 54}, 12: {6, 32, 58}, 13: {6, 34, 62}, 14: {6, 26, 46, 66}, 15: {6, 26, 48, 70}, 16: {6, 26, 50, 74},
	17: {6, 30, 54, 78}, 18: {6, 30, 56, 82}, 19: {6, 30, 58, 86}, 20: {6, 34, 62, 90}, 21: {6, 28, 50, 72, 94}, 22: {6, 26, 50, 74, 98},
	23: {6, 30, 54, 78, 102}, 24: {6, 28, 54, 80, 106}, 25: {6, 32, 58, 84, 110}, 26: {6, 30, 58, 86, 114}, 27: {6, 34, 62, 90, 118},
	28: {6, 26, 50, 74, 98, 122}, 29: {6, 30, 54, 78, 102, 126}, 30: {6, 26, 52, 78, 104, 130}, 31: {6, 30, 56, 82, 108, 134},
	32: {6, 34, 60, 86, 112, 138}, 33: {6, 30, 58, 86, 114, 142}, 34: {6, 34, 62, 90, 118, 146}, 35: {6, 30, 54, 78, 102, 126, 150},
	36: {6, 24, 50, 76, 102, 128, 154}, 37: {6, 28, 54, 80, 106, 132, 158}, 38: {6, 32, 58, 84, 110, 136, 162},
	39: {6, 26, 54, 82, 110, 138, 166}, 40: {6, 30, 58, 86, 114, 142, 170}}

// Encode returns the smallest level-L code holding data.
func Encode(data []byte) (Code, error) {
	for v := 1; v <= 40; v++ {
		t := levelL[v]
		capacity := t[1]*t[2] + t[3]*t[4]
		countBits := 8
		if v >= 10 {
			countBits = 16
		}
		if 4+countBits+8*len(data) <= 8*capacity {
			return build(v, codewords(v, data, countBits)), nil
		}
	}
	return nil, errors.New("qr: data too long")
}

type bits struct {
	b []byte
	n int
}

func (w *bits) put(value, width int) {
	for i := width - 1; i >= 0; i-- {
		if w.n%8 == 0 {
			w.b = append(w.b, 0)
		}
		if value>>i&1 == 1 {
			w.b[w.n/8] |= 0x80 >> (w.n % 8)
		}
		w.n++
	}
}

// codewords is the final interleaved data+EC stream for version v.
func codewords(v int, data []byte, countBits int) []byte {
	t := levelL[v]
	capacity := t[1]*t[2] + t[3]*t[4]
	var w bits
	w.put(0b0100, 4)
	w.put(len(data), countBits)
	for _, c := range data {
		w.put(int(c), 8)
	}
	w.put(0, min(4, 8*capacity-w.n))
	if w.n%8 != 0 {
		w.put(0, 8-w.n%8)
	}
	for pad := 0xEC; len(w.b) < capacity; pad ^= 0xEC ^ 0x11 {
		w.b = append(w.b, byte(pad))
	}
	var blocks, ecs [][]byte
	at := 0
	for group := 0; group < 2; group++ {
		for range t[1+2*group] {
			block := w.b[at : at+t[2+2*group]]
			at += len(block)
			blocks = append(blocks, block)
			ecs = append(ecs, reedSolomon(block, t[0]))
		}
	}
	var out []byte
	for _, set := range [][][]byte{blocks, ecs} {
		for i := 0; ; i++ {
			wrote := false
			for _, block := range set {
				if i < len(block) {
					out = append(out, block[i])
					wrote = true
				}
			}
			if !wrote {
				break
			}
		}
	}
	return out
}

var exp, log [512]byte

func init() {
	x := 1
	for i := 0; i < 255; i++ {
		exp[i], log[x] = byte(x), byte(i)
		if x <<= 1; x&0x100 != 0 {
			x ^= 0x11D
		}
	}
	for i := 255; i < 512; i++ {
		exp[i] = exp[i-255]
	}
}

func mul(a, b byte) byte {
	if a == 0 || b == 0 {
		return 0
	}
	return exp[int(log[a])+int(log[b])]
}

func reedSolomon(data []byte, n int) []byte {
	gen := []byte{1}
	for i := 0; i < n; i++ {
		next := make([]byte, len(gen)+1)
		for j, g := range gen {
			next[j] ^= g
			next[j+1] ^= mul(g, exp[i])
		}
		gen = next
	}
	rem := make([]byte, n)
	for _, d := range data {
		factor := d ^ rem[0]
		copy(rem, rem[1:])
		rem[n-1] = 0
		for j := range rem {
			rem[j] ^= mul(gen[j+1], factor)
		}
	}
	return rem
}

type grid struct {
	size        int
	dark, fixed [][]bool
}

func (g *grid) set(r, c int, dark bool) { g.dark[r][c], g.fixed[r][c] = dark, true }

func build(v int, stream []byte) Code {
	g := layout(v, stream)
	best, bestScore := Code(nil), -1
	for m := 0; m < 8; m++ {
		candidate := g.masked(m)
		if s := penalty(candidate); bestScore < 0 || s < bestScore {
			best, bestScore = candidate, s
		}
	}
	return best
}

// layout places function patterns and data, unmasked.
func layout(v int, stream []byte) *grid {
	size := 17 + 4*v
	g := &grid{size: size, dark: square(size), fixed: square(size)}
	for _, at := range [][2]int{{0, 0}, {0, size - 7}, {size - 7, 0}} {
		for r := -1; r <= 7; r++ {
			for c := -1; c <= 7; c++ {
				if rr, cc := at[0]+r, at[1]+c; rr >= 0 && rr < size && cc >= 0 && cc < size {
					ring := max(abs(r-3), abs(c-3))
					g.set(rr, cc, ring != 2 && ring != 4)
				}
			}
		}
	}
	for i := 8; i < size-8; i++ {
		g.set(6, i, i%2 == 0)
		g.set(i, 6, i%2 == 0)
	}
	last := len(alignment[v]) - 1
	for i, r := range alignment[v] {
		for j, c := range alignment[v] {
			if i == 0 && j == 0 || i == 0 && j == last || i == last && j == 0 {
				continue // these would sit on the finder patterns
			}
			for dr := -2; dr <= 2; dr++ {
				for dc := -2; dc <= 2; dc++ {
					g.set(r+dr, c+dc, max(abs(dr), abs(dc)) != 1)
				}
			}
		}
	}
	g.set(size-8, 8, true)
	g.format(0) // reserve the format areas before placing data
	if v >= 7 {
		g.version(v)
	}
	i := 0
	for right := size - 1; right >= 1; right -= 2 {
		if right == 6 {
			right = 5
		}
		for step := 0; step < size; step++ {
			r := step
			if (right+1)&2 == 0 {
				r = size - 1 - step
			}
			for _, c := range []int{right, right - 1} {
				if g.fixed[r][c] {
					continue
				}
				g.dark[r][c] = i < 8*len(stream) && stream[i/8]>>(7-i%8)&1 == 1
				i++
			}
		}
	}
	return g
}

func (g *grid) masked(m int) Code {
	out := &grid{size: g.size, dark: square(g.size), fixed: g.fixed}
	for r := range g.size {
		for c := range g.size {
			out.dark[r][c] = g.dark[r][c] != (!g.fixed[r][c] && flip(m, r, c))
		}
	}
	out.format(m)
	return out.dark
}

func flip(m, r, c int) bool {
	switch m {
	case 0:
		return (r+c)%2 == 0
	case 1:
		return r%2 == 0
	case 2:
		return c%3 == 0
	case 3:
		return (r+c)%3 == 0
	case 4:
		return (r/2+c/3)%2 == 0
	case 5:
		return r*c%2+r*c%3 == 0
	case 6:
		return (r*c%2+r*c%3)%2 == 0
	}
	return ((r+c)%2+r*c%3)%2 == 0
}

// format writes the 15-bit level-L format word for mask m, both copies.
func (g *grid) format(m int) {
	data := 0b01<<3 | m
	word := data << 10
	for i := 14; i >= 10; i-- {
		if word>>i&1 == 1 {
			word ^= 0x537 << (i - 10)
		}
	}
	word = (data<<10 | word) ^ 0x5412
	for i := 0; i < 15; i++ {
		dark := word>>i&1 == 1
		switch {
		case i < 6:
			g.set(i, 8, dark)
		case i < 8:
			g.set(i+1, 8, dark)
		default:
			if i == 8 {
				g.set(8, 7, dark)
			} else {
				g.set(8, 14-i, dark)
			}
		}
		if i < 8 {
			g.set(8, g.size-1-i, dark)
		} else {
			g.set(g.size-15+i, 8, dark)
		}
	}
	g.set(8, 8, word>>7&1 == 1)
}

func (g *grid) version(v int) {
	word := v << 12
	for i := 17; i >= 12; i-- {
		if word>>i&1 == 1 {
			word ^= 0x1F25 << (i - 12)
		}
	}
	word |= v << 12
	for i := 0; i < 18; i++ {
		dark := word>>i&1 == 1
		g.set(g.size-11+i%3, i/3, dark)
		g.set(i/3, g.size-11+i%3, dark)
	}
}

// penalty is the standard mask score (N1–N4); lower reads more reliably.
func penalty(q Code) int {
	n, score, darkCount := len(q), 0, 0
	for i := 0; i < n; i++ {
		for _, line := range [][]bool{q[i], column(q, i)} {
			run := 1
			for j := 1; j <= n; j++ {
				if j < n && line[j] == line[j-1] {
					run++
					continue
				}
				if run >= 5 {
					score += run - 2
				}
				run = 1
			}
			for j := 0; j+11 <= n; j++ {
				if matches(line[j:j+11], "10111010000") || matches(line[j:j+11], "00001011101") {
					score += 40
				}
			}
		}
		for j := 0; j < n; j++ {
			if q[i][j] {
				darkCount++
			}
			if i+1 < n && j+1 < n && q[i][j] == q[i][j+1] && q[i][j] == q[i+1][j] && q[i][j] == q[i+1][j+1] {
				score += 3
			}
		}
	}
	return score + abs(darkCount*20/(n*n)-10)*10
}

func matches(line []bool, pattern string) bool {
	for i, p := range pattern {
		if line[i] != (p == '1') {
			return false
		}
	}
	return true
}

func column(q Code, c int) []bool {
	out := make([]bool, len(q))
	for r := range q {
		out[r] = q[r][c]
	}
	return out
}

func square(n int) [][]bool {
	out := make([][]bool, n)
	for i := range out {
		out[i] = make([]bool, n)
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// Terminal renders two module rows per line with half blocks, dark on light and a quiet zone,
// in explicit ANSI colours so it scans on dark and light terminal themes alike.
func (q Code) Terminal() string {
	const light, dark = 7, 0 // ANSI white and black
	n := len(q) + 2          // a one-module quiet zone keeps version 15 inside 80 columns
	at := func(r, c int) bool {
		r, c = r-1, c-1
		return r >= 0 && c >= 0 && r < len(q) && c < len(q) && q[r][c]
	}
	var b []byte
	for r := 0; r < n; r += 2 {
		for c := 0; c < n; c++ {
			top, bottom := light, light
			if at(r, c) {
				top = dark
			}
			if r+1 < n && at(r+1, c) {
				bottom = dark
			}
			b = append(b, "\x1b[3"...)
			b = append(b, byte('0'+top))
			b = append(b, ";4"...)
			b = append(b, byte('0'+bottom))
			b = append(b, "m▀"...)
		}
		b = append(b, "\x1b[0m\n"...)
	}
	return string(b)
}
