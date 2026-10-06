package whatsapp

import (
	"regexp"

	"github.com/OrdalieTech/orb/chat/internal/runechunk"
)

// maxMessageLen is the Cloud API text.body character limit; replies split at
// it in runes.
//
// ponytail: chunk boundaries ignore code fences — a >4096-char fence splits
// mid-block; accepted ceiling.
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
	return runechunk.FormatFenced(markdown, func(line string) string { return line }, formatInline)
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
