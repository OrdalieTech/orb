package ai_test

import (
	"bytes"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
	"github.com/OrdalieTech/orb/internal/partialjson"
)

// TestStreamingStringifyIsNormalizeFixedPoint gates the trusted fast path in
// SetToolCallArgumentsNormalizedJSON: every value StringifyStreamingJSON can
// emit — including every streaming prefix of the F1 corpus, the input class
// the Anthropic delta parser feeds it — must already be a byte-for-byte fixed
// point of NormalizeJSONStringifyJSON.
func TestStreamingStringifyIsNormalizeFixedPoint(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name  string  `json:"name"`
			Input *string `json:"input"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F1", "partialjson.json", &fixture)

	corpus := []string{
		"",
		`{"a":1,"a":2}`,
		`{"b":1,"a":2,"10":3,"2":4,"0":5}`,
		`{"n":-0,"x":1e21,"y":1.5e-7,"z":9007199254740993}`,
		`{"s":"héllo ☃ 😀 \ud800    <>&"}`,
		`{"nested":{"deep":[{"a":1},null,true,"x"]},"tail":"y"}`,
		`{"text":"line\nbreak\ttab\\slash\"quote"}`,
		`[{"a":1},2,"three"]`,
		`"lone string with \ud83d split`,
		`{"unterminated":"val`,
	}
	for _, test := range fixture.Cases {
		if test.Input != nil {
			corpus = append(corpus, *test.Input)
		}
	}
	for _, input := range corpus {
		for cut := 0; cut <= len(input); cut++ {
			partial := input[:cut]
			encoded, err := partialjson.StringifyStreamingJSON(partial)
			if err != nil {
				// The streaming argument setter falls back to {} on error.
				continue
			}
			normalized, err := ai.NormalizeJSONStringifyJSON(encoded)
			if err != nil {
				t.Fatalf("normalize %q (from partial %q): %v", encoded, partial, err)
			}
			if !bytes.Equal(normalized, encoded) {
				t.Fatalf("partial %q: stringify %q re-normalizes to %q", partial, encoded, normalized)
			}
		}
	}
}
