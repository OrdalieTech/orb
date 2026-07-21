package slack

// format.go is the pure formatting pipeline: standard markdown → Slack
// mrkdwn, plus fence-aware chunking bounded by characters (the unit Slack
// counts). No I/O and no adapter state; golden tests live under testdata/.

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// chat.update rejects text over 4000 characters.
const textLimit = 4000

var (
	reBoldStars = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder = regexp.MustCompile(`__(.+?)__`)
	reStrike    = regexp.MustCompile(`~~(.+?)~~`)
	reItalic    = regexp.MustCompile(`\*([^*\n]+)\*`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeading   = regexp.MustCompile(`^#{1,6}[ \t]+(.+)$`)
	reBullet    = regexp.MustCompile(`^([ \t]*)[-*][ \t]+`)
)

const boldMark = "\x00"

// FormatText converts standard markdown to Slack mrkdwn: `**b**`/`__b__` →
// `*b*`, `*i*` → `_i_`, `~~s~~` → `~s~`, `[t](u)` → `<u|t>`, headings →
// `*bold*` lines, `- ` bullets → `• `, with `&`, `<`, `>` escaped in body
// text before any `<...>` construct is inserted. Fenced code blocks keep
// their triple-backtick markers (language tokens are dropped — mrkdwn would
// display them literally); fence content is escaped but otherwise untouched.
//
// ponytail: line/regex transform, not a goldmark AST walk; inline-code
// contents are transformed like normal text, and spaced single asterisks can
// over-italicize — accepted ceilings.
func FormatText(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") && isFenceMarker(trimmed, inFence) {
			inFence = !inFence
			out = append(out, "```")
			continue
		}
		if inFence {
			out = append(out, escapeText(line))
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			// Inline triple-backtick span with content on the same line:
			// downgrade to single backticks so no fence dangles.
			line = strings.ReplaceAll(line, "```", "`")
		}
		out = append(out, formatLine(line))
	}
	return strings.Join(out, "\n")
}

func isFenceMarker(trimmed string, inFence bool) bool {
	rest := trimmed[len("```"):]
	if rest == "" {
		return true
	}
	return !inFence && !strings.Contains(rest, "`") && len(strings.Fields(rest)) == 1
}

var mrkdwnEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

func escapeText(text string) string {
	return mrkdwnEscaper.Replace(text)
}

func formatLine(line string) string {
	quote := ""
	rest := line
	for {
		if rest == ">" {
			quote += ">"
			rest = ""
			break
		}
		if !strings.HasPrefix(rest, "> ") {
			break
		}
		quote += "> "
		rest = rest[2:]
	}
	rest = escapeText(rest)
	heading := false
	if match := reHeading.FindStringSubmatch(rest); match != nil {
		rest = match[1]
		heading = true
	}
	rest = reBullet.ReplaceAllString(rest, "$1• ")
	rest = reBoldStars.ReplaceAllString(rest, boldMark+"$1"+boldMark)
	rest = reBoldUnder.ReplaceAllString(rest, boldMark+"$1"+boldMark)
	rest = reStrike.ReplaceAllString(rest, "~$1~")
	rest = reItalic.ReplaceAllString(rest, "_${1}_") // ${1}: a bare $1_ would parse as group "1_"
	rest = reLink.ReplaceAllString(rest, "<$2|$1>")
	if heading {
		rest = boldMark + rest + boldMark
	}
	return quote + strings.ReplaceAll(rest, boldMark, "*")
}

// ChunkText splits text into chunks of at most limit characters, at line
// boundaries where possible (long lines split at spaces, then hard). Code
// fences are chunk-aware: a split inside a fence closes it with ``` and
// reopens ``` at the start of the next chunk. Empty input yields no chunks.
func ChunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = textLimit
	}
	var chunks []string
	var current []string
	currentLen := 0
	inFence := false
	flush := func() {
		if len(current) == 0 {
			return
		}
		if chunk := strings.TrimRight(strings.Join(current, "\n"), "\n "); chunk != "" {
			chunks = append(chunks, chunk)
		}
		current = nil
		currentLen = 0
	}
	add := func(piece string) {
		if len(current) > 0 {
			currentLen++ // the joining newline
		}
		current = append(current, piece)
		currentLen += utf8.RuneCountInString(piece)
	}
	for _, line := range strings.Split(text, "\n") {
		marker := strings.HasPrefix(strings.TrimSpace(line), "```")
		// Inside a fence, reserve room so any piece still fits a chunk that
		// both reopens and closes the fence around it ("```\n"+piece+"\n```").
		budget := limit
		if inFence {
			budget = max(limit-8, 1)
		}
		for _, piece := range splitLongLine(line, budget) {
			need := utf8.RuneCountInString(piece)
			separator := 0
			if len(current) > 0 {
				separator = 1
			}
			closeCost := 0
			if inFence {
				closeCost = 4 // "\n```" to close the fence before flushing
			}
			if len(current) > 0 && currentLen+separator+need+closeCost > limit {
				if inFence {
					add("```")
					flush()
					add("```")
				} else {
					flush()
				}
			}
			add(piece)
		}
		if marker {
			inFence = !inFence
		}
	}
	flush()
	return chunks
}

func splitLongLine(line string, limit int) []string {
	runes := []rune(line)
	if len(runes) <= limit {
		return []string{line}
	}
	var pieces []string
	for len(runes) > limit {
		cut := limit
		for i := limit; i > 0; i-- {
			if runes[i-1] == ' ' {
				cut = i
				break
			}
		}
		if piece := strings.TrimRight(string(runes[:cut]), " "); piece != "" {
			pieces = append(pieces, piece)
		}
		runes = runes[cut:]
		for len(runes) > 0 && runes[0] == ' ' {
			runes = runes[1:]
		}
	}
	if len(runes) > 0 {
		pieces = append(pieces, string(runes))
	}
	return pieces
}

func truncateRunes(s string, limit int) string {
	if len(s) <= limit || utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
