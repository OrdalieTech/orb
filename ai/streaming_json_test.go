package ai

import (
	"reflect"
	"testing"
)

func TestParseStreamingJSON(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  any
	}{
		{"complete object", `{"command":"ls","timeout":5}`, map[string]any{"command": "ls", "timeout": 5.0}},
		{"partial object with open string", `{"command":"ls -l`, map[string]any{"command": "ls -l"}},
		{"partial array", `[1, 2`, []any{1.0, 2.0}},
		{"empty input", "", map[string]any{}},
		{"whitespace input", "   \n\t", map[string]any{}},
		{"malformed input", "not json", map[string]any{}},
		{"raw newline repaired inside string", "{\"a\":\"x\ny\"}", map[string]any{"a": "x\ny"}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ParseStreamingJSON(testCase.input)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("ParseStreamingJSON(%q) = %#v, want %#v", testCase.input, got, testCase.want)
			}
		})
	}
}
