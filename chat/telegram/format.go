package telegram

// format.go is the pure formatting pipeline: markdown → Telegram HTML blocks
// → chunks bounded by UTF-16 code units (the unit Telegram counts). No I/O
// and no adapter state; golden tests live under testdata/.

import (
	"fmt"
	"html"
	"strings"

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
	const separator = "\n\n"
	separatorLen := utf16Len(separator)
	var chunks []string
	var current []string
	currentLen := 0
	flush := func() {
		if len(current) > 0 {
			chunks = append(chunks, strings.Join(current, separator))
			current = nil
			currentLen = 0
		}
	}
	for _, block := range blocks {
		for _, piece := range splitBlock(block, limit) {
			need := utf16Len(piece)
			if len(current) > 0 && currentLen+separatorLen+need > limit {
				flush()
			}
			if len(current) > 0 {
				currentLen += separatorLen
			}
			current = append(current, piece)
			currentLen += need
		}
	}
	flush()
	return chunks
}

// splitBlock splits one block into pieces of at most limit UTF-16 code units.
// Pre blocks split at line boundaries with the pre tags closed and reopened
// on each side of the split.
func splitBlock(block htmlBlock, limit int) []string {
	if block.pre {
		return splitPreBlock(block, limit)
	}
	if utf16Len(block.html) <= limit {
		return []string{block.html}
	}
	var pieces []string
	var current []string
	currentLen := 0
	flush := func() {
		if len(current) > 0 {
			pieces = append(pieces, strings.Join(current, "\n"))
			current = nil
			currentLen = 0
		}
	}
	for _, line := range strings.Split(block.html, "\n") {
		for _, part := range splitLongLine(line, limit) {
			need := utf16Len(part)
			if len(current) > 0 && currentLen+1+need > limit {
				flush()
			}
			if len(current) > 0 {
				currentLen++
			}
			current = append(current, part)
			currentLen += need
		}
	}
	flush()
	return pieces
}

// splitPreBlock splits a code fence at line boundaries so that every piece,
// including its open and close tags, fits the limit.
func splitPreBlock(block htmlBlock, limit int) []string {
	whole := renderPre(block.lang, block.code)
	if utf16Len(whole) <= limit {
		return []string{whole}
	}
	overhead := utf16Len(preOpen(block.lang)) + utf16Len(preClose(block.lang))
	budget := max(limit-overhead, 1)
	var pieces []string
	var current []string
	currentLen := 0
	flush := func() {
		if len(current) > 0 {
			pieces = append(pieces, renderPre(block.lang, strings.Join(current, "\n")))
			current = nil
			currentLen = 0
		}
	}
	for _, line := range strings.Split(block.code, "\n") {
		for _, part := range splitLongLine(line, budget) {
			need := utf16Len(part)
			if len(current) > 0 && currentLen+1+need > budget {
				flush()
			}
			if len(current) > 0 {
				currentLen++
			}
			current = append(current, part)
			currentLen += need
		}
	}
	flush()
	return pieces
}

// splitLongLine cuts one line into pieces of at most limit UTF-16 code units,
// preferring space boundaries and never cutting inside a surrogate pair or an
// HTML tag.
// ponytail: hard cuts may still unbalance inline formatting tags on
// pathological single-line input; the plain-text resend fallback covers it.
func splitLongLine(line string, limit int) []string {
	if utf16Len(line) <= limit {
		return []string{line}
	}
	var pieces []string
	for utf16Len(line) > limit {
		cut := cutIndex(line, limit)
		pieces = append(pieces, strings.TrimRight(line[:cut], " "))
		line = strings.TrimLeft(line[cut:], " ")
	}
	if line != "" {
		pieces = append(pieces, line)
	}
	return pieces
}

// cutIndex returns the byte index to cut line at so the head fits limit
// UTF-16 code units: the last in-budget space outside a tag or entity when
// one exists, else the last in-budget rune boundary outside a tag or entity,
// else any in-budget rune boundary. '&…;' escape runs are atomic like tags:
// a cut inside one would surface as literal "amp;"-style garbage, and unlike
// a broken tag it is not a parse-entities error, so no fallback catches it.
func cutIndex(line string, limit int) int {
	units := 0
	inTag := false
	entityLen := 0 // runes since an unclosed '&' (0 = not in an entity)
	lastSpace, lastSafe, lastAny := 0, 0, 0
	for i, r := range line {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			break
		}
		units += width
		end := i + len(string(r))
		lastAny = end
		switch r {
		case '<':
			inTag = true
		case '>':
			inTag = false
		}
		switch {
		case r == '&':
			entityLen = 1
		case entityLen > 0:
			entityLen++
			// The longest escape emitted here is 6 runes ("&quot;"); a run
			// exceeding that (or hitting a space) is a bare ampersand.
			if r == ';' || r == ' ' || entityLen > 6 {
				entityLen = 0
			}
		}
		if !inTag && entityLen == 0 {
			lastSafe = end
			if r == ' ' {
				lastSpace = end
			}
		}
	}
	switch {
	case lastSpace > 0:
		return lastSpace
	case lastSafe > 0:
		return lastSafe
	case lastAny > 0:
		return lastAny
	default:
		// A single rune wider than the limit: emit it anyway to guarantee
		// progress.
		for i := range line {
			if i > 0 {
				return i
			}
		}
		return len(line)
	}
}

// utf16Len counts s in UTF-16 code units, the unit of Telegram's limits.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r > 0xFFFF {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// utf16Truncate cuts s to at most limit UTF-16 code units at a rune boundary.
func utf16Truncate(s string, limit int) string {
	units := 0
	for i, r := range s {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > limit {
			return s[:i]
		}
		units += width
	}
	return s
}
