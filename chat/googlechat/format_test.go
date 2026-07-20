package googlechat

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGoldens = flag.Bool("update", false, "rewrite format golden files")

// chunkSeparator delimits chunks inside golden files.
const chunkSeparator = "\n----chunk----\n"

func TestFormatGoldens(t *testing.T) {
	cases := []struct {
		name  string
		limit int
	}{
		{"dialect", 4000}, // bold/italic/strike/link/heading conversion
		{"fences", 120},   // fence split closed and reopened across chunks
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", tc.name+".md"))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			chunks := ChunkText(FormatText(string(source)), tc.limit)
			for i, chunk := range chunks {
				if n := len([]rune(chunk)); n > tc.limit {
					t.Errorf("chunk %d is %d chars, limit %d", i, n, tc.limit)
				}
			}
			got := strings.Join(chunks, chunkSeparator)
			goldenPath := filepath.Join("testdata", tc.name+".golden")
			if *updateGoldens {
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden (run with -update to create): %v", err)
			}
			if got != string(want) {
				t.Errorf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
			}
		})
	}
}

func TestFormatInlineConversions(t *testing.T) {
	cases := []struct{ in, want string }{
		{"**bold**", "*bold*"},
		{"__bold__", "*bold*"},
		{"*italic*", "_italic_"},
		{"_italic_", "_italic_"},
		{"~~gone~~", "~gone~"},
		{"[label](https://x.example/a?b=c)", "<https://x.example/a?b=c|label>"},
		{"# Title", "*Title*"},
		{"### Deep title", "*Deep title*"},
		{"**a** and *b* mixed", "*a* and _b_ mixed"},
		{"2 * 3 * 4", "2 * 3 * 4"}, // spaced asterisks are not emphasis
		{"plain text", "plain text"},
		{"- list item", "- list item"},
		{"> quote", "> quote"},
	}
	for _, tc := range cases {
		if got := FormatText(tc.in); got != tc.want {
			t.Errorf("FormatText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFormatKeepsFenceContentVerbatim(t *testing.T) {
	in := "```go\nx := **not bold** [not](a-link)\n```"
	want := "```\nx := **not bold** [not](a-link)\n```"
	if got := FormatText(in); got != want {
		t.Errorf("FormatText fence = %q, want %q", got, want)
	}
}

func TestChunkTextNeverSplitsFences(t *testing.T) {
	code := strings.Repeat("some code line\n", 40)
	chunks := ChunkText("intro\n\n```go\n"+code+"```\n\ntail", 120)
	if len(chunks) < 3 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if n := len([]rune(chunk)); n > 120 {
			t.Errorf("chunk %d is %d chars", i, n)
		}
		if strings.Count(chunk, "```")%2 != 0 {
			t.Errorf("chunk %d has an unbalanced fence:\n%s", i, chunk)
		}
	}
}

func TestChunkTextClosesTrailingFence(t *testing.T) {
	chunks := ChunkText("```\ncode without a closing fence", 4000)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	if strings.Count(chunks[0], "```") != 2 {
		t.Errorf("trailing fence not closed:\n%s", chunks[0])
	}
}

func TestChunkTextHardCutsOversizeLines(t *testing.T) {
	chunks := ChunkText(strings.Repeat("x", 10000), 4000)
	if len(chunks) < 3 {
		t.Fatalf("got %d chunks, want the line hard-cut", len(chunks))
	}
	total := 0
	for i, chunk := range chunks {
		n := len([]rune(chunk))
		if n > 4000 {
			t.Errorf("chunk %d is %d chars", i, n)
		}
		total += n
	}
	if total != 10000 {
		t.Errorf("hard cut lost content: %d of 10000 chars", total)
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if chunks := ChunkText("", 4000); chunks != nil {
		t.Fatalf("empty input yielded %v", chunks)
	}
	if chunks := ChunkText("   \n  ", 4000); len(chunks) != 0 {
		t.Fatalf("whitespace input yielded %v", chunks)
	}
}
