package tui

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

var sourceListMarker = regexp.MustCompile(`^(?: {0,3})(\d{1,9}[.)]|[-+*])(?:[ \t]+|$)`)

func (markdown *Markdown) renderList(list *ast.List, source []byte, depth, width int, style *inlineStyleContext) []string {
	lines := make([]string, 0)
	index := 0
	for child := list.FirstChild(); child != nil; child = child.NextSibling() {
		item, ok := child.(*ast.ListItem)
		if !ok {
			continue
		}
		marker := markdown.listMarker(list, item, source, index)
		indent := strings.Repeat("    ", depth)
		firstPrefix := indent + markdown.theme.ListBullet(marker)
		continuation := indent + strings.Repeat(" ", VisibleWidth(marker))
		itemWidth := max(1, width-VisibleWidth(firstPrefix))
		rendered := false
		for block := item.FirstChild(); block != nil; block = block.NextSibling() {
			if nested, ok := block.(*ast.List); ok {
				lines = append(lines, markdown.renderList(nested, source, depth+1, width, style)...)
				rendered = true
				continue
			}
			blockLines := markdown.renderBlock(block, source, itemWidth, block.NextSibling(), style)
			for _, line := range blockLines {
				for _, wrapped := range WrapTextWithANSI(line, itemWidth) {
					prefix := continuation
					if !rendered {
						prefix = firstPrefix
					}
					lines = append(lines, prefix+wrapped)
					rendered = true
				}
			}
		}
		if !rendered {
			lines = append(lines, firstPrefix)
		}
		if !list.IsTight && item.NextSibling() != nil {
			lines = append(lines, "")
		}
		index++
	}
	return lines
}

func (markdown *Markdown) listMarker(list *ast.List, item *ast.ListItem, source []byte, index int) string {
	task := ""
	if checked, ok := listTask(item); ok {
		if checked {
			task = "[x] "
		} else {
			task = "[ ] "
		}
	}
	if markdown.options.PreserveOrderedListMarkers {
		if raw := rawListMarker(item, source); raw != "" {
			return raw + " " + task
		}
	}
	if list.IsOrdered() {
		return strings.TrimSpace(strings.Join([]string{integerString(list.Start + index), string(list.Marker)}, "")) + " " + task
	}
	marker := "-"
	if markdown.options.PreserveOrderedListMarkers && list.Marker != 0 {
		marker = string(list.Marker)
	}
	return marker + " " + task
}

func listTask(parent ast.Node) (bool, bool) {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		if task, ok := node.(*extast.TaskCheckBox); ok {
			return task.IsChecked, true
		}
		if checked, ok := listTask(node); ok {
			return checked, true
		}
	}
	return false, false
}

func rawListMarker(item *ast.ListItem, source []byte) string {
	first := firstBlockLineStart(item)
	if first < 0 || first > len(source) {
		return ""
	}
	lineStart := first
	for lineStart > 0 && source[lineStart-1] != '\n' {
		lineStart--
	}
	match := sourceListMarker.FindSubmatch(source[lineStart:first])
	if len(match) != 2 {
		return ""
	}
	return string(match[1])
}

func firstBlockLineStart(parent ast.Node) int {
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		if node.Type() == ast.TypeBlock && node.Lines().Len() > 0 {
			return node.Lines().At(0).Start
		}
		if value := firstBlockLineStart(node); value >= 0 {
			return value
		}
	}
	return -1
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	digits := [24]byte{}
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

func (markdown *Markdown) renderBlockquote(quote *ast.Blockquote, source []byte, width int) []string {
	quoteStyle := func(value string) string { return markdown.theme.Quote(markdown.theme.Italic(value)) }
	prefix := stylePrefix(quoteStyle)
	context := inlineStyleContext{apply: func(value string) string { return value }, prefix: prefix}
	contentWidth := max(1, width-2)
	inner := markdown.renderBlocks(quote, source, contentWidth, &context)
	for len(inner) > 0 && inner[len(inner)-1] == "" {
		inner = inner[:len(inner)-1]
	}
	lines := make([]string, 0, len(inner))
	for _, line := range inner {
		if prefix != "" {
			line = strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+prefix)
		}
		line = quoteStyle(line)
		for _, wrapped := range WrapTextWithANSI(line, contentWidth) {
			lines = append(lines, markdown.theme.QuoteBorder("│ ")+wrapped)
		}
	}
	return lines
}
