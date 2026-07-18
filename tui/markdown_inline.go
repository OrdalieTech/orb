package tui

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
)

func (markdown *Markdown) renderInlineChildren(parent ast.Node, source []byte, context inlineStyleContext) string {
	var result strings.Builder
	var plain strings.Builder
	flushPlain := func() {
		if plain.Len() == 0 {
			return
		}
		result.WriteString(applyInlineText(plain.String(), context.apply))
		plain.Reset()
	}
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch typed := child.(type) {
		case *ast.Text:
			value := string(typed.Value(source))
			if !markdown.options.PreserveBackslashEscapes {
				value = escapablePunctuation.ReplaceAllString(value, `$1`)
			}
			plain.WriteString(value)
			if typed.SoftLineBreak() || typed.HardLineBreak() {
				plain.WriteByte('\n')
			}
			continue
		case *ast.String:
			plain.Write(typed.Value)
			continue
		}
		flushPlain()
		result.WriteString(markdown.renderInline(child, source, context))
	}
	flushPlain()
	value := result.String()
	for context.prefix != "" && strings.HasSuffix(value, context.prefix) {
		value = strings.TrimSuffix(value, context.prefix)
	}
	return value
}

func applyInlineText(value string, apply StyleFunc) string {
	parts := strings.Split(value, "\n")
	for index := range parts {
		parts[index] = apply(parts[index])
	}
	return strings.Join(parts, "\n")
}

func (markdown *Markdown) renderInline(node ast.Node, source []byte, context inlineStyleContext) string {
	applyLines := func(value string) string {
		parts := strings.Split(value, "\n")
		for index := range parts {
			parts[index] = context.apply(parts[index])
		}
		return strings.Join(parts, "\n")
	}
	switch typed := node.(type) {
	case *ast.Text:
		value := string(typed.Value(source))
		if !markdown.options.PreserveBackslashEscapes {
			value = escapablePunctuation.ReplaceAllString(value, `$1`)
		}
		if typed.SoftLineBreak() || typed.HardLineBreak() {
			value += "\n"
		}
		return applyLines(value)
	case *ast.String:
		return applyLines(string(typed.Value))
	case *ast.CodeSpan:
		return markdown.theme.Code(inlinePlainText(typed, source)) + context.prefix
	case *ast.Emphasis:
		value := markdown.renderInlineChildren(typed, source, context)
		if typed.Level == 2 {
			return markdown.theme.Bold(value) + context.prefix
		}
		return markdown.theme.Italic(value) + context.prefix
	case *extast.Strikethrough:
		return markdown.theme.Strikethrough(markdown.renderInlineChildren(typed, source, context)) + context.prefix
	case *ast.Link:
		return markdown.renderLink(markdown.renderInlineChildren(typed, source, context), string(typed.Destination), source, context)
	case *ast.AutoLink:
		return markdown.renderLink(string(typed.Label(source)), string(typed.URL(source)), source, context)
	case *ast.RawHTML:
		var value strings.Builder
		for index := 0; index < typed.Segments.Len(); index++ {
			segment := typed.Segments.At(index)
			value.Write(segment.Value(source))
		}
		return applyLines(value.String())
	case *ast.Image:
		return markdown.renderInlineChildren(typed, source, context)
	case *extast.TaskCheckBox:
		return ""
	default:
		if node.HasChildren() {
			return markdown.renderInlineChildren(node, source, context)
		}
		return ""
	}
}

func inlinePlainText(parent ast.Node, source []byte) string {
	var value strings.Builder
	for child := parent.FirstChild(); child != nil; child = child.NextSibling() {
		switch typed := child.(type) {
		case *ast.Text:
			value.Write(typed.Value(source))
		case *ast.String:
			value.Write(typed.Value)
		default:
			value.WriteString(inlinePlainText(child, source))
		}
	}
	return value.String()
}

func (markdown *Markdown) renderLink(label, destination string, _ []byte, context inlineStyleContext) string {
	styled := markdown.theme.Link(markdown.theme.Underline(label))
	if markdown.options.Hyperlinks {
		return "\x1b]8;;" + destination + "\x1b\\" + styled + "\x1b]8;;\x1b\\" + context.prefix
	}
	comparison := destination
	comparison = strings.TrimPrefix(comparison, "mailto:")
	if label == destination || label == comparison {
		return styled + context.prefix
	}
	return styled + markdown.theme.LinkURL(" ("+destination+")") + context.prefix
}
