package main

import (
	"slices"
	"strings"
	"unicode"
)

// modelAliases expands common provider/family shorthands during fuzzy model search.
var modelAliases = map[string]string{
	"oai":  "openai",
	"anth": "anthropic",
	"gem":  "gemini",
	"ds":   "deepseek",
}

// fuzzyFilterModels filters and ranks model IDs (e.g. "anthropic/claude-sonnet-4-6") against
// a whitespace-separated query. Every token must match (AND, any order), ignoring separators
// (- _ . / : space) and supporting shorthand aliases and ordered subsequence matching.
func fuzzyFilterModels(models []string, query string) []string {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return models
	}
	tokens := strings.Fields(q)
	if len(tokens) == 0 {
		return models
	}
	fullNorm := normalizeModel(q)

	type scored struct {
		id    string
		score int
	}
	var hits []scored
	for _, id := range models {
		if sc, ok := scoreModel(id, q, fullNorm, tokens); ok {
			hits = append(hits, scored{id: id, score: sc})
		}
	}
	slices.SortStableFunc(hits, func(a, b scored) int {
		if a.score != b.score {
			return b.score - a.score
		}
		return len(a.id) - len(b.id)
	})
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.id
	}
	return out
}

func scoreModel(id, q, fullNorm string, tokens []string) (int, bool) {
	lower := strings.ToLower(id)
	_, short, hasSlash := strings.Cut(lower, "/")
	if !hasSlash {
		short = lower
	}
	normID := normalizeModel(lower)
	normShort := normalizeModel(short)

	total := 0
	for _, tok := range tokens {
		sc, ok := scoreModelToken(lower, short, normID, normShort, tok)
		if !ok {
			return 0, false
		}
		total += sc
	}

	// Whole-query bonuses reward exact and prefix matches on the short model name first.
	switch {
	case short == q || (fullNorm != "" && normShort == fullNorm):
		total += 10000
	case lower == q || (fullNorm != "" && normID == fullNorm):
		total += 9500
	case strings.HasPrefix(short, q) || (fullNorm != "" && strings.HasPrefix(normShort, fullNorm)):
		total += 5000
	case strings.HasPrefix(lower, q) || (fullNorm != "" && strings.HasPrefix(normID, fullNorm)):
		total += 4000
	}

	return total, true
}

func scoreModelToken(lower, short, normID, normShort, tok string) (int, bool) {
	best, matched := scoreSingleToken(lower, short, normID, normShort, tok)
	if alias, ok := modelAliases[tok]; ok {
		if sc, ok2 := scoreSingleToken(lower, short, normID, normShort, alias); ok2 && (!matched || sc > best) {
			best, matched = sc, true
		}
	}
	return best, matched
}

func scoreSingleToken(lower, short, normID, normShort, tok string) (int, bool) {
	nt := normalizeModel(tok)
	if nt == "" {
		return 0, true
	}

	// 1. Exact short-name match.
	if short == tok || normShort == nt {
		return 3000, true
	}
	// 2. Prefix match on short name.
	if strings.HasPrefix(short, tok) || strings.HasPrefix(normShort, nt) {
		return 2200, true
	}
	// 3. Word-boundary prefix match in short name or full ID.
	if idx := boundaryIndex(short, tok); idx >= 0 {
		return 1700 - idx, true
	}
	if idx := boundaryIndex(lower, tok); idx >= 0 {
		return 1400 - idx, true
	}
	// 4. Direct substring match.
	if idx := strings.Index(short, tok); idx >= 0 {
		return 1100 - idx, true
	}
	if idx := strings.Index(lower, tok); idx >= 0 {
		return 900 - idx, true
	}
	// 5. Separator-normalized substring match (e.g. "54" -> "5.4", "opus55" -> "opus-5-5").
	if idx := strings.Index(normShort, nt); idx >= 0 {
		return 750 - idx, true
	}
	if idx := strings.Index(normID, nt); idx >= 0 {
		return 600 - idx, true
	}
	// 6. Ordered subsequence match with word-boundary and consecutive-run bonuses.
	if sc, ok := subsequenceScore(short, nt); ok {
		return 300 + sc, true
	}
	if sc, ok := subsequenceScore(lower, nt); ok {
		return 150 + sc, true
	}
	if sc, ok := subsequenceScore(normShort, nt); ok {
		return 120 + sc, true
	}
	if sc, ok := subsequenceScore(normID, nt); ok {
		return 60 + sc, true
	}
	return 0, false
}

// normalizeModel strips separators (- _ . / : whitespace) from a lowercased string.
func normalizeModel(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isModelSep(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isModelSep(r rune) bool {
	switch r {
	case '-', '_', '.', '/', ':':
		return true
	}
	return unicode.IsSpace(r)
}

func isWordBoundary(s string, i int) bool {
	if i == 0 {
		return true
	}
	prev := rune(s[i-1])
	curr := rune(s[i])
	if isModelSep(prev) {
		return true
	}
	// Letter <-> digit transition is also a natural boundary in model IDs (e.g. "gpt5" / "4o").
	return (unicode.IsDigit(prev) != unicode.IsDigit(curr)) && (unicode.IsLetter(prev) || unicode.IsDigit(prev))
}

func boundaryIndex(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	for off := 0; off < len(haystack); {
		idx := strings.Index(haystack[off:], needle)
		if idx < 0 {
			return -1
		}
		pos := off + idx
		if isWordBoundary(haystack, pos) {
			return pos
		}
		off = pos + 1
	}
	return -1
}

// subsequenceScore matches needle as an ordered subsequence of haystack, preferring word-boundary
// hits and consecutive runs.
func subsequenceScore(haystack, needle string) (int, bool) {
	if needle == "" {
		return 0, true
	}
	if len(needle) > len(haystack) {
		return 0, false
	}
	score := 0
	first, prev := -1, -2
	hi := 0
	for ni := 0; ni < len(needle); ni++ {
		ch := needle[ni]
		// Prefer a word-boundary match if one exists and still allows the rest of needle to match.
		found := -1
		for j := hi; j < len(haystack); j++ {
			if haystack[j] != ch {
				continue
			}
			if found == -1 {
				found = j
			}
			if isWordBoundary(haystack, j) && canMatchSubseq(haystack[j+1:], needle[ni+1:]) {
				found = j
				break
			}
		}
		if found < 0 {
			return 0, false
		}
		if first < 0 {
			first = found
		}
		if found == prev+1 {
			score += 40
		}
		if isWordBoundary(haystack, found) {
			score += 60
		}
		prev = found
		hi = found + 1
	}
	span := prev - first + 1
	score -= (span - len(needle)) * 2
	return score, true
}

func canMatchSubseq(haystack, needle string) bool {
	hi := 0
	for ni := 0; ni < len(needle); ni++ {
		idx := strings.IndexByte(haystack[hi:], needle[ni])
		if idx < 0 {
			return false
		}
		hi += idx + 1
	}
	return true
}
