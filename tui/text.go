package tui

import (
	"strings"
	"sync"
)

// Text renders wrapped multi-line text with optional padding and background.
type Text struct {
	mu         sync.RWMutex
	text       string
	paddingX   int
	paddingY   int
	background StyleFunc
	cacheText  string
	cacheWidth int
	cacheLines []string
}

func NewText(text string, paddingX, paddingY int, background StyleFunc) *Text {
	return &Text{text: text, paddingX: paddingX, paddingY: paddingY, background: background, cacheWidth: -1}
}

func (text *Text) SetText(value string) {
	text.mu.Lock()
	defer text.mu.Unlock()
	text.text = value
	text.cacheLines, text.cacheWidth = nil, -1
}

func (text *Text) SetBackground(background StyleFunc) {
	text.mu.Lock()
	defer text.mu.Unlock()
	text.background = background
	text.cacheLines, text.cacheWidth = nil, -1
}

func (text *Text) Invalidate() {
	text.mu.Lock()
	defer text.mu.Unlock()
	text.cacheLines, text.cacheWidth = nil, -1
}

func (text *Text) Render(width int) []string {
	text.mu.Lock()
	defer text.mu.Unlock()
	if text.cacheLines != nil && text.cacheText == text.text && text.cacheWidth == width {
		return text.cacheLines
	}
	if text.text == "" || strings.TrimSpace(text.text) == "" {
		text.cacheText, text.cacheWidth, text.cacheLines = text.text, width, []string{}
		return text.cacheLines
	}
	normalized := strings.ReplaceAll(text.text, "\t", "   ")
	contentWidth := max(1, width-text.paddingX*2)
	wrapped := WrapTextWithANSI(normalized, contentWidth)
	left, right := strings.Repeat(" ", max(0, text.paddingX)), strings.Repeat(" ", max(0, text.paddingX))
	content := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		line = left + line + right
		if text.background != nil {
			line = ApplyBackgroundToLine(line, width, text.background)
		} else {
			line += strings.Repeat(" ", max(0, width-VisibleWidth(line)))
		}
		content = append(content, line)
	}
	empty := strings.Repeat(" ", max(0, width))
	if text.background != nil {
		empty = ApplyBackgroundToLine(empty, width, text.background)
	}
	lines := make([]string, 0, len(content)+max(0, text.paddingY)*2)
	for range max(0, text.paddingY) {
		lines = append(lines, empty)
	}
	lines = append(lines, content...)
	for range max(0, text.paddingY) {
		lines = append(lines, empty)
	}
	text.cacheText, text.cacheWidth, text.cacheLines = text.text, width, lines
	return lines
}
