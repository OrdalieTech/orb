package tui

import "testing"

// Cases ported from upstream packages/tui/test/fuzzy.test.ts.
func TestFuzzyMatchScore(t *testing.T) {
	if match := FuzzyMatchScore("", "anything"); !match.Matches || match.Score != 0 {
		t.Fatalf("empty query = %+v", match)
	}
	if match := FuzzyMatchScore("toolong", "abc"); match.Matches {
		t.Fatalf("query longer than text matched")
	}
	if match := FuzzyMatchScore("abc", "abc"); !match.Matches || match.Score >= 0 {
		t.Fatalf("exact match = %+v", match)
	}
	if match := FuzzyMatchScore("cba", "abc"); match.Matches {
		t.Fatalf("out-of-order query matched")
	}
	if match := FuzzyMatchScore("ABC", "abc"); !match.Matches {
		t.Fatalf("case-insensitive query did not match")
	}
	if match := FuzzyMatchScore("abc", "ABC"); !match.Matches {
		t.Fatalf("case-insensitive text did not match")
	}
	consecutive := FuzzyMatchScore("abc", "abcdef")
	scattered := FuzzyMatchScore("abc", "axbxcx")
	if !consecutive.Matches || !scattered.Matches || consecutive.Score >= scattered.Score {
		t.Fatalf("consecutive %v should score better than scattered %v", consecutive.Score, scattered.Score)
	}
	boundary := FuzzyMatchScore("fb", "foo-bar")
	middle := FuzzyMatchScore("fb", "xxfxxbxx")
	if !boundary.Matches || !middle.Matches || boundary.Score >= middle.Score {
		t.Fatalf("word boundary %v should score better than middle %v", boundary.Score, middle.Score)
	}
	if match := FuzzyMatchScore("o1", "1o"); !match.Matches {
		t.Fatalf("swapped alpha-numeric query did not match")
	}
	if match := FuzzyMatchScore("4o", "o4-mini"); !match.Matches {
		t.Fatalf("swapped numeric-alpha query did not match")
	}
}

func TestFuzzyMatchUTF16ScoringAndLowercase(t *testing.T) {
	if got := FuzzyMatchScore("a", "😀a"); !got.Matches || got.Score != 0.2 {
		t.Fatalf("astral-prefix score = %+v, want {true 0.2}", got)
	}
	if got := FuzzyMatchScore("😀", "x😀"); !got.Matches || got.Score != -4.7 {
		t.Fatalf("astral query score = %+v, want {true -4.7}", got)
	}
	if got := FuzzyMatchScore("i̇", "İ"); !got.Matches || got.Score != -124.9 {
		t.Fatalf("expanding lowercase score = %+v, want {true -124.9}", got)
	}
}

func TestFuzzyFilter(t *testing.T) {
	identity := func(value string) string { return value }

	items := []string{"alpha", "beta", "gamma"}
	if got := FuzzyFilter(items, "", identity); len(got) != 3 {
		t.Fatalf("empty query filtered items: %v", got)
	}
	if got := FuzzyFilter(items, "  ", identity); len(got) != 3 {
		t.Fatalf("blank query filtered items: %v", got)
	}

	got := FuzzyFilter([]string{"apple", "banana", "cherry"}, "an", identity)
	if len(got) != 1 || got[0] != "banana" {
		t.Fatalf("filter = %v", got)
	}

	got = FuzzyFilter([]string{"xtestx", "test", "tempest"}, "test", identity)
	if len(got) == 0 || got[0] != "test" {
		t.Fatalf("sort by quality = %v", got)
	}

	// Slash-separated tokens must all match.
	got = FuzzyFilter([]string{"openai/gpt-4", "anthropic/claude"}, "open/gpt", identity)
	if len(got) != 1 || got[0] != "openai/gpt-4" {
		t.Fatalf("token filter = %v", got)
	}
}
