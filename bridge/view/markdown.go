package view

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/internal/mermaid"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Block is one block of markdown an app lays out: a paragraph (p), heading (h), list item (li),
// quote, code, a drawn diagram (art), table or rule. Items of nested lists and what follows
// their first paragraph carry a Depth; a quote holds blocks of its own.
type Block struct {
	Type   string     `json:"type"`
	Level  int        `json:"level,omitempty"`
	Mark   string     `json:"mark,omitempty"`
	Depth  int        `json:"depth,omitempty"`
	Spans  []Span     `json:"spans,omitempty"`
	Lang   string     `json:"lang,omitempty"`
	Text   string     `json:"text,omitempty"`
	Blocks []Block    `json:"blocks,omitempty"`
	Rows   [][][]Span `json:"rows,omitempty"` // the first row is the header
}

// Span is a run of text in one style.
type Span struct {
	Text   string `json:"t"`
	Bold   bool   `json:"b,omitempty"`
	Italic bool   `json:"i,omitempty"`
	Code   bool   `json:"c,omitempty"`
	Strike bool   `json:"s,omitempty"`
	Href   string `json:"h,omitempty"`
}

// ponytail: goldmark keeps parse state per call, so one parser is safe to share.
var markdown = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList, extension.Linkify)).Parser()

// Blocks parses what a model wrote. A Mermaid block is drawn once its message is [done]; while
// it streams, and when it does not parse, it stays code.
func Blocks(md string, done bool) []Block {
	src := []byte(md)
	return children(markdown.Parse(text.NewReader(src)), src, 0, done)
}

func children(n ast.Node, src []byte, depth int, done bool) []Block {
	var out []Block
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, block(c, src, depth, done)...)
	}
	return out
}

func block(n ast.Node, src []byte, depth int, done bool) []Block {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		return []Block{{Type: "p", Depth: depth, Spans: spans(n, src)}}
	case *ast.Heading:
		return []Block{{Type: "h", Level: n.Level, Depth: depth, Spans: spans(n, src)}}
	case *ast.ThematicBreak:
		return []Block{{Type: "rule", Depth: depth}}
	case *ast.Blockquote:
		return []Block{{Type: "quote", Depth: depth, Blocks: children(n, src, 0, done)}}
	case *ast.FencedCodeBlock:
		lang, code := string(n.Language(src)), raw(n, src)
		if lang == "mermaid" && done {
			if art := mermaid.Render(code); art != nil {
				return []Block{{Type: "art", Depth: depth, Text: strings.Join(art.Plain, "\n")}}
			}
		}
		return []Block{{Type: "code", Lang: lang, Depth: depth, Text: code}}
	case *ast.CodeBlock:
		return []Block{{Type: "code", Depth: depth, Text: raw(n, src)}}
	case *ast.HTMLBlock:
		return []Block{{Type: "p", Depth: depth, Spans: []Span{{Text: raw(n, src)}}}}
	case *ast.List:
		var out []Block
		number := n.Start
		for item := n.FirstChild(); item != nil; item = item.NextSibling() {
			li := Block{Type: "li", Mark: "·", Depth: depth}
			if n.IsOrdered() {
				li.Mark, number = strconv.Itoa(number)+string(n.Marker), number+1
			}
			c := item.FirstChild()
			if _, ok := c.(*ast.Paragraph); ok {
				li.Spans, c = spans(c, src), c.NextSibling()
			} else if _, ok := c.(*ast.TextBlock); ok {
				li.Spans, c = spans(c, src), c.NextSibling()
			}
			out = append(out, li)
			for ; c != nil; c = c.NextSibling() {
				out = append(out, block(c, src, depth+1, done)...)
			}
		}
		return out
	case *extast.Table:
		table := Block{Type: "table", Depth: depth}
		for row := n.FirstChild(); row != nil; row = row.NextSibling() {
			var cells [][]Span
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				cells = append(cells, spans(cell, src))
			}
			table.Rows = append(table.Rows, cells)
		}
		return []Block{table}
	}
	return nil
}

// web is [href] when it leads to the web (or one of [also]'s schemes), "" otherwise: what a model
// writes, or a peer sends, never points an app at a file, a script or another app.
func web(href string, also ...string) string {
	u, err := url.Parse(strings.TrimSpace(href))
	if err != nil || u.Host == "" && u.Opaque == "" || !slices.Contains(append([]string{"http", "https"}, also...), strings.ToLower(u.Scheme)) {
		return ""
	}
	return u.String()
}

// raw is a block's own lines, as written.
func raw(n ast.Node, src []byte) string {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		line := n.Lines().At(i)
		b.Write(line.Value(src))
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// spans flattens a block's inline content into styled runs; a line break inside a paragraph is
// kept, as chat writers mean it.
func spans(n ast.Node, src []byte) []Span {
	var out []Span
	add := func(style Span, s string) {
		if s == "" {
			return
		}
		if last := len(out) - 1; last >= 0 {
			if prev := out[last]; prev.Bold == style.Bold && prev.Italic == style.Italic && prev.Code == style.Code && prev.Strike == style.Strike && prev.Href == style.Href {
				out[last].Text += s
				return
			}
		}
		style.Text = s
		out = append(out, style)
	}
	var walk func(n ast.Node, style Span)
	walk = func(n ast.Node, style Span) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Text:
				add(style, string(c.Segment.Value(src)))
				if c.SoftLineBreak() || c.HardLineBreak() {
					add(style, "\n")
				}
			case *ast.String:
				add(style, string(c.Value))
			case *ast.CodeSpan:
				code := style
				code.Code = true
				var b strings.Builder
				for t := c.FirstChild(); t != nil; t = t.NextSibling() {
					if t, ok := t.(*ast.Text); ok {
						b.Write(t.Segment.Value(src))
					}
				}
				add(code, b.String())
			case *ast.Emphasis:
				em := style
				em.Bold, em.Italic = em.Bold || c.Level >= 2, em.Italic || c.Level == 1
				walk(c, em)
			case *extast.Strikethrough:
				struck := style
				struck.Strike = true
				walk(c, struck)
			case *ast.Link:
				link := style
				link.Href = web(string(c.Destination), "mailto")
				walk(c, link)
			case *ast.AutoLink:
				link, href := style, string(c.URL(src))
				if c.AutoLinkType == ast.AutoLinkEmail && !strings.HasPrefix(href, "mailto:") {
					href = "mailto:" + href
				}
				link.Href = web(href, "mailto")
				add(link, string(c.Label(src)))
			case *ast.RawHTML:
				for i := 0; i < c.Segments.Len(); i++ {
					segment := c.Segments.At(i)
					add(style, string(segment.Value(src)))
				}
			case *extast.TaskCheckBox:
				add(style, map[bool]string{true: "☑ ", false: "☐ "}[c.IsChecked])
			default:
				walk(c, style)
			}
		}
	}
	walk(n, Span{})
	return out
}
