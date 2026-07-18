package tui

import (
	"sort"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// FuzzyMatch reports whether all query characters appear in order in text.
// Lower score = better match.
type FuzzyMatch struct {
	Matches bool
	Score   float64
}

func fuzzyMatchQuery(normalizedQuery, textLower []uint16) FuzzyMatch {
	if len(normalizedQuery) == 0 {
		return FuzzyMatch{Matches: true}
	}
	if len(normalizedQuery) > len(textLower) {
		return FuzzyMatch{}
	}
	queryIndex := 0
	score := 0.0
	lastMatchIndex := -1
	consecutiveMatches := 0
	for i := 0; i < len(textLower) && queryIndex < len(normalizedQuery); i++ {
		if textLower[i] != normalizedQuery[queryIndex] {
			continue
		}
		previous := rune(0)
		if i > 0 {
			previous = rune(textLower[i-1])
		}
		isWordBoundary := i == 0 || strings.ContainsRune("-_./:", previous) || isWhitespaceChar(string(previous))
		if lastMatchIndex == i-1 {
			consecutiveMatches++
			score -= float64(consecutiveMatches * 5)
		} else {
			consecutiveMatches = 0
			if lastMatchIndex >= 0 {
				score += float64((i - lastMatchIndex - 1) * 2)
			}
		}
		if isWordBoundary {
			score -= 10
		}
		score += float64(i) * 0.1
		lastMatchIndex = i
		queryIndex++
	}
	if queryIndex < len(normalizedQuery) {
		return FuzzyMatch{}
	}
	if slicesEqualUTF16(normalizedQuery, textLower) {
		score -= 100
	}
	return FuzzyMatch{Matches: true, Score: score}
}

// splitAlphaNumeric splits an all-lowercase query of the form letters+digits
// or digits+letters, returning the swapped form, or "".
func slicesEqualUTF16(left, right []uint16) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func swappedAlphaNumericQuery(query []uint16) []uint16 {
	isDigit := func(r uint16) bool { return r >= '0' && r <= '9' }
	isAlpha := func(r uint16) bool { return r >= 'a' && r <= 'z' }
	boundary := 0
	for boundary < len(query) && isAlpha(query[boundary]) {
		boundary++
	}
	if boundary > 0 && boundary < len(query) {
		rest := query[boundary:]
		allDigits := true
		for _, r := range rest {
			if !isDigit(r) {
				allDigits = false
				break
			}
		}
		if allDigits {
			return append(append([]uint16(nil), rest...), query[:boundary]...)
		}
	}
	boundary = 0
	for boundary < len(query) && isDigit(query[boundary]) {
		boundary++
	}
	if boundary > 0 && boundary < len(query) {
		rest := query[boundary:]
		allAlpha := true
		for _, r := range rest {
			if !isAlpha(r) {
				allAlpha = false
				break
			}
		}
		if allAlpha {
			return append(append([]uint16(nil), rest...), query[:boundary]...)
		}
	}
	return nil
}

// FuzzyMatchScore mirrors upstream fuzzyMatch, including the swapped
// alpha-numeric fallback (e.g. "o1" also tries "1o").
func FuzzyMatchScore(query, text string) FuzzyMatch {
	lower := cases.Lower(language.Und)
	queryLower := utf16.Encode([]rune(lower.String(query)))
	textLower := utf16.Encode([]rune(lower.String(text)))
	primary := fuzzyMatchQuery(queryLower, textLower)
	if primary.Matches {
		return primary
	}
	swapped := swappedAlphaNumericQuery(queryLower)
	if swapped == nil {
		return primary
	}
	swappedMatch := fuzzyMatchQuery(swapped, textLower)
	if !swappedMatch.Matches {
		return primary
	}
	return FuzzyMatch{Matches: true, Score: swappedMatch.Score + 5}
}

// FuzzyFilter filters and sorts items by fuzzy match quality (best first).
// Whitespace- and slash-separated query tokens must all match.
func FuzzyFilter[T any](items []T, query string, getText func(T) string) []T {
	if trimWhitespace(query) == "" {
		return items
	}
	tokens := strings.FieldsFunc(trimWhitespace(query), func(r rune) bool {
		return r == '/' || isWhitespaceChar(string(r))
	})
	if len(tokens) == 0 {
		return items
	}
	type scored struct {
		item  T
		total float64
	}
	results := make([]scored, 0, len(items))
	for _, item := range items {
		text := getText(item)
		total := 0.0
		allMatch := true
		for _, token := range tokens {
			match := FuzzyMatchScore(token, text)
			if !match.Matches {
				allMatch = false
				break
			}
			total += match.Score
		}
		if allMatch {
			results = append(results, scored{item: item, total: total})
		}
	}
	sort.SliceStable(results, func(a, b int) bool { return results[a].total < results[b].total })
	filtered := make([]T, len(results))
	for index, result := range results {
		filtered[index] = result.item
	}
	return filtered
}
