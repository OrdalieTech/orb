package tools

import (
	"strings"
	"testing"
)

func TestGenerateEditDiffFormatsMatchUpstream(t *testing.T) {
	patch, err := GenerateUnifiedPatch("file.txt", "Hello, world!", "Hello, testing!", 4)
	if err != nil {
		t.Fatal(err)
	}
	wantPatch := strings.Join([]string{
		"--- file.txt",
		"+++ file.txt",
		"@@ -1,1 +1,1 @@",
		"-Hello, world!",
		"\\ No newline at end of file",
		"+Hello, testing!",
		"\\ No newline at end of file",
		"",
	}, "\n")
	if patch != wantPatch {
		t.Fatalf("patch mismatch:\n got: %q\nwant: %q", patch, wantPatch)
	}
	diff := GenerateDiffString("Hello, world!", "Hello, testing!", 4)
	if diff.Diff != "-1 Hello, world!\n+1 Hello, testing!" || diff.FirstChangedLine == nil || *diff.FirstChangedLine != 1 {
		t.Fatalf("display diff = %#v", diff)
	}
}

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

func TestFuzzyFindTextReportsJavaScriptUTF16Offsets(t *testing.T) {
	match := FuzzyFindText("😀 before\nhello\u00a0world\n", "hello world")
	if !match.Found || !match.UsedFuzzyMatch || match.Index != 10 || match.MatchLength != 11 {
		t.Fatalf("match = %#v", match)
	}
}

func TestNormalizeForFuzzyMatchTrimsCarriageReturnPerLine(t *testing.T) {
	if got := NormalizeForFuzzyMatch("x\r\ny"); got != "x\ny" {
		t.Fatalf("NormalizeForFuzzyMatch() = %q, want %q", got, "x\ny")
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
