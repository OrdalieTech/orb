package tui

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// MarkdownCodeToken is a top-level fenced code block lexed from markdown
// source, shaped like an upstream marked lexer code token: [Start:End) is the
// token raw (including a lone trailing newline, which marked folds into the
// preceding token's raw), Info is the fence info string, and Text is the fence
// body without its trailing newline.
type MarkdownCodeToken struct {
	Start int
	End   int
	Info  string
	Text  string
}

// LexTopLevelCodeTokens returns the top-level fenced code blocks of a markdown
// document in source order. Fences nested in lists or quotes are not top-level
// tokens, and fences without an info string are omitted (they can never carry
// a language).
func LexTopLevelCodeTokens(source string) []MarkdownCodeToken {
	contents := []byte(source)
	document := markdownParser.Parser().Parse(text.NewReader(contents))
	var tokens []MarkdownCodeToken
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		block, ok := node.(*ast.FencedCodeBlock)
		if !ok || block.Info == nil {
			continue
		}
		infoSegment := block.Info.Segment
		start := infoSegment.Start
		for start > 0 && contents[start-1] != '\n' {
			start--
		}
		// Content starts after the opening fence line.
		contentStop := infoSegment.Stop
		for contentStop < len(contents) && contents[contentStop] != '\n' {
			contentStop++
		}
		if contentStop < len(contents) {
			contentStop++
		}
		if lines := block.Lines(); lines.Len() > 0 {
			contentStop = lines.At(lines.Len() - 1).Stop
		}
		// A top-level fence ends at EOF or at a closing fence line; content
		// segments stop before that line.
		end := contentStop
		for end < len(contents) && contents[end] != '\n' {
			end++
		}
		if spaceTokenLength(source, end) == 1 {
			end++
		}
		tokens = append(tokens, MarkdownCodeToken{
			Start: start,
			End:   end,
			Info:  string(infoSegment.Value(contents)),
			Text:  string(blockText(block, contents)),
		})
	}
	return tokens
}

// spaceTokenLength is the length marked's space token regex
// (?:[ \t]*(?:\n|$))+ matches at index; a length of exactly one means marked
// folds that spacer into the preceding token's raw.
func spaceTokenLength(source string, index int) int {
	position := index
	for {
		lineStart := position
		for position < len(source) && (source[position] == ' ' || source[position] == '\t') {
			position++
		}
		if position < len(source) && source[position] == '\n' {
			position++
			continue
		}
		if position >= len(source) {
			return position - index
		}
		return lineStart - index
	}
}
