package tui

import "testing"

// Cases ported from upstream packages/tui/test/word-navigation.test.ts.
// The dictionary-segmented CJK case ("你好世界" splitting into two words) is
// excluded: uniseg has no CJK dictionary; contiguous ideograph runs move as
// one unit (recorded in docs/plan/wp-420-report.md).
func TestFindWordBackward(t *testing.T) {
	cases := []struct {
		text   string
		cursor int
		want   int
	}{
		{"hello world", 11, 6},
		{"hello world", 6, 0},
		{"foo.bar", 7, 4},
		{"foo.bar", 4, 3},
		{"foo.bar", 3, 0},
		{"foo:bar", 7, 4},
		{"foo:bar", 4, 3},
		{"foo:bar", 3, 0},
		{"path/to/file", 12, 8},
		{"path/to/file", 8, 7},
		{"path/to/file", 7, 5},
		{"path/to/file", 5, 4},
		{"path/to/file", 4, 0},
		{"你好世界 test", 9, 5},
		{"  hello  ", 9, 2},
		{"  hello  ", 2, 0},
		{"foo...bar", 9, 6},
		{"foo...bar", 6, 3},
		{"foo...bar", 3, 0},
		{"hello", 0, 0},
	}
	for _, testCase := range cases {
		if got := findWordBackward(testCase.text, testCase.cursor, nil); got != testCase.want {
			t.Errorf("findWordBackward(%q, %d) = %d, want %d", testCase.text, testCase.cursor, got, testCase.want)
		}
	}
}

func TestFindWordForward(t *testing.T) {
	cases := []struct {
		text   string
		cursor int
		want   int
	}{
		{"hello world", 0, 5},
		{"hello world", 5, 11},
		{"foo.bar", 0, 3},
		{"foo.bar", 3, 4},
		{"foo.bar", 4, 7},
		{"foo:bar", 0, 3},
		{"foo:bar", 3, 4},
		{"foo:bar", 4, 7},
		{"path/to/file", 0, 4},
		{"path/to/file", 4, 5},
		{"path/to/file", 5, 7},
		{"path/to/file", 7, 8},
		{"path/to/file", 8, 12},
		{"  hello  ", 0, 7},
		{"  hello  ", 7, 9},
		{"foo...bar", 0, 3},
		{"foo...bar", 3, 6},
		{"foo...bar", 6, 9},
		{"hello", 5, 5},
	}
	for _, testCase := range cases {
		if got := findWordForward(testCase.text, testCase.cursor, nil); got != testCase.want {
			t.Errorf("findWordForward(%q, %d) = %d, want %d", testCase.text, testCase.cursor, got, testCase.want)
		}
	}

	// CJK walk reaches the end of the text.
	text := "你好世界 test"
	firstEnd := findWordForward(text, 0, nil)
	if firstEnd <= 0 || firstEnd > 4 {
		t.Fatalf("first CJK step = %d", firstEnd)
	}
	pos := 0
	for pos < runeLen(text) {
		next := findWordForward(text, pos, nil)
		if next == pos {
			break
		}
		pos = next
	}
	if pos != runeLen(text) {
		t.Fatalf("CJK walk ended at %d, want %d", pos, runeLen(text))
	}
}

func TestWordNavigationAtomicSegments(t *testing.T) {
	marker := "[paste #1 +5 lines]"
	text := "hello " + marker + " world"
	options := &wordNavigationOptions{
		segment:         func(input string) []segment { return segmentWithMarkers(input, wordSegments, map[int]bool{1: true}) },
		isAtomicSegment: func(value string) bool { return value == marker },
	}

	if got := findWordBackward(text, runeLen(text), options); got != 26 {
		t.Fatalf("backward from end = %d, want 26", got)
	}
	if got := findWordBackward(text, 26, options); got != 6 {
		t.Fatalf("backward from 26 = %d, want 6", got)
	}
	if got := findWordForward(text, 6, options); got != 6+len(marker) {
		t.Fatalf("forward from 6 = %d, want %d", got, 6+len(marker))
	}
}
