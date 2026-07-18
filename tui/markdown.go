package tui

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

type DefaultTextStyle struct {
	Color         StyleFunc
	Background    StyleFunc
	Bold          bool
	Italic        bool
	Strikethrough bool
	Underline     bool
}

type MarkdownTheme struct {
	Heading         StyleFunc
	Link            StyleFunc
	LinkURL         StyleFunc
	Code            StyleFunc
	CodeBlock       StyleFunc
	CodeBlockBorder StyleFunc
	Quote           StyleFunc
	QuoteBorder     StyleFunc
	HorizontalRule  StyleFunc
	ListBullet      StyleFunc
	Bold            StyleFunc
	Italic          StyleFunc
	Strikethrough   StyleFunc
	Underline       StyleFunc
	HighlightCode   func(code, language string) []string
	CodeBlockIndent string
}

type MarkdownOptions struct {
	PreserveOrderedListMarkers bool
	PreserveBackslashEscapes   bool
	Hyperlinks                 bool
}

type inlineStyleContext struct {
	apply  StyleFunc
	prefix string
}

type Markdown struct {
	text         string
	paddingX     int
	paddingY     int
	theme        MarkdownTheme
	defaultStyle *DefaultTextStyle
	options      MarkdownOptions

	cachedText  string
	cachedWidth int
	cachedLines []string
	cached      bool
}

func NewMarkdown(text string, paddingX, paddingY int, theme MarkdownTheme, defaultStyle *DefaultTextStyle, options *MarkdownOptions) *Markdown {
	markdown := &Markdown{text: text, paddingX: paddingX, paddingY: paddingY, theme: normalizeMarkdownTheme(theme), defaultStyle: defaultStyle}
	if options != nil {
		markdown.options = *options
	}
	return markdown
}

func (markdown *Markdown) SetText(value string) {
	markdown.text = value
	markdown.Invalidate()
}

func (markdown *Markdown) Invalidate() {
	markdown.cached = false
	markdown.cachedLines = nil
}

func (markdown *Markdown) Render(width int) []string {
	if markdown.cached && markdown.cachedText == markdown.text && markdown.cachedWidth == width {
		return markdown.cachedLines
	}
	if strings.TrimSpace(markdown.text) == "" {
		markdown.cache(width, []string{})
		return markdown.cachedLines
	}

	contentWidth := max(1, width-markdown.paddingX*2)
	source := strings.ReplaceAll(markdown.text, "\t", "   ")
	if markdown.options.PreserveBackslashEscapes {
		source = protectBackslashEscapes(source)
	}
	contents := []byte(source)
	root := markdownParser().Parser().Parse(text.NewReader(contents))
	rendered := markdown.renderBlocks(root, contents, contentWidth, nil)

	wrapped := make([]string, 0, len(rendered))
	for _, line := range rendered {
		wrapped = append(wrapped, WrapTextWithANSI(line, contentWidth)...)
	}

	left, right := strings.Repeat(" ", markdown.paddingX), strings.Repeat(" ", markdown.paddingX)
	lines := make([]string, 0, len(wrapped)+markdown.paddingY*2)
	empty := strings.Repeat(" ", max(0, width))
	for range max(0, markdown.paddingY) {
		lines = append(lines, ApplyBackgroundToLine(empty, width, markdown.background()))
	}
	for _, line := range wrapped {
		line = restoreBackslashEscapes(left + line + right)
		lines = append(lines, ApplyBackgroundToLine(line, width, markdown.background()))
	}
	for range max(0, markdown.paddingY) {
		lines = append(lines, ApplyBackgroundToLine(empty, width, markdown.background()))
	}
	if len(lines) == 0 {
		lines = []string{""}
	}
	markdown.cache(width, lines)
	return markdown.cachedLines
}

func (markdown *Markdown) cache(width int, lines []string) {
	markdown.cachedText, markdown.cachedWidth = markdown.text, width
	markdown.cachedLines, markdown.cached = lines, true
}

func (markdown *Markdown) background() StyleFunc {
	if markdown.defaultStyle == nil {
		return nil
	}
	return markdown.defaultStyle.Background
}

func markdownParser() goldmark.Markdown {
	return goldmark.New(goldmark.WithExtensions(extension.Table, extension.Linkify, extension.TaskList), goldmark.WithParserOptions(
		parser.WithInlineParsers(util.Prioritized(strictStrikethroughParser{}, 500)),
	))
}

type strictStrikethroughProcessor struct{}

func (strictStrikethroughProcessor) IsDelimiter(value byte) bool { return value == '~' }
func (strictStrikethroughProcessor) CanOpenCloser(opener, closer *parser.Delimiter) bool {
	return opener.Char == closer.Char
}
func (strictStrikethroughProcessor) OnMatch(int) ast.Node { return extast.NewStrikethrough() }

type strictStrikethroughParser struct{}

func (strictStrikethroughParser) Trigger() []byte { return []byte{'~'} }
func (strictStrikethroughParser) Parse(_ ast.Node, reader text.Reader, context parser.Context) ast.Node {
	before := reader.PrecendingCharacter()
	line, segment := reader.PeekLine()
	delimiter := parser.ScanDelimiter(line, before, 2, strictStrikethroughProcessor{})
	if delimiter == nil || delimiter.OriginalLength != 2 || before == '~' {
		return nil
	}
	delimiter.Segment = segment.WithStop(segment.Start + 2)
	reader.Advance(2)
	context.PushDelimiter(delimiter)
	return delimiter
}
func (strictStrikethroughParser) CloseBlock(ast.Node, parser.Context) {}

const escapeSentinel = "\ue000"

var escapablePunctuation = regexp.MustCompile(`\\([!"#$%&'()*+,\-./:;<=>?@\[\\\]^_` + "`" + `{|}~])`)

func protectBackslashEscapes(value string) string {
	return escapablePunctuation.ReplaceAllString(value, escapeSentinel+`$1`)
}

func restoreBackslashEscapes(value string) string {
	return strings.ReplaceAll(value, escapeSentinel, `\`)
}

func normalizeMarkdownTheme(theme MarkdownTheme) MarkdownTheme {
	identity := func(value string) string { return value }
	for _, target := range []*StyleFunc{
		&theme.Heading, &theme.Link, &theme.LinkURL, &theme.Code, &theme.CodeBlock,
		&theme.CodeBlockBorder, &theme.Quote, &theme.QuoteBorder, &theme.HorizontalRule,
		&theme.ListBullet, &theme.Bold, &theme.Italic, &theme.Strikethrough, &theme.Underline,
	} {
		if *target == nil {
			*target = identity
		}
	}
	if theme.CodeBlockIndent == "" {
		theme.CodeBlockIndent = "  "
	}
	return theme
}

func (markdown *Markdown) applyDefaultStyle(value string) string {
	if markdown.defaultStyle == nil {
		return value
	}
	styled := value
	if markdown.defaultStyle.Color != nil {
		styled = markdown.defaultStyle.Color(styled)
	}
	if markdown.defaultStyle.Bold {
		styled = markdown.theme.Bold(styled)
	}
	if markdown.defaultStyle.Italic {
		styled = markdown.theme.Italic(styled)
	}
	if markdown.defaultStyle.Strikethrough {
		styled = markdown.theme.Strikethrough(styled)
	}
	if markdown.defaultStyle.Underline {
		styled = markdown.theme.Underline(styled)
	}
	return styled
}

func stylePrefix(style StyleFunc) string {
	const sentinel = "\x00"
	styled := style(sentinel)
	if index := strings.Index(styled, sentinel); index >= 0 {
		return styled[:index]
	}
	return ""
}

func (markdown *Markdown) defaultInlineContext() inlineStyleContext {
	return inlineStyleContext{apply: markdown.applyDefaultStyle, prefix: stylePrefix(markdown.applyDefaultStyle)}
}

func (markdown *Markdown) renderBlocks(parent ast.Node, source []byte, width int, style *inlineStyleContext) []string {
	lines := make([]string, 0)
	var previous ast.Node
	for node := parent.FirstChild(); node != nil; node = node.NextSibling() {
		if len(lines) > 0 && lines[len(lines)-1] != "" && (node.HasBlankPreviousLines() || blankLineBetween(previous, node, source)) {
			lines = append(lines, "")
		}
		lines = append(lines, markdown.renderBlock(node, source, width, node.NextSibling(), style)...)
		previous = node
	}
	return lines
}

func (markdown *Markdown) renderBlock(node ast.Node, source []byte, width int, next ast.Node, style *inlineStyleContext) []string {
	spacing := func(lines []string, exceptList bool) []string {
		if next != nil && (!exceptList || next.Kind() != ast.KindList) {
			return append(lines, "")
		}
		return lines
	}
	switch typed := node.(type) {
	case *ast.Heading:
		styleHeading := func(value string) string {
			if typed.Level == 1 {
				return markdown.theme.Heading(markdown.theme.Bold(markdown.theme.Underline(value)))
			}
			return markdown.theme.Heading(markdown.theme.Bold(value))
		}
		context := inlineStyleContext{apply: styleHeading, prefix: stylePrefix(styleHeading)}
		value := markdown.renderInlineChildren(typed, source, context)
		if typed.Level >= 3 {
			value = styleHeading(strings.Repeat("#", typed.Level)+" ") + value
		}
		return spacing([]string{value}, false)
	case *ast.Paragraph, *ast.TextBlock:
		context := markdown.resolveInlineContext(style)
		return spacing([]string{markdown.renderInlineChildren(node, source, context)}, true)
	case *ast.FencedCodeBlock:
		return spacing(markdown.renderFencedCodeBlock(typed, source), false)
	case *ast.CodeBlock:
		return spacing(markdown.renderCodeBlock(blockText(typed, source), ""), false)
	case *ast.List:
		return markdown.renderList(typed, source, 0, width, style)
	case *extast.Table:
		return spacing(markdown.renderTable(typed, source, width, style), false)
	case *ast.Blockquote:
		return spacing(markdown.renderBlockquote(typed, source, width), false)
	case *ast.ThematicBreak:
		return spacing([]string{markdown.theme.HorizontalRule(strings.Repeat("─", min(width, 80)))}, false)
	case *ast.HTMLBlock:
		return []string{markdown.applyDefaultStyle(strings.TrimSpace(htmlBlockText(typed, source)))}
	default:
		if node.Type() == ast.TypeInline {
			return []string{markdown.renderInline(node, source, markdown.resolveInlineContext(style))}
		}
		return markdown.renderBlocks(node, source, width, style)
	}
}

var blankLinePattern = regexp.MustCompile(`\n[ \t>]*\n`)

func blankLineBetween(previous, next ast.Node, source []byte) bool {
	if previous == nil || next == nil {
		return false
	}
	end, start := blockBoundary(previous, false), blockBoundary(next, true)
	if end < 0 || start < end || start > len(source) {
		return false
	}
	return blankLinePattern.Match(source[end:start])
}

func blockBoundary(node ast.Node, start bool) int {
	value := -1
	if node.Type() == ast.TypeBlock && node.Lines().Len() > 0 {
		if start {
			value = node.Lines().At(0).Start
		} else {
			value = node.Lines().At(node.Lines().Len() - 1).Stop
		}
	}
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		candidate := blockBoundary(child, start)
		if candidate < 0 {
			continue
		}
		if value < 0 || (start && candidate < value) || (!start && candidate > value) {
			value = candidate
		}
	}
	return value
}

func blockText(node ast.Node, source []byte) []byte {
	var result bytes.Buffer
	lines := node.Lines()
	for index := 0; index < lines.Len(); index++ {
		segment := lines.At(index)
		result.Write(segment.Value(source))
	}
	return bytes.TrimSuffix(result.Bytes(), []byte("\n"))
}

func htmlBlockText(block *ast.HTMLBlock, source []byte) string {
	var result bytes.Buffer
	result.Write(blockText(block, source))
	if block.HasClosure() {
		closure := block.ClosureLine
		result.Write(closure.Value(source))
	}
	return result.String()
}

func (markdown *Markdown) renderCodeBlock(code []byte, language string) []string {
	value := strings.TrimSuffix(string(code), "\n")
	lines := []string{markdown.theme.CodeBlockBorder("```" + language)}
	if markdown.theme.HighlightCode != nil {
		for _, line := range markdown.theme.HighlightCode(value, language) {
			lines = append(lines, markdown.theme.CodeBlockIndent+line)
		}
	} else {
		for _, line := range strings.Split(value, "\n") {
			lines = append(lines, markdown.theme.CodeBlockIndent+markdown.theme.CodeBlock(line))
		}
	}
	return append(lines, markdown.theme.CodeBlockBorder("```"))
}

func (markdown *Markdown) renderFencedCodeBlock(block *ast.FencedCodeBlock, source []byte) []string {
	value := strings.TrimSuffix(string(blockText(block, source)), "\n")
	marker, size := fencedMarker(block, source)
	parts := strings.Split(value, "\n")
	if len(parts) > 0 {
		last := parts[len(parts)-1]
		if len(last) > 0 && len(last) < size && strings.Trim(last, string(marker)) == "" && !hasClosingFence(block, source, marker, size) {
			parts = parts[:len(parts)-1]
			value = strings.Join(parts, "\n")
		}
	}
	return markdown.renderCodeBlock([]byte(value), string(block.Language(source)))
}

func hasClosingFence(block *ast.FencedCodeBlock, source []byte, marker byte, size int) bool {
	lines := block.Lines()
	if lines.Len() == 0 {
		return false
	}
	position := lines.At(lines.Len() - 1).Stop
	for position < len(source) && (source[position] == '\n' || source[position] == '\r') {
		position++
	}
	for position < len(source) && source[position] == ' ' {
		position++
	}
	count := 0
	for position+count < len(source) && source[position+count] == marker {
		count++
	}
	return count >= size
}

func fencedMarker(block *ast.FencedCodeBlock, source []byte) (byte, int) {
	position := 0
	if block.Info != nil {
		position = block.Info.Segment.Start
	} else if block.Lines().Len() > 0 {
		position = block.Lines().At(0).Start
	}
	lineStart := position
	for lineStart > 0 && source[lineStart-1] != '\n' {
		lineStart--
	}
	if lineStart == position && lineStart > 0 {
		lineStart--
		for lineStart > 0 && source[lineStart-1] != '\n' {
			lineStart--
		}
	}
	lineEnd := lineStart
	for lineEnd < len(source) && source[lineEnd] != '\n' {
		lineEnd++
	}
	line := strings.TrimLeft(string(source[lineStart:lineEnd]), " ")
	if line == "" || (line[0] != '`' && line[0] != '~') {
		return '`', 3
	}
	marker := line[0]
	size := 0
	for size < len(line) && line[size] == marker {
		size++
	}
	return marker, max(3, size)
}

func (markdown *Markdown) resolveInlineContext(context *inlineStyleContext) inlineStyleContext {
	if context != nil {
		return *context
	}
	return markdown.defaultInlineContext()
}
