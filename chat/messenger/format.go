package messenger

import "github.com/OrdalieTech/pi-go/chat/internal/runechunk"

const maxMessageLen = 2000

const chunkLimit = 1900

// chunkText splits text into chunks of at most limit characters (runes —
// the Send API counts characters, not bytes), preferring paragraph breaks,
// then line breaks, then spaces, then a hard cut. Empty input yields no
// chunks.
//
// ponytail: chunk boundaries ignore code fences — a >limit fence splits
// mid-block; accepted ceiling (same as the WhatsApp adapter).
func chunkText(text string, limit int) []string {
	if limit <= 0 {
		limit = chunkLimit
	}
	return runechunk.Split(text, limit)
}
