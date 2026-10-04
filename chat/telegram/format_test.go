package telegram

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/chat/internal/runechunk"
)

func TestChunkingCountsEmojiAsTwoUnits(t *testing.T) {
	// 12 emoji = 24 UTF-16 units but only 12 runes: a rune-counting chunker
	// would keep this in one chunk of limit 16.
	chunks := formatHTML(strings.Repeat("😀", 12), 16)
	if len(chunks) < 2 {
		t.Fatalf("expected an emoji split under UTF-16 counting, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if n := runechunk.LenUTF16(chunk); n > 16 {
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
		if n := runechunk.LenUTF16(chunk); n > 64 {
			t.Errorf("chunk %d is %d units, limit 64", i, n)
		}
		if strings.ReplaceAll(chunk, "&amp;", "") != "" {
			t.Errorf("chunk %d splits an escape: %q", i, chunk)
		}
	}
}
