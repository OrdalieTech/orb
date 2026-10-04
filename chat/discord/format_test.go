package discord

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkTextCountsRunesNotBytes(t *testing.T) {
	// 1200 two-byte runes = 2400 bytes but only 1200 codepoints: one chunk.
	text := strings.Repeat("é", 1200)
	got := chunkText(text, messageLimit)
	if len(got) != 1 {
		t.Fatalf("chunks = %d, want 1 (limit counts runes, not bytes)", len(got))
	}
	// 4001 runes split into three; every chunk within the rune limit.
	text = strings.Repeat("é", 4001)
	got = chunkText(text, messageLimit)
	if len(got) != 3 {
		t.Fatalf("chunks = %d, want 3", len(got))
	}
	for i, chunk := range got {
		if n := utf8.RuneCountInString(chunk); n > messageLimit {
			t.Errorf("chunk %d = %d runes, over the %d limit", i, n, messageLimit)
		}
	}
}
