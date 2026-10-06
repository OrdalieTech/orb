package teams

// format.go is the pure formatting pipeline: markdown → the Teams
// text-message subset → chunks bounded by UTF-16 code units. Teams text
// messages support bold, italic, inline/pre code, blockquotes, and links,
// but not headings, tables, images, or horizontal rules — those are
// downgraded. No I/O and no adapter state; golden tests live in testdata/.

import (
	"regexp"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/chat/internal/runechunk"
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
		if runechunk.IsFence(trimmed, inFence) {
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
			currentLen = runechunk.LenUTF16(reopen)
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
			currentLen += runechunk.LenUTF16(line)
		}
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		opens, closes := false, false
		if runechunk.IsFence(trimmed, inFence) {
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
			pieceBudget = budget - runechunk.LenUTF16("```"+lang) - 1
			if pieceBudget < 1 {
				pieceBudget = 1
			}
		}
		for _, piece := range runechunk.SplitLine(line, pieceBudget, utf16.RuneLen, false) {
			need := runechunk.LenUTF16(piece)
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
