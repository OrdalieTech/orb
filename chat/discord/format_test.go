package discord

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestChunkTextBoundaries(t *testing.T) {
	if got := chunkText("", 10); got != nil {
		t.Errorf("empty input chunks = %v, want none", got)
	}
	if got := chunkText("short", 10); len(got) != 1 || got[0] != "short" {
		t.Errorf("short input chunks = %v", got)
	}

	// Paragraph boundary preferred.
	text := strings.Repeat("a", 6) + "\n\n" + strings.Repeat("b", 6)
	got := chunkText(text, 10)
	if len(got) != 2 || got[0] != strings.Repeat("a", 6) || got[1] != strings.Repeat("b", 6) {
		t.Errorf("paragraph split = %q", got)
	}

	// Line boundary next.
	text = "aaaa\nbbbb\ncccc"
	got = chunkText(text, 10)
	if len(got) != 2 || got[0] != "aaaa\nbbbb" || got[1] != "cccc" {
		t.Errorf("line split = %q", got)
	}

	// Word boundary next.
	text = "aaa bbb ccc ddd"
	got = chunkText(text, 10)
	if len(got) != 2 || got[0] != "aaa bbb" || got[1] != "ccc ddd" {
		t.Errorf("word split = %q", got)
	}

	// Hard cut when nothing else fits.
	text = strings.Repeat("x", 25)
	got = chunkText(text, 10)
	if len(got) != 3 || got[0] != strings.Repeat("x", 10) || got[2] != strings.Repeat("x", 5) {
		t.Errorf("hard cut = %q", got)
	}
}

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

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("héllo", 10); got != "héllo" {
		t.Errorf("truncateRunes short = %q", got)
	}
	if got := truncateRunes("héllo", 3); got != "hél" {
		t.Errorf("truncateRunes = %q, want hél", got)
	}
}
