package tui

import "strings"

// TruncatedText renders only the first logical line, padded to the viewport.
type TruncatedText struct {
	text     string
	paddingX int
	paddingY int
}

func NewTruncatedText(text string, paddingX, paddingY int) *TruncatedText {
	return &TruncatedText{text: text, paddingX: paddingX, paddingY: paddingY}
}

func (text *TruncatedText) Invalidate() {}

func (text *TruncatedText) Render(width int) []string {
	empty := strings.Repeat(" ", max(0, width))
	lines := make([]string, 0, max(0, text.paddingY)*2+1)
	for range max(0, text.paddingY) {
		lines = append(lines, empty)
	}
	available := max(1, width-text.paddingX*2)
	value := text.text
	if index := strings.IndexByte(value, '\n'); index >= 0 {
		value = value[:index]
	}
	value = TruncateToWidth(value, available, "...", false)
	line := strings.Repeat(" ", max(0, text.paddingX)) + value + strings.Repeat(" ", max(0, text.paddingX))
	line += strings.Repeat(" ", max(0, width-VisibleWidth(line)))
	lines = append(lines, line)
	for range max(0, text.paddingY) {
		lines = append(lines, empty)
	}
	return lines
}
