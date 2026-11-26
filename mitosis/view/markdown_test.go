package view

import (
	"testing"
)

func TestSpansMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   []Span
		want string
	}{
		{
			name: "empty",
			in:   nil,
			want: "",
		},
		{
			name: "plain",
			in:   []Span{{Text: "hello world"}},
			want: "hello world",
		},
		{
			name: "bold italic bold+italic strike",
			in: []Span{
				{Text: "a "},
				{Text: "bold", Bold: true},
				{Text: " "},
				{Text: "italic", Italic: true},
				{Text: " "},
				{Text: "both", Bold: true, Italic: true},
				{Text: " "},
				{Text: "gone", Strike: true},
			},
			want: "a **bold** *italic* ***both*** ~~gone~~",
		},
		{
			name: "whitespace kept outside emphasis",
			in: []Span{
				{Text: "  padded  ", Bold: true},
				{Text: "\nline\t", Italic: true},
				{Text: "   ", Bold: true},
			},
			want: "  **padded**  \n*line*\t   ",
		},
		{
			name: "inline code with backticks",
			in: []Span{
				{Text: "simple", Code: true},
				{Text: " "},
				{Text: "a`b", Code: true},
				{Text: " "},
				{Text: "`edge`", Code: true},
			},
			want: "`simple` ``a`b`` `` `edge` ``",
		},
		{
			name: "links with styles and code",
			in: []Span{
				{Text: "docs", Href: "https://example.com"},
				{Text: " "},
				{Text: "bold link", Bold: true, Href: "https://example.com/b"},
				{Text: " "},
				{Text: "fn()", Code: true, Href: "https://example.com/fn"},
			},
			want: "[docs](https://example.com) [**bold link**](https://example.com/b) [`fn()`](https://example.com/fn)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SpansMarkdown(tc.in); got != tc.want {
				t.Fatalf("SpansMarkdown() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestTableMarkdown(t *testing.T) {
	if got := TableMarkdown(nil); got != "" {
		t.Fatalf("empty table = %q, want empty", got)
	}
	if got := TableMarkdown([][][]Span{{}}); got != "" {
		t.Fatalf("zero-col table = %q, want empty", got)
	}

	rows := [][][]Span{
		{{{Text: "Col A"}}, {{Text: "Col B"}}},
		{{{Text: "a | b"}}, {{Text: "line1\nline2"}}},
		{{{Text: "only one"}}},
	}
	want := "| Col A | Col B |\n| --- | --- |\n| a \\| b | line1<br>line2 |\n| only one |  |"
	if got := TableMarkdown(rows); got != want {
		t.Fatalf("TableMarkdown() =\n%s\nwant:\n%s", got, want)
	}
}

func TestBlockMarkdown(t *testing.T) {
	tests := []struct {
		name string
		in   Block
		want string
	}{
		{
			name: "paragraph",
			in:   Block{Type: "p", Spans: []Span{{Text: "hello"}}},
			want: "hello",
		},
		{
			name: "heading clamped",
			in:   Block{Type: "h", Level: 9, Spans: []Span{{Text: "deep"}}},
			want: "###### deep",
		},
		{
			name: "heading level zero clamped to 1",
			in:   Block{Type: "h", Level: 0, Spans: []Span{{Text: "top"}}},
			want: "# top",
		},
		{
			name: "bullet list dot normalized",
			in:   Block{Type: "li", Mark: "·", Depth: 1, Spans: []Span{{Text: "nested"}}},
			want: "  - nested",
		},
		{
			name: "ordered list",
			in:   Block{Type: "li", Mark: "2.", Depth: 0, Spans: []Span{{Text: "second"}}},
			want: "2. second",
		},
		{
			name: "quote recursive",
			in: Block{
				Type: "quote",
				Blocks: []Block{
					{Type: "p", Spans: []Span{{Text: "first"}}},
					{Type: "p", Spans: []Span{{Text: "second"}}},
				},
			},
			want: "> first\n>\n> second",
		},
		{
			name: "code with nested backticks",
			in:   Block{Type: "code", Lang: "md", Text: "```go\nfmt.Println()\n```"},
			want: "````md\n```go\nfmt.Println()\n```\n````",
		},
		{
			name: "art defaults to text lang",
			in:   Block{Type: "art", Text: "+---+\n| A |\n+---+"},
			want: "```text\n+---+\n| A |\n+---+\n```",
		},
		{
			name: "rule",
			in:   Block{Type: "rule"},
			want: "---",
		},
		{
			name: "unknown",
			in:   Block{Type: "unknown"},
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.Markdown(); got != tc.want {
				t.Fatalf("Block.Markdown() =\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

func TestBlocksMarkdownRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			name: "headings and paragraphs with inline styles",
			src:  "# Title\n\nA **bold**, *italic*, ***both***, `code`, ~~strike~~, and [link](https://example.com).",
			want: "# Title\n\nA **bold**, *italic*, ***both***, `code`, ~~strike~~, and [link](https://example.com).",
		},
		{
			name: "nested lists",
			src:  "- one\n  - nested a\n  - nested b\n- two\n\n1. first\n2. second",
			want: "- one\n  - nested a\n  - nested b\n- two\n1. first\n2. second",
		},
		{
			name: "blockquotes",
			src:  "> quoted line\n>\n> > nested quote",
			want: "> quoted line\n>\n> > nested quote",
		},
		{
			name: "fenced code with nested backticks",
			src:  "````markdown\n```go\nfmt.Println(\"hi\")\n```\n````",
			want: "````markdown\n```go\nfmt.Println(\"hi\")\n```\n````",
		},
		{
			name: "gfm table with escaped pipes",
			src:  "| Name | Expr |\n| --- | --- |\n| or | `a \\| b` |\n| bold | **x** |",
			want: "| Name | Expr |\n| --- | --- |\n| or | `a \\| b` |\n| bold | **x** |",
		},
		{
			name: "rule between paragraphs",
			src:  "above\n\n---\n\nbelow",
			want: "above\n\n---\n\nbelow",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blocks := Blocks(tc.src, true)
			got := BlocksMarkdown(blocks)
			if got != tc.want {
				t.Fatalf("BlocksMarkdown(Blocks(src, true)) =\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}
