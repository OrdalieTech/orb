package tui

import "strings"

// ReopenAfterReset re-opens style after every full SGR reset inside content.
// Styled content routinely contains \x1b[0m (highlighters close their runs
// with it; TruncateToWidth brackets its ellipsis with it), and a full reset
// closes the span style opened around that content too — leaving the rest of
// the span, padding cells included, unstyled: a background hole-punch or a
// dropped quote prefix. Callers emit the opening sequence themselves, since
// some prepend it and others wrap the result in a style function that already
// opens it.
func ReopenAfterReset(style, content string) string {
	if style == "" {
		return content
	}
	return strings.ReplaceAll(content, "\x1b[0m", "\x1b[0m"+style)
}
