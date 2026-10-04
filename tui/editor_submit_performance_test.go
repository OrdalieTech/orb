package tui

import (
	"strings"
	"testing"
)

// Terminal send-text injects ordinary characters, not a bracketed paste.
// Repeatedly copying the whole line for each rune delays the following Enter.
func TestRuneSlicesDoNotCopyLongEditorInput(t *testing.T) {
	text := strings.Repeat("sample text 界😀 ", 2731)
	for _, test := range []struct {
		name  string
		slice func() string
	}{
		{"prefix", func() string { return runeSlice(text, 0, 32000) }},
		{"suffix", func() string { return runeSliceFrom(text, 32000) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			if allocations := testing.AllocsPerRun(10, func() {
				if test.slice() == "" {
					t.Fatal("empty slice")
				}
			}); allocations != 0 {
				t.Fatalf("slicing a long editor line allocates %.0f copies per character", allocations)
			}
		})
	}
}

func TestRuneSlicesPreserveClampingAndUnicode(t *testing.T) {
	for _, text := range []string{"", "ascii", "é界😀e\u0301", "a\xffb"} {
		runes := []rune(text)
		for from := -2; from <= len(runes)+2; from++ {
			start := max(0, min(from, len(runes)))
			if got, want := runeSliceFrom(text, from), string(runes[start:]); got != want {
				t.Fatalf("suffix(%q, %d) = %q, want %q", text, from, got, want)
			}
			for to := -2; to <= len(runes)+2; to++ {
				end := max(start, min(to, len(runes)))
				if got, want := runeSlice(text, from, to), string(runes[start:end]); got != want {
					t.Fatalf("slice(%q, %d, %d) = %q, want %q", text, from, to, got, want)
				}
			}
		}
	}
}
