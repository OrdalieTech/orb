// Package runechunk splits platform text at readable rune boundaries.
package runechunk

import "strings"

// Split cuts text into chunks of at most limit runes, preferring paragraph
// breaks, then line breaks, then spaces, then a hard cut. Empty input or a
// non-positive limit yields no chunks.
func Split(text string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		if len(runes) <= limit {
			if chunk := strings.TrimRight(string(runes), "\n "); chunk != "" {
				chunks = append(chunks, chunk)
			}
			break
		}
		cut := splitIndex(runes[:limit])
		if chunk := strings.TrimRight(string(runes[:cut]), "\n "); chunk != "" {
			chunks = append(chunks, chunk)
		}
		runes = runes[cut:]
		for len(runes) > 0 && (runes[0] == '\n' || runes[0] == ' ') {
			runes = runes[1:]
		}
	}
	return chunks
}

func splitIndex(window []rune) int {
	for i := len(window) - 2; i > 0; i-- {
		if window[i] == '\n' && window[i+1] == '\n' {
			return i + 2
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == '\n' {
			return i + 1
		}
	}
	for i := len(window) - 1; i > 0; i-- {
		if window[i] == ' ' {
			return i + 1
		}
	}
	return len(window)
}
