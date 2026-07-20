package slack

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

var updateGoldens = flag.Bool("update", false, "rewrite format golden files")

// chunkSeparator delimits chunks inside golden files.
const chunkSeparator = "\n----chunk----\n"

func TestFormatGoldens(t *testing.T) {
	cases := []struct {
		name  string
		limit int
	}{
		{"basic", 4000},  // inline markup, escaping, headings, lists, quotes
		{"fences", 80},   // fence split closed and reopened across chunks
		{"longline", 48}, // space and hard cuts on oversize lines
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", tc.name+".md"))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			chunks := ChunkText(FormatText(string(source)), tc.limit)
			for i, chunk := range chunks {
				if n := utf8.RuneCountInString(chunk); n > tc.limit {
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

func TestFormatTextRules(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bold stars", "a **b** c", "a *b* c"},
		{"bold underscores", "a __b__ c", "a *b* c"},
		{"italic star", "a *b* c", "a _b_ c"},
		{"strike", "a ~~b~~ c", "a ~b~ c"},
		{"link", "see [docs](https://x.test/p?a=1&b=2)", "see <https://x.test/p?a=1&amp;b=2|docs>"},
		{"heading", "## Title", "*Title*"},
		{"escape", "a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{"bullet dash", "- item", "• item"},
		{"bullet star", "* item", "• item"},
		{"blockquote survives escaping", "> a > b", "> a &gt; b"},
		{"bold not italicized", "**x** and *y*", "*x* and _y_"},
		{"fence language dropped", "```go\na()\n```", "```\na()\n```"},
		{"fence content escaped only", "```\na < b && **x**\n```", "```\na &lt; b &amp;&amp; **x**\n```"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatText(tc.in); got != tc.want {
				t.Fatalf("FormatText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestChunkTextFenceReopening(t *testing.T) {
	text := "```\n" + strings.Repeat("line one\n", 6) + "```"
	chunks := ChunkText(text, 40)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a split", len(chunks))
	}
	for i, chunk := range chunks {
		if n := utf8.RuneCountInString(chunk); n > 40 {
			t.Errorf("chunk %d is %d chars", i, n)
		}
		if !strings.HasPrefix(chunk, "```") {
			t.Errorf("chunk %d does not reopen the fence: %q", i, chunk)
		}
		if !strings.HasSuffix(chunk, "```") {
			t.Errorf("chunk %d does not close the fence: %q", i, chunk)
		}
		if got := strings.Count(chunk, "```") % 2; got != 0 {
			t.Errorf("chunk %d has unbalanced fences: %q", i, chunk)
		}
	}
}

func TestChunkTextPrefersLineBoundaries(t *testing.T) {
	text := "first paragraph line\n\nsecond paragraph line"
	chunks := ChunkText(text, 25)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %q, want 2", chunks)
	}
	if chunks[0] != "first paragraph line" || chunks[1] != "second paragraph line" {
		t.Fatalf("chunks = %q", chunks)
	}
}

func TestChunkTextEmpty(t *testing.T) {
	if got := ChunkText("", 100); len(got) != 0 {
		t.Fatalf("ChunkText(\"\") = %q", got)
	}
	if got := ChunkText("  \n ", 100); len(got) != 0 {
		t.Fatalf("ChunkText(blank) = %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 3); got != "hél" {
		t.Fatalf("truncateRunes = %q", got)
	}
	if got := truncateRunes("ok", 10); got != "ok" {
		t.Fatalf("truncateRunes = %q", got)
	}
}
