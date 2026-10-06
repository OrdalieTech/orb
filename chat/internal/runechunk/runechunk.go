// Package runechunk splits and cuts platform text at readable rune boundaries.
package runechunk

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Split cuts text into chunks of at most limit runes, preferring paragraph
// breaks, then line breaks, then spaces, then a hard cut. Empty input or a
// non-positive limit yields no chunks.
func Split(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	if len(text) <= limit && utf8.ValidString(text) {
		text = strings.TrimRight(text, "\n ")
		if text == "" {
			return nil
		}
		return []string{text}
	}
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= limit {
			if chunk := strings.TrimRight(string(runes), "\n "); chunk != "" {
				chunks = append(chunks, chunk)
			}
			break
		}
		cut := splitIndex(runes[:limit])
		if chunk := strings.TrimRight(string(runes[:cut]), "\n "); chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[cut:]
		for len(runes) > 0 && (runes[0] == '\n' || runes[0] == ' ') {
			runes = runes[1:]
		}
	}
	return chunks
}

func splitIndex(window []rune) int {
	for i := len(window) - 2; i > 0; i-- {
		if window[i] == '\n' && window[i+1] == '\n' {
			return i + 2
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	return len(window)
}

// Truncate cuts s to at most limit runes.
func Truncate(s string, limit int) string {
	if len(s) <= limit || utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// LenUTF16 counts text in UTF-16 code units, as required by platform limits.
func LenUTF16(s string) int { return measure(s, utf16.RuneLen) }

func measure(s string, width func(rune) int) int {
	n := 0
	for _, r := range s {
		n += width(r)
	}
	return n
}

// SplitLine cuts one line into pieces of at most limit units, measuring each
// rune with width, preferring the last space that fits and dropping the
// spaces around a cut. With html, a cut lands inside a <tag> or a '&…;'
// escape only when nothing else fits: a cut escape would surface as literal
// "amp;"-style text that no parse error catches.
func SplitLine(line string, limit int, width func(rune) int, html bool) []string {
	var pieces []string
	for measure(line, width) > limit {
		cut := cutIndex(line, limit, width, html)
		if piece := strings.TrimRight(line[:cut], " "); piece != "" {
			pieces = append(pieces, piece)
		}
		line = strings.TrimLeft(line[cut:], " ")
	}
	if line != "" || pieces == nil {
		pieces = append(pieces, line)
	}
	return pieces
}

// cutIndex returns the byte index to cut line at so the head fits limit: the
// last fitting space outside markup, else the last fitting boundary outside
// markup, else any fitting rune boundary, else after the first rune so a
// rune wider than the limit still makes progress.
func cutIndex(line string, limit int, width func(rune) int, html bool) int {
	units := 0
	inTag := false
	entityLen := 0 // runes since an unclosed '&' (0 = not in an entity)
	lastSpace, lastSafe, lastAny := 0, 0, 0
	for i, r := range line {
		if units += width(r); units > limit {
			break
		}
		end := i + utf8.RuneLen(r)
		lastAny = end
		if html {
			switch r {
			case '<':
				inTag = true
			case '>':
				inTag = false
			}
			switch {
			case r == '&':
				entityLen = 1
			case entityLen > 0:
				entityLen++
				// The longest escape emitted is 6 runes ("&quot;"); a run
				// exceeding that (or hitting a space) is a bare ampersand.
				if r == ';' || r == ' ' || entityLen > 6 {
					entityLen = 0
				}
			}
		}
		if !inTag && entityLen == 0 {
			lastSafe = end
			if r == ' ' {
				lastSpace = end
			}
		}
	}
	switch {
	case lastSpace > 0:
		return lastSpace
	case lastSafe > 0:
		return lastSafe
	case lastAny > 0:
		return lastAny
	}
	_, size := utf8.DecodeRuneInString(line)
	return size
}

// TruncateUTF16 cuts at a rune boundary within limit UTF-16 code units.
func TruncateUTF16(s string, limit int) string {
	units := 0
	for i, r := range s {
		width := utf16.RuneLen(r)
		if units+width > limit {
			return s[:i]
		}
		units += width
	}
	return s
}

// IsFence reports whether trimmed is a pure code-fence marker: bare ```
// always, or ``` plus a single language token when opening a fence. Closing
// fences carry no info string (CommonMark), and any line with more backticks
// or extra words is inline content, not a fence.
func IsFence(trimmed string, inFence bool) bool {
	rest, ok := strings.CutPrefix(trimmed, "```")
	return ok && (rest == "" || !inFence && !strings.Contains(rest, "`") && len(strings.Fields(rest)) == 1)
}
