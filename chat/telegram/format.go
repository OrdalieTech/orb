package telegram

// format.go is the pure formatting pipeline: markdown → Telegram HTML blocks
// → chunks bounded by UTF-16 code units (the unit Telegram counts). No I/O
// and no adapter state; golden tests live under testdata/.

import (
	"fmt"
	"html"
	"strings"
	"unicode/utf16"

	"github.com/OrdalieTech/orb/chat/internal/runechunk"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
)

// textLimit is Telegram's message text ceiling in UTF-16 code units.
const textLimit = 4096

// markdownParser is the shared goldmark instance (safe for concurrent use).
var markdownParser = goldmark.New(goldmark.WithExtensions(extension.Strikethrough))

// htmlBlock is one renderable block. Pre blocks keep their code and language
// separate so chunking can close and reopen the <pre> tags across splits.
type htmlBlock struct {
	html string // rendered HTML, non-pre blocks only
	pre  bool
	lang string // fence language, pre blocks only
	code string // HTML-escaped code content, pre blocks only
}

// formatHTML converts markdown to Telegram-HTML message chunks, each at most
// limit UTF-16 code units, split at paragraph and fence boundaries.
func formatHTML(markdown string, limit int) []string {
	return chunkBlocks(renderBlocks(markdown), limit)
}

// renderBlocks parses markdown and renders every top-level block.
func renderBlocks(markdown string) []htmlBlock {
	source := []byte(markdown)
	document := markdownParser.Parser().Parse(gtext.NewReader(source))
	var blocks []htmlBlock
	for node := document.FirstChild(); node != nil; node = node.NextSibling() {
		switch fence := node.(type) {
		case *ast.FencedCodeBlock:
			blocks = append(blocks, htmlBlock{
				pre:  true,
				lang: string(fence.Language(source)),
				code: html.EscapeString(blockLines(fence, source)),
			})
		case *ast.CodeBlock:
			blocks = append(blocks, htmlBlock{pre: true, code: html.EscapeString(blockLines(fence, source))})
		default:
			if rendered := renderBlockString(node, source); rendered != "" {
				blocks = append(blocks, htmlBlock{html: rendered})
			}
		}
	}
	return blocks
}

// renderBlockString renders any block node to its Telegram HTML string. Code
// fences nested below the top level (inside quotes or lists) render inline
// here.
// ponytail: nested fences join their parent block, so an oversize nested
// fence splits without tag reopening; the plain-text resend fallback covers
// the pathological case.
func renderBlockString(node ast.Node, source []byte) string {
	switch block := node.(type) {
	case *ast.Heading:
		return "<b>" + renderInlineChildren(block, source) + "</b>"
	case *ast.Paragraph, *ast.TextBlock:
		return renderInlineChildren(block, source)
	case *ast.FencedCodeBlock:
		return renderPre(string(block.Language(source)), html.EscapeString(blockLines(block, source)))
	case *ast.CodeBlock:
		return renderPre("", html.EscapeString(blockLines(block, source)))
	case *ast.Blockquote:
		var parts []string
		for child := block.FirstChild(); child != nil; child = child.NextSibling() {
			if rendered := renderBlockString(child, source); rendered != "" {
				parts = append(parts, rendered)
			}
		}
		return "<blockquote>" + strings.Join(parts, "\n") + "</blockquote>"
	case *ast.List:
		return renderList(block, source, 0)
	case *ast.ThematicBreak:
		return "———"
	case *ast.HTMLBlock:
		// Raw HTML degrades to visible escaped text.
		return html.EscapeString(blockLines(block, source))
	default:
		return renderInlineChildren(block, source)
	}
}

// renderList renders a (possibly nested) list as bullet or numbered lines.
func renderList(list *ast.List, source []byte, depth int) string {
	indent := strings.Repeat("  ", depth)
	number := list.Start
	if number == 0 {
		number = 1
	}
	var lines []string
	for item := list.FirstChild(); item != nil; item = item.NextSibling() {
		marker := "• "
		if list.IsOrdered() {
			marker = fmt.Sprintf("%d. ", number)
			number++
		}
		head := true
		for child := item.FirstChild(); child != nil; child = child.NextSibling() {
			if nested, ok := child.(*ast.List); ok {
				lines = append(lines, renderList(nested, source, depth+1))
				continue
			}
			content := renderBlockString(child, source)
			if content == "" {
				continue
			}
			prefix := indent
			if head {
				prefix += marker
				head = false
			} else {
				prefix += strings.Repeat(" ", len(marker))
			}
			lines = append(lines, prefix+content)
		}
		if head {
			lines = append(lines, indent+marker)
		}
	}
	return strings.Join(lines, "\n")
}

// renderInlineChildren renders the inline children of node.
func renderInlineChildren(node ast.Node, source []byte) string {
	var builder strings.Builder
	for child := node.FirstChild(); child != nil; child = child.NextSibling() {
		renderInlineNode(child, source, &builder)
	}
	return builder.String()
}

// renderInlineNode renders one inline node; anything Telegram cannot express
// degrades to escaped plain text.
func renderInlineNode(node ast.Node, source []byte, builder *strings.Builder) {
	switch inline := node.(type) {
	case *ast.Text:
		builder.WriteString(html.EscapeString(string(inline.Segment.Value(source))))
		if inline.HardLineBreak() || inline.SoftLineBreak() {
			builder.WriteByte('\n')
		}
	case *ast.String:
		builder.WriteString(html.EscapeString(string(inline.Value)))
	case *ast.CodeSpan:
		builder.WriteString("<code>")
		for child := inline.FirstChild(); child != nil; child = child.NextSibling() {
			if segment, ok := child.(*ast.Text); ok {
				builder.WriteString(html.EscapeString(string(segment.Segment.Value(source))))
			}
		}
		builder.WriteString("</code>")
	case *ast.Emphasis:
		tag := "i"
		if inline.Level >= 2 {
			tag = "b"
		}
		builder.WriteString("<" + tag + ">")
		builder.WriteString(renderInlineChildren(inline, source))
		builder.WriteString("</" + tag + ">")
	case *extast.Strikethrough:
		builder.WriteString("<s>")
		builder.WriteString(renderInlineChildren(inline, source))
		builder.WriteString("</s>")
	case *ast.Link:
		builder.WriteString(`<a href="` + html.EscapeString(string(inline.Destination)) + `">`)
		builder.WriteString(renderInlineChildren(inline, source))
		builder.WriteString("</a>")
	case *ast.AutoLink:
		url := html.EscapeString(string(inline.URL(source)))
		builder.WriteString(`<a href="` + url + `">` + html.EscapeString(string(inline.Label(source))) + "</a>")
	case *ast.Image:
		// Degrade to a link labeled with the alt text.
		href := html.EscapeString(string(inline.Destination))
		label := renderInlineChildren(inline, source)
		if label == "" {
			label = href
		}
		builder.WriteString(`<a href="` + href + `">` + label + "</a>")
	case *ast.RawHTML:
		for i := 0; i < inline.Segments.Len(); i++ {
			segment := inline.Segments.At(i)
			builder.WriteString(html.EscapeString(string(segment.Value(source))))
		}
	default:
		builder.WriteString(renderInlineChildren(inline, source))
	}
}

// blockLines concatenates a block node's raw source lines.
func blockLines(node ast.Node, source []byte) string {
	var builder strings.Builder
	lines := node.Lines()
	for i := 0; i < lines.Len(); i++ {
		line := lines.At(i)
		builder.Write(line.Value(source))
	}
	return strings.TrimRight(builder.String(), "\n")
}

// renderPre wraps escaped code in Telegram's pre tags.
func renderPre(lang, code string) string {
	return preOpen(lang) + code + preClose(lang)
}

func preOpen(lang string) string {
	if lang == "" {
		return "<pre>"
	}
	return `<pre><code class="language-` + html.EscapeString(lang) + `">`
}

func preClose(lang string) string {
	if lang == "" {
		return "</pre>"
	}
	return "</code></pre>"
}

// chunkBlocks packs blocks into chunks of at most limit UTF-16 code units,
// joined by blank lines, splitting oversize blocks as needed.
func chunkBlocks(blocks []htmlBlock, limit int) []string {
	var pieces []string
	for _, block := range blocks {
		pieces = append(pieces, splitBlock(block, limit)...)
	}
	return pack(pieces, "\n\n", limit)
}

// splitBlock splits one block into pieces of at most limit UTF-16 code units
// at line boundaries. Pre blocks keep their tags closed and reopened on each
// side of a split.
//
// ponytail: hard cuts may still unbalance inline formatting tags on
// pathological single-line input; the plain-text resend fallback covers it.
func splitBlock(block htmlBlock, limit int) []string {
	if !block.pre {
		if runechunk.LenUTF16(block.html) <= limit {
			return []string{block.html}
		}
		return pack(splitLines(block.html, limit), "\n", limit)
	}
	whole := renderPre(block.lang, block.code)
	if runechunk.LenUTF16(whole) <= limit {
		return []string{whole}
	}
	budget := max(limit-runechunk.LenUTF16(preOpen(block.lang)+preClose(block.lang)), 1)
	pieces := pack(splitLines(block.code, budget), "\n", budget)
	for i, piece := range pieces {
		pieces[i] = renderPre(block.lang, piece)
	}
	return pieces
}

func splitLines(text string, limit int) []string {
	var parts []string
	for _, line := range strings.Split(text, "\n") {
		parts = append(parts, runechunk.SplitLine(line, limit, utf16.RuneLen, true)...)
	}
	return parts
}

// pack joins pieces with sep into chunks of at most limit UTF-16 code units.
func pack(pieces []string, sep string, limit int) []string {
	var chunks, current []string
	currentLen := 0
	for _, piece := range pieces {
		need := runechunk.LenUTF16(piece)
		if len(current) > 0 && currentLen+len(sep)+need > limit {
			chunks = append(chunks, strings.Join(current, sep))
			current, currentLen = nil, 0
		}
		if len(current) > 0 {
			currentLen += len(sep)
		}
		current = append(current, piece)
		currentLen += need
	}
	if len(current) > 0 {
		chunks = append(chunks, strings.Join(current, sep))
	}
	return chunks
}
