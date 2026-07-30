package whatsapp

import (
	"regexp"
	"strings"

	"github.com/OrdalieTech/orb/chat/internal/runechunk"
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
	return runechunk.Split(text, limit)
}
