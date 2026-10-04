package googlechat

import (
	"strings"
	"testing"
)

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
