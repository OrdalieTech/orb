package whatsapp

import (
	"regexp"
	"strings"
)

// maxMessageLen is the Cloud API text.body character limit.
const maxMessageLen = 4096

var (
	reBold      = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder = regexp.MustCompile(`__(.+?)__`)
	reStrike    = regexp.MustCompile(`~~(.+?)~~`)
	reLink      = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	reHeading   = regexp.MustCompile(`^#{1,6}[ \t]+(.+)$`)
)

// FormatText converts common markdown to WhatsApp markup: **b**/__b__ → *b*,
// ~~s~~ → ~s~, headings → *bold* lines, [text](url) → "text (url)". Fenced
// code blocks are kept as triple-backtick blocks (WhatsApp renders them as
// monospace); the language token on the opening fence is dropped because
// WhatsApp would display it literally.
//
// ponytail: line/regex transform, not a goldmark AST walk; single-asterisk
// and single-underscore emphasis pass through unchanged (md italic renders
// as WhatsApp bold/italic respectively — accepted ceiling).
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
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			// Inline triple-backtick span with content on the same line
			// (e.g. "```ls -la``` lists files"): downgrade to single
			// backticks so no content is lost and no fence dangles.
			line = strings.ReplaceAll(line, "```", "`")
		}
		out = append(out, formatInline(line))
	}
	return strings.Join(out, "\n")
}

// isFenceMarker reports whether trimmed (known to start with ```) is a pure
// fence marker: bare ``` always, or ``` plus a single language token when
// opening a fence. Closing fences carry no info string (CommonMark), and any
// line with more backticks or extra words is inline content, not a fence.
func isFenceMarker(trimmed string, inFence bool) bool {
	rest := trimmed[len("```"):]
	if rest == "" {
		return true
	}
	return !inFence && !strings.Contains(rest, "`") && len(strings.Fields(rest)) == 1
}

func formatInline(line string) string {
	line = reBold.ReplaceAllString(line, "*$1*")
	line = reBoldUnder.ReplaceAllString(line, "*$1*")
	line = reStrike.ReplaceAllString(line, "~$1~")
	line = reLink.ReplaceAllString(line, "$1 ($2)")
	if match := reHeading.FindStringSubmatch(line); match != nil {
		line = "*" + match[1] + "*"
	}
	return line
}

// ChunkText splits text into chunks of at most limit characters (runes),
// preferring paragraph breaks, then line breaks, then spaces, then a hard
// cut. Empty input yields no chunks.
//
// ponytail: chunk boundaries ignore code fences — a >4096-char fence splits
// mid-block; accepted ceiling.
func ChunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = maxMessageLen
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

// splitIndex picks the cut point inside one window: after the last
// paragraph break, else after the last newline, else after the last space,
// else the full window.
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
