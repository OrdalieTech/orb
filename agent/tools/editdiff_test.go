package tools

import (
	"fmt"
	"math/rand/v2"
	"reflect"
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

func TestApplyReplacementsToOriginalKeepsSeamLineBreaks(t *testing.T) {
	// Only files without LF give inserted text CR line endings, so check the seam directly.
	written := ApplyReplacementsToOriginal("a\rb\nc", AppliedEditsResult{replacements: []textReplacement{{matchIndex: 2, matchLen: 1, newText: "\r"}}})
	if got := NormalizeToLF(written); got != "a\n\n\nc" {
		t.Fatalf("normalized written = %q", got)
	}
}

func TestEditWritesWhatItDiffsForRandomLineEndings(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	endings := []string{"\n", "\r\n", "\r", ""}
	for run := range 3000 {
		var original strings.Builder
		for range random.IntN(12) + 1 {
			fmt.Fprintf(&original, "w%d%s%s", random.IntN(100), strings.Repeat("  ", random.IntN(5)/4), endings[random.IntN(4)])
		}
		normalized := NormalizeToLF(original.String())
		start := random.IntN(len(normalized))
		oldText := normalized[start:min(len(normalized), start+1+random.IntN(12))]
		newText := []string{"", "\n", "X\n", "\nY", "\r", "Z"}[random.IntN(6)]
		applied, err := ApplyEditsToNormalizedContent(normalized, []Edit{{OldText: oldText, NewText: newText}}, "f.txt")
		if err != nil {
			continue
		}
		if got := NormalizeToLF(ApplyReplacementsToOriginal(original.String(), applied)); got != applied.NewContent {
			t.Fatalf("run %d: %q edited %q->%q wrote %q, diffed %q", run, original.String(), oldText, newText, got, applied.NewContent)
		}
	}
}

func TestGenerateEditDiffMatchesWholeFileDiff(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))
	for run := range 500 {
		lines := make([]string, random.IntN(400))
		for index := range lines {
			lines[index] = fmt.Sprintf("line %d", index)
		}
		content := strings.Join(lines, "\n") + []string{"", "\n"}[random.IntN(2)]
		var replacements []textReplacement
		var changed strings.Builder
		position := 0
		for index := 0; index < 1+random.IntN(4) && position < len(content); index++ {
			matchIndex := position + random.IntN(min(len(content)-position, 300))
			matchLen := 1 + random.IntN(min(len(content)-matchIndex, 120))
			newText := ""
			for line := range random.IntN(4) {
				newText += fmt.Sprintf("new %d %d\n", index, line)
			}
			replacements = append(replacements, textReplacement{matchIndex: matchIndex, matchLen: matchLen, newText: newText})
			changed.WriteString(content[position:matchIndex] + newText)
			position = matchIndex + matchLen
		}
		changed.WriteString(content[position:])
		if changed.String() == content {
			continue
		}
		windowed, err := GenerateEditDiff("f.txt", AppliedEditsResult{BaseContent: content, NewContent: changed.String(), replacements: replacements})
		if err != nil {
			t.Fatal(err)
		}
		whole := GenerateDiffString(content, changed.String(), 4)
		patch, err := GenerateUnifiedPatch("f.txt", content, changed.String(), 4)
		if err != nil {
			t.Fatal(err)
		}
		if windowed.Diff != whole.Diff || windowed.Patch != patch || !reflect.DeepEqual(windowed.FirstChangedLine, whole.FirstChangedLine) {
			t.Fatalf("run %d: windowed diff differs from the whole-file diff\nwindowed:\n%s\n%s\nwhole:\n%s\n%s", run, windowed.Diff, windowed.Patch, whole.Diff, patch)
		}
	}
}
