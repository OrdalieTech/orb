package telegram

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
		{"basic", 4096},  // inline markup, escaping, lists, quotes
		{"fences", 120},  // fence split with pre tags closed and reopened
		{"utf16", 28},    // emoji + CJK counted as UTF-16 code units
		{"longline", 40}, // space and hard cuts on oversize lines
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", tc.name+".md"))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			chunks := formatHTML(string(source), tc.limit)
			for i, chunk := range chunks {
				if n := utf16Len(chunk); n > tc.limit {
					t.Errorf("chunk %d is %d UTF-16 units, limit %d", i, n, tc.limit)
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

func TestUTF16Len(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"😀", 2},   // surrogate pair
		{"日本語", 3}, // BMP CJK: one unit each
		{"a😀b", 4},
		{"👩‍👩‍👧", 8}, // ZWJ family: 3 pairs + 2 joiners
	}
	for _, tc := range cases {
		if got := utf16Len(tc.in); got != tc.want {
			t.Errorf("utf16Len(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestUTF16TruncateNeverSplitsSurrogates(t *testing.T) {
	s := "ab😀cd"
	if got := utf16Truncate(s, 3); got != "ab" {
		t.Errorf("truncate(3) = %q, want %q (no half surrogate)", got, "ab")
	}
	if got := utf16Truncate(s, 4); got != "ab😀" {
		t.Errorf("truncate(4) = %q, want %q", got, "ab😀")
	}
	if got := utf16Truncate(s, 100); got != s {
		t.Errorf("truncate(100) = %q, want full string", got)
	}
}

func TestChunkingSplitsFencesAcrossChunks(t *testing.T) {
	code := strings.Repeat("line one\n", 30)
	chunks := formatHTML("```go\n"+code+"```", 120)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if !strings.HasPrefix(chunk, `<pre><code class="language-go">`) {
			t.Errorf("chunk %d does not reopen the pre tag: %q", i, chunk)
		}
		if !strings.HasSuffix(chunk, "</code></pre>") {
			t.Errorf("chunk %d does not close the pre tag: %q", i, chunk)
		}
		if n := utf16Len(chunk); n > 120 {
			t.Errorf("chunk %d exceeds the limit: %d", i, n)
		}
	}
}

func TestChunkingCountsEmojiAsTwoUnits(t *testing.T) {
	// 12 emoji = 24 UTF-16 units but only 12 runes: a rune-counting chunker
	// would keep this in one chunk of limit 16.
	chunks := formatHTML(strings.Repeat("😀", 12), 16)
	if len(chunks) < 2 {
		t.Fatalf("expected an emoji split under UTF-16 counting, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if n := utf16Len(chunk); n > 16 {
			t.Errorf("chunk %d is %d units", i, n)
		}
		if strings.ContainsRune(chunk, 0xFFFD) {
			t.Errorf("chunk %d contains a broken surrogate", i)
		}
	}
}

func TestHardCutsNeverSplitHTMLEscapes(t *testing.T) {
	// A spaceless line of ampersands renders as repeated "&amp;" escapes and
	// forces hard cuts; a cut inside an escape surfaces as literal "amp;".
	chunks := formatHTML(strings.Repeat("&", 3000), 64)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, chunk := range chunks {
		if n := utf16Len(chunk); n > 64 {
			t.Errorf("chunk %d is %d units, limit 64", i, n)
		}
		if strings.ReplaceAll(chunk, "&amp;", "") != "" {
			t.Errorf("chunk %d splits an escape: %q", i, chunk)
		}
	}
}
