package tui

// wordNavigationOptions optionally overrides segmentation, e.g. with the
// editor's paste-marker-aware segmenter. Cursor positions are rune indices.
type wordNavigationOptions struct {
	segment         func(text string) []segment
	isAtomicSegment func(value string) bool
}

func (options *wordNavigationOptions) segments(text string) []segment {
	if options != nil && options.segment != nil {
		return options.segment(text)
	}
	return wordSegments(text)
}

func (options *wordNavigationOptions) atomic(value string) bool {
	return options != nil && options.isAtomicSegment != nil && options.isAtomicSegment(value)
}

// findWordBackward returns the cursor position one word backward: skip
// trailing whitespace, then stop at the next word/punctuation boundary.
func findWordBackward(text string, cursor int, options *wordNavigationOptions) int {
	if cursor <= 0 {
		return 0
	}
	segments := options.segments(runeSlice(text, 0, cursor))
	newCursor := cursor

	for len(segments) > 0 {
		last := segments[len(segments)-1]
		if options.atomic(last.text) || !isWhitespaceChar(last.text) {
			break
		}
		newCursor -= runeLen(last.text)
		segments = segments[:len(segments)-1]
	}
	if len(segments) == 0 {
		return newCursor
	}

	last := segments[len(segments)-1]
	switch {
	case options.atomic(last.text):
		newCursor -= runeLen(last.text)
	case last.wordLike:
		// Skip inside one word-like segment, preserving ASCII punctuation
		// boundaries (e.g. "foo.bar" stops after the dot).
		if end := lastPunctuationEnd(last.text); end < 0 {
			newCursor -= runeLen(last.text)
		} else {
			newCursor -= runeLen(last.text) - end
		}
	default:
		for len(segments) > 0 {
			candidate := segments[len(segments)-1]
			if options.atomic(candidate.text) || candidate.wordLike || isWhitespaceChar(candidate.text) {
				break
			}
			newCursor -= runeLen(candidate.text)
			segments = segments[:len(segments)-1]
		}
	}
	return newCursor
}

// findWordForward returns the cursor position one word forward: skip leading
// whitespace, then stop at the next word/punctuation boundary.
func findWordForward(text string, cursor int, options *wordNavigationOptions) int {
	length := runeLen(text)
	if cursor >= length {
		return length
	}
	segments := options.segments(runeSliceFrom(text, cursor))
	newCursor := cursor
	position := 0

	for position < len(segments) {
		value := segments[position].text
		if options.atomic(value) || !isWhitespaceChar(value) {
			break
		}
		newCursor += runeLen(value)
		position++
	}
	if position >= len(segments) {
		return newCursor
	}

	first := segments[position]
	switch {
	case options.atomic(first.text):
		newCursor += runeLen(first.text)
	case first.wordLike:
		if index := firstPunctuationIndex(first.text); index >= 0 {
			newCursor += index
		} else {
			newCursor += runeLen(first.text)
		}
	default:
		for position < len(segments) {
			value := segments[position].text
			if options.atomic(value) || segments[position].wordLike || isWhitespaceChar(value) {
				break
			}
			newCursor += runeLen(value)
			position++
		}
	}
	return newCursor
}

// FindWordBackward exposes upstream's UTF-16-indexed default word movement.
func FindWordBackward(text string, cursor int) int {
	// JS slice preserves a lone high surrogate when the cursor splits a
	// supplementary code point; Intl treats it as the punctuation unit directly
	// before the cursor, so the first backward move lands at its leading edge.
	if utf16OffsetSplitsRune(text, cursor) {
		return cursor - 1
	}
	result := findWordBackward(text, runeIndexFromUTF16(text, cursor), nil)
	return utf16Length(runeSlice(text, 0, result))
}

// FindWordForward exposes upstream's UTF-16-indexed default word movement.
func FindWordForward(text string, cursor int) int {
	result := findWordForward(text, runeIndexFromUTF16(text, cursor), nil)
	return utf16Length(runeSlice(text, 0, result))
}
