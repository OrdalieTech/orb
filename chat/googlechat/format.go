package googlechat

// format.go is the pure formatting pipeline: model markdown → Google Chat's
// text dialect → chunks that never split a code fence. No I/O and no
// adapter state; golden tests live under testdata/.

import (
	"regexp"
	"strings"
)

const maxMessageLen = 4096

// Leaves room under Chat's 4096-character cap for fence reopening.
const chunkLimit = 4000

const fenceMarker = "```"

var (
	reBold      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder = regexp.MustCompile(`__(.+?)__`)
	reItalic    = regexp.MustCompile(`\*([^*\s](?:[^*]*[^*\s])?)\*`)
	reStrike    = regexp.MustCompile(`~~(.+?)~~`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeading   = regexp.MustCompile(`^#{1,6}[ \t]+(.+)$`)
)

const boldSentinel = "\x00"

// FormatText converts common markdown to Google Chat's dialect: **b**/__b__
// → *b*, *i* → _i_, ~~s~~ → ~s~, [text](url) → <url|text>, headings →
// *bold* lines. Fenced code blocks are kept as triple-backtick blocks; the
// language token on the opening fence is dropped because Chat would display
// it literally. Lists, quotes, inline code, and plain underscores pass
// through unchanged (Chat renders them natively).
//
// ponytail: line/regex transform, not a goldmark AST walk; markup inside
// inline code spans is rewritten too — accepted ceiling, matching the
// WhatsApp adapter.
func FormatText(markdown string) string {
	lines := strings.Split(markdown, "\n")
	out := make([]string, 0, len(lines))
	inFence := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, fenceMarker) && isFenceMarker(trimmed, inFence) {
			inFence = !inFence
			out = append(out, fenceMarker)
			continue
		}
		if inFence {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, fenceMarker) {
			// Inline triple-backtick span with content on the same line:
			// downgrade to single backticks so no fence dangles.
			line = strings.ReplaceAll(line, fenceMarker, "`")
		}
		out = append(out, formatInline(line))
	}
	return strings.Join(out, "\n")
}

func isFenceMarker(trimmed string, inFence bool) bool {
	rest := trimmed[len(fenceMarker):]
	if rest == "" {
		return true
	}
	return !inFence && !strings.Contains(rest, "`") && len(strings.Fields(rest)) == 1
}

func formatInline(line string) string {
	line = reLink.ReplaceAllString(line, "<$2|$1>")
	line = reBold.ReplaceAllString(line, boldSentinel+"$1"+boldSentinel)
	line = reBoldUnder.ReplaceAllString(line, boldSentinel+"$1"+boldSentinel)
	line = reItalic.ReplaceAllString(line, "_${1}_") // ${1}: a bare $1_ would parse as group "1_"
	line = strings.ReplaceAll(line, boldSentinel, "*")
	line = reStrike.ReplaceAllString(line, "~$1~")
	if match := reHeading.FindStringSubmatch(line); match != nil {
		line = "*" + match[1] + "*"
	}
	return line
}

// ChunkText splits text into chunks of at most limit characters, preferring
// line boundaries and never splitting inside a code fence: the fence is
// closed at the chunk edge and reopened in the next chunk. A single line
// longer than the budget is hard-cut. Empty input yields no chunks.
func ChunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = chunkLimit
	}
	// Reserve room for the closing fence appended at a chunk edge.
	budget := limit - len(fenceMarker) - 1
	var chunks []string
	var current []string
	currentLen := 0
	inFence := false

	flush := func() {
		lines := current
		if inFence {
			lines = append(lines, fenceMarker)
		}
		joined := strings.TrimRight(strings.Join(lines, "\n"), "\n ")
		if strings.Trim(joined, "`\n ") != "" {
			chunks = append(chunks, joined)
		}
		current = nil
		currentLen = 0
		if inFence {
			current = []string{fenceMarker}
			currentLen = len(fenceMarker) + 1
		}
	}
	appendLine := func(line string) {
		lineLen := len([]rune(line))
		if currentLen+lineLen+1 > budget && currentLen > 0 {
			flush()
		}
		current = append(current, line)
		currentLen += lineLen + 1
	}

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		for len([]rune(line)) > budget {
			// Oversize single line: hard-cut at the budget.
			runes := []rune(line)
			appendLine(string(runes[:budget]))
			flush()
			line = string(runes[budget:])
		}
		appendLine(line)
		if strings.HasPrefix(trimmed, fenceMarker) && isFenceMarker(trimmed, inFence) {
			inFence = !inFence
		}
	}
	if len(current) > 0 {
		// A trailing unterminated fence (mid-stream truncation) is closed
		// by flush so the chunk renders as code instead of leaking the
		// fence into plain text.
		flush()
	}
	return chunks
}
