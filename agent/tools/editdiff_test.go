package tools

import (
	"strings"
	"testing"
)

func TestApplyEditsPreservesUntouchedFuzzyLines(t *testing.T) {
	original := strings.Join([]string{
		"keep before  ",
		"first target  ",
		"first after",
		"keep middle   ",
		"second target  ",
		"second after",
		"keep after  ",
		"",
	}, "\n")
	result, err := ApplyEditsToNormalizedContent(original, []Edit{
		{OldText: "first target\nfirst after", NewText: "FIRST\nFIRST2"},
		{OldText: "second target\nsecond after", NewText: "SECOND\nSECOND2"},
	}, "fuzzy.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"keep before  ", "FIRST", "FIRST2", "keep middle   ", "SECOND", "SECOND2", "keep after  ", "",
	}, "\n")
	if result.BaseContent != original || result.NewContent != want {
		t.Fatalf("applied = %#v, want content %q", result, want)
	}
}

func TestApplyEditsRejectsDuplicateAndOverlap(t *testing.T) {
	_, err := ApplyEditsToNormalizedContent("hello world   \nhello world\n", []Edit{{OldText: "hello world", NewText: "x"}}, "dups.txt")
	if err == nil || !strings.Contains(err.Error(), "Found 2 occurrences") {
		t.Fatalf("duplicate error = %v", err)
	}
	_, err = ApplyEditsToNormalizedContent("one\ntwo\nthree\n", []Edit{
		{OldText: "one\ntwo\n", NewText: "ONE\nTWO\n"},
		{OldText: "two\nthree\n", NewText: "TWO\nTHREE\n"},
	}, "overlap.txt")
	if err == nil || !strings.Contains(err.Error(), "edits[0] and edits[1] overlap") {
		t.Fatalf("overlap error = %v", err)
	}
}
