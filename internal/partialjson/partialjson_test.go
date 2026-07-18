package partialjson

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestParsePartialJSONUpstreamCorpus(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		assertParsed(t, `"`, AllowString, "")
		assertParsed(t, `" \x12`, AllowString, " ")
		assertErrorType[*PartialJSONError](t, `"`, ^AllowString)
	})

	t.Run("array", func(t *testing.T) {
		assertParsed(t, `["`, AllowArray, []any{})
		assertParsed(t, `["`, AllowArray|AllowString, []any{""})

		for _, input := range []string{`[`, `["`, `[""`, `["",`} {
			assertErrorType[*PartialJSONError](t, input, AllowString)
		}
	})

	t.Run("object", func(t *testing.T) {
		assertParsed(t, `{"": "`, AllowObject, map[string]any{})
		assertParsed(t, `{"": "`, AllowObject|AllowString, map[string]any{"": ""})

		for _, input := range []string{`{`, `{"`, `{""`, `{"":`, `{"":"`, `{"":""`} {
			assertErrorType[*PartialJSONError](t, input, AllowString)
		}
	})

	t.Run("singletons", func(t *testing.T) {
		assertParsed(t, "n", AllowNull, nil)
		assertErrorType[*MalformedJSONError](t, "n", ^AllowNull)
		assertParsed(t, "t", AllowBool, true)
		assertErrorType[*MalformedJSONError](t, "t", ^AllowBool)
		assertParsed(t, "f", AllowBool, false)
		assertErrorType[*MalformedJSONError](t, "f", ^AllowBool)

		positive, err := Parse("I", AllowInfinity)
		if err != nil || !math.IsInf(positive.(float64), 1) {
			t.Fatalf("Parse(I) = (%v, %v), want +Inf", positive, err)
		}
		assertErrorType[*MalformedJSONError](t, "I", ^AllowInfinity)

		negative, err := Parse("-I", AllowNegativeInfinity)
		if err != nil || !math.IsInf(negative.(float64), -1) {
			t.Fatalf("Parse(-I) = (%v, %v), want -Inf", negative, err)
		}
		assertErrorType[*MalformedJSONError](t, "-I", ^AllowNegativeInfinity)

		nan, err := Parse("N", AllowNaN)
		if err != nil || !math.IsNaN(nan.(float64)) {
			t.Fatalf("Parse(N) = (%v, %v), want NaN", nan, err)
		}
		assertErrorType[*MalformedJSONError](t, "N", ^AllowNaN)
	})

	t.Run("number", func(t *testing.T) {
		assertParsed(t, "0", ^AllowNumber, float64(0))
		assertParsed(t, "-1.25e+4", ^AllowNumber, float64(-12500))
		assertParsed(t, "-1.25e+", AllowNumber, -1.25)
		assertParsed(t, "-1.25e", AllowNumber, -1.25)
	})
}

func TestRepairJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "unchanged valid escapes",
			input: `{"x":"\"\\\/\b\f\n\r\t\u12aF"}`,
			want:  `{"x":"\"\\\/\b\f\n\r\t\u12aF"}`,
		},
		{
			name:  "invalid escapes",
			input: `{"path":"C:\q\z"}`,
			want:  `{"path":"C:\\q\\z"}`,
		},
		{
			name:  "raw controls",
			input: "\"a\x00\b\f\n\r\t\x1fb\"",
			want:  `"a\u0000\b\f\n\r\t\u001fb"`,
		},
		{
			name:  "outside string unchanged",
			input: "{\n\t}",
			want:  "{\n\t}",
		},
		{
			name:  "trailing backslash",
			input: `"abc\`,
			want:  `"abc\\`,
		},
		{
			name:  "incomplete unicode escape remains",
			input: `"\u12`,
			want:  `"\u12`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RepairJSON(test.input); got != test.want {
				t.Fatalf("RepairJSON(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestParseJSONWithRepair(t *testing.T) {
	value, err := ParseJSONWithRepair(`{"path":"C:\q","line":"a` + "\n" + `b"}`)
	if err != nil {
		t.Fatalf("ParseJSONWithRepair() error = %v", err)
	}
	want := map[string]any{"path": `C:\q`, "line": "a\nb"}
	if !reflect.DeepEqual(value, want) {
		t.Fatalf("ParseJSONWithRepair() = %#v, want %#v", value, want)
	}

	for _, input := range []string{`{"a":`, `{not json}`} {
		if _, err := ParseJSONWithRepair(input); err == nil {
			t.Fatalf("ParseJSONWithRepair(%q) unexpectedly succeeded", input)
		}
	}
}

func TestParseStreamingJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  any
	}{
		{name: "empty", input: "", want: map[string]any{}},
		{name: "ecmascript whitespace", input: "\ufeff \n", want: map[string]any{}},
		{name: "complete", input: `{"name":"pi","n":1}`, want: map[string]any{"name": "pi", "n": float64(1)}},
		{
			name:  "nested partial",
			input: `{"a":1,"b":[true,{"c":"hel`,
			want: map[string]any{
				"a": float64(1),
				"b": []any{true, map[string]any{"c": "hel"}},
			},
		},
		{name: "unfinished member omitted", input: `{"a":1,"b":`, want: map[string]any{"a": float64(1)}},
		{name: "invalid escape repaired", input: `{"path":"C:\q"}`, want: map[string]any{"path": `C:\q`}},
		{name: "partial root string repaired", input: "\"line\nnext", want: "line\nnext"},
		{name: "malformed", input: `wrong`, want: map[string]any{}},
		{name: "incomplete null is nullish", input: `n`, want: map[string]any{}},
		{name: "complete null stays null", input: `null`, want: nil},
		{name: "trailing comma", input: `{"a":1,}`, want: map[string]any{"a": float64(1)}},
		{name: "partial parser ignores object suffix", input: `{"a":1}garbage`, want: map[string]any{"a": float64(1)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParseStreamingJSON(test.input)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ParseStreamingJSON(%q) = %#v, want %#v", test.input, got, test.want)
			}
		})
	}

	got := ParseStreamingJSON("Infinity")
	if value, ok := got.(float64); !ok || !math.IsInf(value, 1) {
		t.Fatalf("ParseStreamingJSON(Infinity) = %#v, want +Inf", got)
	}
}

func TestParsePreservesPartialJSONQuirks(t *testing.T) {
	assertParsed(t, `{"a":1,"b":`, AllowAll, map[string]any{"a": float64(1)})
	assertParsed(t, `[1,2,`, AllowAll, []any{float64(1), float64(2)})
	assertParsed(t, `{"a":1,}`, AllowAll, map[string]any{"a": float64(1)})
	assertParsed(t, `truegarbage`, AllowAll, true)

	if _, err := Parse("123.", AllowNumber); err == nil {
		t.Fatal("Parse(123.) unexpectedly succeeded; partial-json 0.1.7 rejects it")
	}
}

func TestParseStreamingJSONReturnsFreshFallbackObjects(t *testing.T) {
	first := ParseStreamingJSON("").(map[string]any)
	first["changed"] = true
	second := ParseStreamingJSON("").(map[string]any)
	if len(second) != 0 {
		t.Fatalf("second fallback object = %#v, want a fresh empty object", second)
	}
}

func TestStringifyStreamingJSONPreservesJavaScriptObjectOrder(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `{"text":"hello","mode":"plain",`, want: `{"text":"hello","mode":"plain"}`},
		{input: `{"outer":{"z":1,"a":2},"tail":"par`, want: `{"outer":{"z":1,"a":2},"tail":"par"}`},
		{input: `{"2":"two","keep":1,"1":"one","keep":2}`, want: `{"1":"one","2":"two","keep":2}`},
		{input: `{"n":1e2,"negativeZero":-0,"overflow":Infinity}`, want: `{"n":100,"negativeZero":0,"overflow":null}`},
		{input: "{\"path\":\"A\\H\",\"text\":\"col1\tcol2\"}", want: `{"path":"A\\H","text":"col1\tcol2"}`},
	}
	for _, test := range tests {
		encoded, err := StringifyStreamingJSON(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != test.want {
			t.Fatalf("StringifyStreamingJSON(%q) = %s, want %s", test.input, encoded, test.want)
		}
	}
}

func FuzzParseStreamingJSONNeverPanics(f *testing.F) {
	for _, seed := range []string{
		"",
		`{"tool":"read","path":"/tmp/a`,
		`[1,{"x":"\u12`,
		"\"raw\x00control",
		`{"bad":"\q"}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, input string) {
		ParseStreamingJSON(input)
	})
}

func assertParsed(t *testing.T, input string, allow Allow, want any) {
	t.Helper()
	got, err := Parse(input, allow)
	if err != nil {
		t.Fatalf("Parse(%q, %09b) error = %v", input, allow, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Parse(%q, %09b) = %#v, want %#v", input, allow, got, want)
	}
}

func assertErrorType[E error](t *testing.T, input string, allow Allow) {
	t.Helper()
	_, err := Parse(input, allow)
	if err == nil {
		t.Fatalf("Parse(%q, %09b) unexpectedly succeeded", input, allow)
	}
	var target E
	if !errors.As(err, &target) {
		t.Fatalf("Parse(%q, %09b) error = %T (%v), want %T", input, allow, err, err, target)
	}
}
