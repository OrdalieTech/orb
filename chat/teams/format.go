package teams

// format.go is the pure formatting pipeline: markdown → the Teams
// text-message subset → chunks bounded by UTF-16 code units. Teams text
// messages support bold, italic, inline/pre code, blockquotes, and links,
// but not headings, tables, images, or horizontal rules — those are
// downgraded. No I/O and no adapter state; golden tests live in testdata/.

import (
	"regexp"
	"strings"
)

// Bot Framework text is capped conservatively in UTF-16 code units.
const chunkLimit = 28000

const minChunkLimit = 1024

var (
	reHeading = regexp.MustCompile(`^[ \t]{0,3}#{1,6}[ \t]+(.+?)[ \t#]*$`)
	reHRule   = regexp.MustCompile(`^[ \t]{0,3}(?:(?:-[ \t]*){3,}|(?:\*[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	reImage   = regexp.MustCompile(`!\[([^\]]*)\]\(`)
)

func formatText(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines)+4)
	inFence := false
	inTable := false
	closeTable := func() {
		if inTable {
			out = append(out, "```")
			inTable = false
		}
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") && isFenceMarker(trimmed, inFence) {
			closeTable()
			inFence = !inFence
			out = append(out, line)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "|") {
			// Tables are unsupported in text messages: render the raw rows
			// as a code block so the columns stay legible.
			if !inTable {
				out = append(out, "```")
				inTable = true
			}
			out = append(out, line)
			continue
		}
		closeTable()
		out = append(out, formatLine(line))
	}
	closeTable()
	return strings.Join(out, "\n")
}

func formatLine(line string) string {
	if match := reHeading.FindStringSubmatch(line); match != nil {
		return "**" + match[1] + "**"
	}
	if reHRule.MatchString(line) {
		return "———"
	}
	return reImage.ReplaceAllString(line, "[$1](")
}

func isFenceMarker(trimmed string, inFence bool) bool {
	rest := trimmed[len("```"):]
	if rest == "" {
		return true
	}
	return !inFence && !strings.Contains(rest, "`") && len(strings.Fields(rest)) == 1
}

func chunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = chunkLimit
	}
	var chunks []string
	var current []string
	currentLen := 0
	inFence := false
	fenceLang := ""

	emit := func(lines []string) {
		joined := strings.Join(lines, "\n")
		if strings.TrimSpace(joined) != "" {
			chunks = append(chunks, strings.Trim(joined, "\n"))
		}
	}
	flush := func() {
		if len(current) == 0 {
			return
		}
		if inFence {
			reopen := "```" + fenceLang
			if strings.TrimSpace(current[len(current)-1]) == reopen {
				// The fence just opened: carry the opener to the next chunk
				// instead of emitting an empty fence.
				emit(current[:len(current)-1])
			} else {
				// Close the fence in this chunk and reopen it in the next.
				emit(append(append([]string{}, current...), "```"))
			}
			current = []string{reopen}
			currentLen = utf16Len(reopen)
			return
		}
		// Prefer a paragraph boundary: split at the last blank line.
		cut := len(current)
		for i := len(current) - 1; i > 0; i-- {
			if strings.TrimSpace(current[i]) == "" {
				cut = i
				break
			}
		}
		emit(current[:cut])
		tail := append([]string{}, current[cut:]...)
		current = current[:0]
		currentLen = 0
		for _, line := range tail {
			if len(current) > 0 {
				currentLen++
			}
			current = append(current, line)
			currentLen += utf16Len(line)
		}
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		opens, closes := false, false
		if strings.HasPrefix(trimmed, "```") && isFenceMarker(trimmed, inFence) {
			opens, closes = !inFence, inFence
		}
		// The fence state flips only after the marker line is placed, so a
		// flush triggered by the marker itself still sees the old state.
		budget := limit
		pieceBudget := limit
		if inFence || opens {
			budget -= 4 // reserve room for the closing "```" line
			lang := fenceLang
			if opens {
				lang = trimmed[len("```"):]
			}
			// Fence content must also fit after a flush leaves the chunk
			// holding only the reopened fence marker.
			pieceBudget = budget - utf16Len("```"+lang) - 1
			if pieceBudget < 1 {
				pieceBudget = 1
			}
		}
		for _, piece := range splitLongLine(line, pieceBudget) {
			need := utf16Len(piece)
			// Each flush either empties current or strictly shrinks it, so
			// this loop terminates.
			for len(current) > 0 && currentLen+1+need > budget {
				before := len(current)
				flush()
				if len(current) == before {
					break
				}
			}
			if len(current) > 0 {
				currentLen++
			}
			current = append(current, piece)
			currentLen += need
		}
		if opens {
			inFence = true
			fenceLang = trimmed[len("```"):]
		}
		if closes {
			inFence = false
			fenceLang = ""
		}
	}
	emit(current)
	return chunks
}

func splitLongLine(line string, limit int) []string {
	if utf16Len(line) <= limit {
		return []string{line}
	}
	var pieces []string
	for utf16Len(line) > limit {
		cut := cutIndex(line, limit)
		pieces = append(pieces, strings.TrimRight(line[:cut], " "))
		line = strings.TrimLeft(line[cut:], " ")
	}
	if line != "" {
		pieces = append(pieces, line)
	}
	return pieces
}

func cutIndex(line string, limit int) int {
	units := 0
	lastSpace, lastAny := 0, 0
	for i, r := range line {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			break
		}
		units += width
		end := i + len(string(r))
		lastAny = end
		if r == ' ' {
			lastSpace = end
		}
	}
	switch {
	case lastSpace > 0:
		return lastSpace
	case lastAny > 0:
		return lastAny
	default:
		for i := range line {
			if i > 0 {
				return i
			}
		}
		return len(line)
	}
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func utf16Truncate(s string, limit int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			return s[:i]
		}
		units += width
	}
	return s
}
