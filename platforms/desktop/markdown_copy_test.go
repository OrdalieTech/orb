package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestSpansSliceToMarkdown(t *testing.T) {
	ss := []Span{
		{Text: "Use "},
		{Text: "go test", Code: true},
		{Text: " with "},
		{Text: "race detection", Bold: true},
		{Text: " (see "},
		{Text: "docs", Href: "https://example.com"},
		{Text: ")."},
	}
	got := spansToMarkdown(ss)
	want := "Use `go test` with **race detection** (see [docs](https://example.com))."
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	// Partial slice across bold + code boundaries ("go test with race").
	partial := spansSliceToMarkdown(ss, 4, 21)
	if partial != "`go test` with **race**" {
		t.Fatalf("partial = %q", partial)
	}
}

func TestTableToMarkdown(t *testing.T) {
	rows := [][][]Span{
		{{{Text: "Name"}}, {{Text: "Flags"}}},
		{{{Text: "read", Code: true}}, {{Text: "a | b"}}},
	}
	got := tableToMarkdown(rows)
	want := "| Name | Flags |\n| --- | --- |\n| `read` | a \\| b |"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCopyRecorderMultiBlockAndPartialTable(t *testing.T) {
	rec := newCopyRecorder()
	rec.parts[1] = mdPart{kind: "h", level: 2, spans: []Span{{Text: "Summary"}}}
	rec.parts[2] = mdPart{kind: "li", mark: "·", depth: 0, spans: []Span{{Text: "First "}, {Text: "item", Bold: true}}}
	rec.parts[3] = mdPart{kind: "li", mark: "·", depth: 1, spans: []Span{{Text: "Nested"}}}
	rec.parts[4] = mdPart{kind: "cell", tableID: 1, row: 0, col: 0, spans: []Span{{Text: "Col A"}}}
	rec.parts[5] = mdPart{kind: "cell", tableID: 1, row: 0, col: 1, spans: []Span{{Text: "Col B"}}}
	rec.parts[6] = mdPart{kind: "cell", tableID: 1, row: 1, col: 0, spans: []Span{{Text: "val1", Code: true}}}
	rec.parts[7] = mdPart{kind: "cell", tableID: 1, row: 1, col: 1, spans: []Span{{Text: "val2"}}}
	rec.parts[8] = mdPart{kind: "code", lang: "go", text: "fmt.Println(1)"}

	got := rec.format([]ui.SelectedText{
		{ID: 1, Text: "Summary", Start: 0, End: 7},
		{ID: 2, Text: "First item", Start: 0, End: 10},
		{ID: 3, Text: "Nested", Start: 0, End: 6},
		{ID: 4, Text: "Col A", Start: 0, End: 5},
		{ID: 5, Text: "Col B", Start: 0, End: 5},
		{ID: 6, Text: "val1", Start: 0, End: 4},
		{ID: 7, Text: "val2", Start: 0, End: 4},
		{ID: 8, Text: "fmt.Println(1)", Start: 0, End: 14},
	})
	want := "## Summary\n\n- First **item**\n  - Nested\n\n| Col A | Col B |\n| --- | --- |\n| `val1` | val2 |\n\n```go\nfmt.Println(1)\n```"
	if got != want {
		t.Fatalf("got:\n%s\n\nwant:\n%s", got, want)
	}

	// Partial selection inside a single code block copies plain code without fences.
	sub := rec.format([]ui.SelectedText{{ID: 8, Text: "fmt.Println(1)", Start: 4, End: 11}})
	if sub != "Println" {
		t.Fatalf("partial code = %q, want %q", sub, "Println")
	}

	// Single cell selection copies just the cell text, not a 1x1 pipe table.
	cell := rec.format([]ui.SelectedText{{ID: 6, Text: "val1", Start: 0, End: 4}})
	if cell != "`val1`" {
		t.Fatalf("single cell = %q, want %q", cell, "`val1`")
	}
}
