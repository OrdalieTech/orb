package discord

import (
	"unicode/utf8"

	"github.com/OrdalieTech/pi-go/chat/internal/runechunk"
)

const messageLimit = 2000

// Discord renders standard markdown natively, so finalized model output is
// sent verbatim — chunking is the only transformation.

// chunkText splits text into chunks of at most limit runes, preferring
// paragraph breaks, then line breaks, then spaces, then a hard cut. Empty
// input yields no chunks.
//
// ponytail: chunk boundaries ignore code fences — a >2000-rune fence splits
// mid-block; accepted ceiling.
func chunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = messageLimit
	}
	return runechunk.Split(text, limit)
}

func truncateRunes(s string, limit int) string {
	if len(s) <= limit || utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}
