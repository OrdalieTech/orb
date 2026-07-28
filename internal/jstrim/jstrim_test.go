package jstrim

import "testing"

func TestIsSpaceMatchesECMAScriptTrimSet(t *testing.T) {
	spaces := []rune{'\t', '\n', '\v', '\f', '\r', ' ', '\u00a0', '\u1680', '\u2028', '\u2029', '\u202f', '\u205f', '\u3000', '\ufeff'}
	for character := rune(0x2000); character <= 0x200a; character++ {
		spaces = append(spaces, character)
	}
	for _, space := range spaces {
		if !IsSpace(space) {
			t.Errorf("IsSpace(%U) = false, want true", space)
		}
	}
	// NEL (U+0085) is whitespace to Go's unicode.IsSpace but JavaScript's
	// trim does not strip it; zero-width characters are never stripped.
	for _, nonSpace := range []rune{'\u0085', '\u200b', '\u200c', '\u200d', 'a', '0'} {
		if IsSpace(nonSpace) {
			t.Errorf("IsSpace(%U) = true, want false", nonSpace)
		}
	}
}
