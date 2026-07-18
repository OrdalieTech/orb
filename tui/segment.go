package tui

import (
	"strings"
	"unicode"

	"github.com/OrdalieTech/pi-go/internal/cjksegment"
	"github.com/rivo/uniseg"
)

// segment mirrors Intl.SegmentData. Index counts runes, the Go analog of
// upstream's UTF-16 offsets; both agree on all BMP text.
type segment struct {
	text     string
	index    int
	wordLike bool
}

func runeLen(value string) int { return len([]rune(value)) }

// runeSlice is the rune-index analog of JS String.prototype.slice.
func runeSlice(value string, from, to int) string {
	runes := []rune(value)
	from = max(0, min(from, len(runes)))
	to = max(from, min(to, len(runes)))
	return string(runes[from:to])
}

func runeSliceFrom(value string, from int) string {
	runes := []rune(value)
	from = max(0, min(from, len(runes)))
	return string(runes[from:])
}

func runeIndexFromUTF16(value string, offset int) int {
	if offset <= 0 {
		return 0
	}
	units, runes := 0, 0
	for _, r := range value {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units+width > offset {
			break
		}
		units += width
		runes++
	}
	return runes
}

func utf16OffsetSplitsRune(value string, offset int) bool {
	if offset <= 0 {
		return false
	}
	units := 0
	for _, r := range value {
		width := 1
		if r > 0xFFFF {
			width = 2
		}
		if units < offset && offset < units+width {
			return true
		}
		units += width
		if units >= offset {
			return false
		}
	}
	return false
}

func isWhitespaceRune(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' }

// isWhitespaceChar mirrors upstream's /\s/ test, which includes the BOM.
func isWhitespaceChar(value string) bool { return strings.ContainsFunc(value, isWhitespaceRune) }

func trimWhitespace(value string) string { return strings.TrimFunc(value, isWhitespaceRune) }

const punctuationChars = `(){}[]<>.,;:'"!?+-=*/\|&%^$#@~` + "`"

func isPunctuationRune(r rune) bool { return strings.ContainsRune(punctuationChars, r) }

// firstPunctuationIndex returns the rune index of the first upstream
// PUNCTUATION_REGEX match, or -1.
func firstPunctuationIndex(value string) int {
	for index, r := range []rune(value) {
		if isPunctuationRune(r) {
			return index
		}
	}
	return -1
}

// lastPunctuationEnd returns the rune index just after the last upstream
// PUNCTUATION_REGEX match, or -1.
func lastPunctuationEnd(value string) int {
	runes := []rune(value)
	for index := len(runes) - 1; index >= 0; index-- {
		if isPunctuationRune(runes[index]) {
			return index + 1
		}
	}
	return -1
}

// graphemeSegments splits text into grapheme clusters with rune indices.
func graphemeSegments(text string) []segment {
	segments := make([]segment, 0, len(text))
	index := 0
	graphemes := uniseg.NewGraphemes(text)
	for graphemes.Next() {
		value := graphemes.Str()
		segments = append(segments, segment{text: value, index: index})
		index += runeLen(value)
	}
	return segments
}

func isCJKRunSegment(value string) bool {
	hasCJK := false
	for _, r := range value {
		if isCJKRuleBase(r) {
			hasCJK = true
			continue
		}
		if isWordBreakExtendFormat(r) {
			if isCJKExtend(r) {
				hasCJK = true
			}
			continue
		}
		if !cjksegment.IsDictionaryRune(r) {
			return false
		}
	}
	return hasCJK
}

func isStandaloneCJKNonWord(r rune) bool {
	return r >= 0x2E80 && r <= 0x2E99 ||
		r >= 0x2E9B && r <= 0x2EF3 ||
		r >= 0x2F00 && r <= 0x2FD5 ||
		r == 0x3005 || r == 0x303B ||
		r >= 0xFF9E && r <= 0xFF9F ||
		r >= 0x16FE2 && r <= 0x16FE3 ||
		r >= 0x16FF0 && r <= 0x16FF1
}

func isCJKExtend(r rune) bool {
	return r == '\uFF9E' || r == '\uFF9F'
}

func isWordBreakExtendFormat(r rune) bool {
	return isCJKExtend(r) || r == '\u200D' || unicode.In(r, unicode.Mn, unicode.Mc, unicode.Me, unicode.Cf)
}

func isCJKRuleBase(r rune) bool {
	return cjksegment.IsDictionaryRune(r) && !isWordBreakExtendFormat(r) || r == '\u309B' || r == '\u309C'
}

func isWordBreakKatakana(r rune) bool {
	return r >= 0x3031 && r <= 0x3035 ||
		r >= 0x309B && r <= 0x309C ||
		r >= 0x30A0 && r <= 0x30FF ||
		r >= 0x31F0 && r <= 0x31FF ||
		r >= 0x32D0 && r <= 0x32FE ||
		r >= 0x3300 && r <= 0x3357 ||
		r >= 0xFF66 && r <= 0xFF9D ||
		r >= 0x1AFF0 && r <= 0x1AFFF ||
		r == 0x1B000 ||
		r >= 0x1B120 && r <= 0x1B122 ||
		r == 0x1B155 ||
		r >= 0x1B164 && r <= 0x1B167
}

func splitCJKRuleRuns(value string) []string {
	runes := []rune(value)
	start := 0
	var runs []string
	for index := 0; index < len(runes); {
		if !isWordBreakExtendFormat(runes[index]) {
			index++
			continue
		}
		extendStart := index
		for index < len(runes) && isWordBreakExtendFormat(runes[index]) {
			index++
		}
		if index == len(runes) {
			break
		}
		if extendStart == start ||
			!isWordBreakKatakana(runes[extendStart-1]) ||
			!isWordBreakKatakana(runes[index]) {
			runs = append(runs, string(runes[start:index]))
			start = index
		}
	}
	return append(runs, string(runes[start:]))
}

func cjkRunIsWordLike(value string) bool {
	baseCount := 0
	var lastBase rune
	for _, r := range value {
		if !isCJKRuleBase(r) {
			continue
		}
		baseCount++
		lastBase = r
	}
	if baseCount == 0 {
		return false
	}
	runes := []rune(value)
	if isWordBreakExtendFormat(runes[len(runes)-1]) {
		return !isStandaloneCJKNonWord(lastBase)
	}
	return baseCount > 1 || !isStandaloneCJKNonWord(lastBase)
}

func splitCJKRun(value string) (words []string) {
	var dictionaryRun strings.Builder
	joinNext := false
	flush := func() {
		if dictionaryRun.Len() == 0 {
			return
		}
		split := cjksegment.Split(dictionaryRun.String())
		dictionaryRun.Reset()
		for joinNext && len(split) > 0 {
			word := split[0]
			words[len(words)-1] += word
			split = split[1:]
			if strings.ContainsFunc(word, func(r rune) bool { return !isCJKExtend(r) }) {
				joinNext = false
			}
		}
		words = append(words, split...)
	}
	for _, r := range value {
		if cjksegment.IsDictionaryRune(r) {
			dictionaryRun.WriteRune(r)
			continue
		}
		flush()
		if len(words) == 0 {
			words = append(words, string(r))
		} else {
			words[len(words)-1] += string(r)
		}
		joinNext = true
	}
	flush()
	return words
}

func segmentIsWordLike(value string) bool {
	return strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	})
}

// wordSegments splits text with UAX #29 rules, then applies ICU's dictionary
// engine to the CJK runs that UAX #29 marks for language-specific handling.
func wordSegments(text string) []segment {
	base := make([]segment, 0, 8)
	index := 0
	state := -1
	rest := text
	for len(rest) > 0 {
		var value string
		value, rest, state = uniseg.FirstWordInString(rest, state)
		length := runeLen(value)
		base = append(base, segment{text: value, index: index, wordLike: segmentIsWordLike(value)})
		index += length
	}

	segments := make([]segment, 0, len(base))
	for position := 0; position < len(base); {
		if !isCJKRunSegment(base[position].text) {
			segments = append(segments, base[position])
			position++
			continue
		}
		start := base[position].index
		var run strings.Builder
		for position < len(base) && isCJKRunSegment(base[position].text) {
			run.WriteString(base[position].text)
			position++
		}
		for _, runText := range splitCJKRuleRuns(run.String()) {
			words := splitCJKRun(runText)
			wordLike := cjkRunIsWordLike(runText)
			for _, word := range words {
				segments = append(segments, segment{text: word, index: start, wordLike: wordLike})
				start += runeLen(word)
			}
		}
	}
	return segments
}
