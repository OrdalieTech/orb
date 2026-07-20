package teams

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
		{"basic", 28000},   // heading/table/image/hr downgrades, one chunk
		{"fences", 40},     // fence split closed and reopened with language
		{"paragraphs", 30}, // paragraph-preferred split points
		{"longline", 12},   // space and hard cuts, surrogate pairs intact
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join("testdata", tc.name+".md"))
			if err != nil {
				t.Fatalf("read input: %v", err)
			}
			chunks := chunkText(formatText(string(source)), tc.limit)
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

func TestChunkTextNeverSplitsInsideFence(t *testing.T) {
	text := "```go\n" + strings.Repeat("line of code here\n", 20) + "```"
	for _, chunk := range chunkText(text, 60) {
		opens := strings.Count(chunk, "```")
		if opens != 2 {
			t.Fatalf("chunk has %d fence markers, want balanced 2:\n%s", opens, chunk)
		}
		if !strings.HasPrefix(chunk, "```go\n") {
			t.Fatalf("continuation chunk lost the fence language:\n%s", chunk)
		}
	}
}

func TestUTF16Helpers(t *testing.T) {
	if got := utf16Len("a😀b"); got != 4 {
		t.Fatalf("utf16Len = %d, want 4", got)
	}
	if got := utf16Truncate("ab😀cd", 3); got != "ab" {
		t.Fatalf("utf16Truncate = %q, want %q (no half surrogate)", got, "ab")
	}
	if got := utf16Truncate("abc", 10); got != "abc" {
		t.Fatalf("utf16Truncate = %q, want unchanged", got)
	}
}
