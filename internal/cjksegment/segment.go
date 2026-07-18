package cjksegment

import (
	"math"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	maxWordSize            = 20
	maxFallbackCost        = 255
	maxKatakanaGroupLength = 20
)

var katakanaCosts = [...]uint32{8192, 984, 408, 240, 204, 252, 300, 372, 480}

var dictionaryScriptRanges = [...][2]rune{
	{0x2E80, 0x2E99}, {0x2E9B, 0x2EF3}, {0x2F00, 0x2FD5}, {0x3005, 0x3005},
	{0x3007, 0x3007}, {0x3021, 0x3029}, {0x3038, 0x303B}, {0x3041, 0x3096},
	{0x309D, 0x309F}, {0x30A1, 0x30FA}, {0x30FC, 0x30FF}, {0x31F0, 0x31FF},
	{0x32D0, 0x32FE}, {0x3300, 0x3357},
	{0x3400, 0x4DBF}, {0x4E00, 0x9FFF}, {0xF900, 0xFA6D}, {0xFA70, 0xFAD9},
	{0xFF66, 0xFF70}, {0xFF71, 0xFF9F}, {0x16FE2, 0x16FE3}, {0x16FF0, 0x16FF6},
	{0x1AFF0, 0x1AFF3}, {0x1AFF5, 0x1AFFB}, {0x1AFFD, 0x1AFFE}, {0x1B000, 0x1B122},
	{0x1B132, 0x1B132}, {0x1B150, 0x1B152}, {0x1B155, 0x1B155},
	{0x1B164, 0x1B167}, {0x1F200, 0x1F200}, {0x20000, 0x2A6DF}, {0x2A700, 0x2B81D},
	{0x2B820, 0x2CEAD}, {0x2CEB0, 0x2EBE0}, {0x2EBF0, 0x2EE5D}, {0x2F800, 0x2FA1D},
	{0x30000, 0x3134A}, {0x31350, 0x33479},
}

// IsDictionaryRune reports membership in ICU 78.2 CjkBreakEngine's set.
func IsDictionaryRune(value rune) bool {
	low, high := 0, len(dictionaryScriptRanges)
	for low < high {
		middle := low + (high-low)/2
		candidate := dictionaryScriptRanges[middle]
		switch {
		case value < candidate[0]:
			high = middle
		case value > candidate[1]:
			low = middle + 1
		default:
			return true
		}
	}
	return false
}

type candidate struct {
	length int
	cost   uint32
}

func (dictionary dictionary) candidates(input []rune, start int) []candidate {
	trie := newCharsTrie(dictionary.trie)
	result := trieNoValue
	matchedCodePoints, matchedUnits := 0, 0
	var candidates []candidate
	for index := start; index < len(input); index++ {
		if matchedCodePoints == 0 {
			result = trie.firstRune(input[index])
		} else {
			result = trie.nextRune(input[index])
		}
		matchedCodePoints++
		matchedUnits++
		if input[index] > 0xffff {
			matchedUnits++
		}
		if result.hasValue() {
			candidates = append(candidates, candidate{length: matchedCodePoints, cost: uint32(trie.value())})
		}
		if result == trieNoMatch || result == trieFinalValue || matchedUnits >= maxWordSize {
			break
		}
	}
	return candidates
}

// Split divides one contiguous ICU CJK dictionary-script run into words.
func Split(text string) []string {
	if text == "" {
		return nil
	}
	input, inputMap := normalizedRunes(text)
	best := make([]uint32, len(input)+1)
	previous := make([]int, len(input)+1)
	for index := 1; index < len(best); index++ {
		best[index] = math.MaxUint32
		previous[index] = -1
	}
	previous[0] = -1

	previousWasKatakana := false
	for index, value := range input {
		if best[index] == math.MaxUint32 {
			continue
		}
		candidates := cjkDictionary.candidates(input, index)
		if len(candidates) == 0 || candidates[0].length != 1 {
			candidates = append(candidates, candidate{length: 1, cost: maxFallbackCost})
		}
		for _, candidate := range candidates {
			end := index + candidate.length
			if end > len(input) {
				continue
			}
			cost := best[index] + candidate.cost
			if cost < best[end] {
				best[end] = cost
				previous[end] = index
			}
		}

		isKatakana := katakana(value)
		if !previousWasKatakana && isKatakana {
			runLength := 1
			for index+runLength < len(input) && runLength < maxKatakanaGroupLength && katakana(input[index+runLength]) {
				runLength++
			}
			if runLength < maxKatakanaGroupLength {
				cost := best[index] + katakanaCost(runLength)
				end := index + runLength
				if cost < best[end] {
					best[end] = cost
					previous[end] = index
				}
			}
		}
		previousWasKatakana = isKatakana
	}

	boundaries := []int{len(input)}
	for position := len(input); position > 0; {
		position = previous[position]
		if position < 0 {
			break
		}
		boundaries = append(boundaries, position)
	}
	for left, right := 0, len(boundaries)-1; left < right; left, right = left+1, right-1 {
		boundaries[left], boundaries[right] = boundaries[right], boundaries[left]
	}

	original := []rune(text)
	words := make([]string, 0, len(boundaries)-1)
	last := -1
	for _, boundary := range boundaries {
		mapped := inputMap[boundary]
		if last >= 0 && mapped > last {
			words = append(words, string(original[last:mapped]))
		}
		if mapped > last {
			last = mapped
		}
	}
	if last < len(original) {
		words = append(words, string(original[max(last, 0):]))
	}
	return words
}

func normalizedRunes(text string) ([]rune, []int) {
	if norm.NFKC.IsNormalString(text) {
		input := []rune(text)
		mapping := make([]int, len(input)+1)
		for index := range mapping {
			mapping[index] = index
		}
		return input, mapping
	}

	var iterator norm.Iter
	iterator.InitString(norm.NFKC, text)
	var normalized []rune
	var mapping []int
	for !iterator.Done() {
		start := iterator.Pos()
		fragment := []rune(string(iterator.Next()))
		originalIndex := utf8.RuneCountInString(text[:start])
		normalized = append(normalized, fragment...)
		for range fragment {
			mapping = append(mapping, originalIndex)
		}
	}
	mapping = append(mapping, utf8.RuneCountInString(text))
	return normalized, mapping
}

func katakana(value rune) bool {
	return value >= 0x30A1 && value <= 0x30FE && value != 0x30FB || value >= 0xFF66 && value <= 0xFF9F
}

func katakanaCost(length int) uint32 {
	if length >= len(katakanaCosts) {
		return katakanaCosts[0]
	}
	return katakanaCosts[length]
}
