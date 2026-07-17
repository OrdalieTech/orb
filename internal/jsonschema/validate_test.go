package jsonschema

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestValidateMatchesUpstreamPlainSchemaPrimitiveCoercion(t *testing.T) {
	cases := []struct {
		name     string
		schema   string
		input    any
		expected any
	}{
		{"number-string", `{"type":"number"}`, "42", float64(42)},
		{"number-boolean", `{"type":"number"}`, true, float64(1)},
		{"number-null", `{"type":"number"}`, nil, float64(0)},
		{"integer-string", `{"type":"integer"}`, "42", float64(42)},
		{"boolean-true", `{"type":"boolean"}`, "true", true},
		{"boolean-false", `{"type":"boolean"}`, "false", false},
		{"boolean-one", `{"type":"boolean"}`, float64(1), true},
		{"boolean-zero", `{"type":"boolean"}`, float64(0), false},
		{"string-null", `{"type":"string"}`, nil, ""},
		{"string-boolean", `{"type":"string"}`, true, "true"},
		{"string-number-fixed", `{"type":"string"}`, float64(0.00001), "0.00001"},
		{"string-negative-zero", `{"type":"string"}`, math.Copysign(0, -1), "0"},
		{"number-hex", `{"type":"number"}`, "0x10", float64(16)},
		{"null-empty", `{"type":"null"}`, "", nil},
		{"null-zero", `{"type":"null"}`, float64(0), nil},
		{"null-false", `{"type":"null"}`, false, nil},
		{"matching-union-is-not-coerced", `{"type":["number","string"]}`, "1", "1"},
		{"union-falls-through", `{"type":["boolean","number"]}`, "1", float64(1)},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := Validate(Schema(testCase.schema), testCase.input)
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if !reflect.DeepEqual(got, testCase.expected) {
				t.Fatalf("got %#v (%T), want %#v (%T)", got, got, testCase.expected, testCase.expected)
			}
		})
	}
}

func TestValidateRejectsInvalidUpstreamCoercions(t *testing.T) {
	cases := []struct {
		schema string
		input  any
	}{
		{`{"type":"boolean"}`, "1"},
		{`{"type":"boolean"}`, "0"},
		{`{"type":"null"}`, "null"},
		{`{"type":"integer"}`, "42.1"},
		{`{"type":"number"}`, "+0x10"},
	}
	for _, testCase := range cases {
		if _, err := Validate(Schema(testCase.schema), testCase.input); err == nil {
			t.Fatalf("Validate(%s, %#v) succeeded", testCase.schema, testCase.input)
		}
	}
}

func TestValidateRecursivelyCoercesObjectsArraysAndAdditionalProperties(t *testing.T) {
	schema := Schema(`{
		"type":"object",
		"properties":{
			"count":{"type":"integer"},
			"items":{"type":"array","items":{"type":"boolean"}}
		},
		"required":["count","items"],
		"additionalProperties":{"type":"number"}
	}`)
	original := map[string]any{
		"count": "42",
		"items": []any{"true", float64(0)},
		"extra": "1.5",
	}
	got, err := Validate(schema, original)
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	want := map[string]any{
		"count": float64(42),
		"items": []any{true, false},
		"extra": float64(1.5),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if original["count"] != "42" {
		t.Fatalf("validation mutated caller arguments: %#v", original)
	}
}

func TestValidateUsesFirstPassingUnionCoercion(t *testing.T) {
	schema := Schema(`{"anyOf":[{"type":"integer"},{"type":"boolean"}]}`)
	got, err := Validate(schema, "7")
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if got != float64(7) {
		t.Fatalf("got %#v, want 7", got)
	}

	overlapping := Schema(`{"oneOf":[{"type":"number"},{"type":"integer"}]}`)
	if _, err := Validate(overlapping, float64(1)); err == nil {
		t.Fatal("expected overlapping oneOf to fail")
	}
}

func TestValidateEnforcesCommonToolSchemaKeywords(t *testing.T) {
	schema := Schema(`{
		"type":"object",
		"properties":{
			"mode":{"type":"string","enum":["read","write"]},
			"path":{"type":"string","minLength":2,"pattern":"^[a-z]"},
			"limit":{"type":"number","minimum":1,"maximum":3},
			"tags":{"type":"array","minItems":1,"uniqueItems":true,"items":{"type":"string"}}
		},
		"required":["mode","path","limit","tags"],
		"additionalProperties":false
	}`)
	valid := map[string]any{"mode": "read", "path": "ab", "limit": float64(2), "tags": []any{"x"}}
	if _, err := Validate(schema, valid); err != nil {
		t.Fatalf("valid value failed: %v", err)
	}
	invalid := map[string]any{
		"mode": "delete", "path": "1", "limit": float64(4), "tags": []any{"x", "x"}, "extra": true,
	}
	if _, err := Validate(schema, invalid); err == nil {
		t.Fatal("invalid value succeeded")
	}
}

func TestValidateToolArgumentsFormatsUpstreamError(t *testing.T) {
	schema := Schema(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"]}`)
	_, err := ValidateToolArguments("echo", schema, map[string]any{"value": "42.1"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	want := strings.Join([]string{
		`Validation failed for tool "echo":`,
		`  - value: must be integer`,
		``,
		`Received arguments:`,
		`{`,
		`  "value": "42.1"`,
		`}`,
	}, "\n")
	if err.Error() != want {
		t.Fatalf("error mismatch:\n got: %s\nwant: %s", err, want)
	}
}

func TestValidateRequiredPathMatchesUpstreamFormatting(t *testing.T) {
	schema := Schema(`{"type":"object","properties":{"nested":{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}},"required":["nested"]}`)
	_, err := ValidateToolArguments("echo", schema, map[string]any{"nested": map[string]any{}})
	if err == nil || !strings.Contains(err.Error(), "  - nested.value: must have required properties value") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateAdditionalPropertiesMatchesTypeBoxErrorShape(t *testing.T) {
	schema := Schema(`{"type":"object","properties":{"z":{"type":"integer"}},"required":["missing"],"additionalProperties":false}`)
	_, err := ValidateToolArguments("echo", schema, map[string]any{"q": true, "z": "bad"})
	if err == nil {
		t.Fatal("expected validation error")
	}
	required := strings.Index(err.Error(), "  - missing: must have required properties missing")
	additional := strings.Index(err.Error(), "  - root: must not have additional properties")
	property := strings.Index(err.Error(), "  - z: must be integer")
	if required < 0 || additional < required || property < additional {
		t.Fatalf("unexpected TypeBox error order:\n%s", err)
	}
	if strings.Count(err.Error(), "must not have additional properties") != 1 {
		t.Fatalf("expected one aggregate additional-properties error:\n%s", err)
	}
}

func TestValidateToolArgumentsJSONPreservesReceivedMemberOrder(t *testing.T) {
	schema := Schema(`{"type":"object","properties":{"z":{"type":"integer"},"a":{"type":"integer"}},"required":["z","a"]}`)
	_, err := ValidateToolArgumentsJSON("echo", schema, []byte(`{"z":"bad","a":"also-bad"}`))
	if err == nil {
		t.Fatal("expected validation error")
	}
	wantReceived := strings.Join([]string{
		`Received arguments:`,
		`{`,
		`  "z": "bad",`,
		`  "a": "also-bad"`,
		`}`,
	}, "\n")
	if !strings.Contains(err.Error(), wantReceived) {
		t.Fatalf("argument order changed:\n%s", err)
	}
	zIssue := strings.Index(err.Error(), "  - z: must be integer")
	aIssue := strings.Index(err.Error(), "  - a: must be integer")
	if zIssue < 0 || aIssue < 0 || zIssue > aIssue {
		t.Fatalf("schema property error order changed:\n%s", err)
	}
}

func TestValidateBooleanSchemas(t *testing.T) {
	if _, err := Validate(Schema(`true`), map[string]any{"anything": true}); err != nil {
		t.Fatalf("true schema: %v", err)
	}
	if _, err := Validate(Schema(`false`), nil); err == nil {
		t.Fatal("false schema accepted value")
	}
}

func TestValidateMatchesTypeBoxCompositeAndConstraintErrors(t *testing.T) {
	_, err := ValidateToolArguments("echo", Schema(`{"anyOf":[{"type":"string"},{"type":"number"}]}`), map[string]any{})
	if err == nil {
		t.Fatal("expected anyOf error")
	}
	wantOrder := []string{"must be string", "must be number", "must match a schema in anyOf"}
	previous := -1
	for _, message := range wantOrder {
		index := strings.Index(err.Error(), message)
		if index <= previous {
			t.Fatalf("unexpected anyOf error order:\n%s", err)
		}
		previous = index
	}

	_, err = ValidateToolArguments("echo", Schema(`{"type":"number","minimum":2,"maximum":3}`), float64(1))
	if err == nil || !strings.Contains(err.Error(), "must be >= 2") {
		t.Fatalf("unexpected minimum error: %v", err)
	}

	_, err = ValidateToolArguments("echo", Schema(`{"type":"array","uniqueItems":true}`), []any{"x", "x"})
	if err == nil || !strings.Contains(err.Error(), "  - root: must not have duplicate items") {
		t.Fatalf("unexpected uniqueItems error: %v", err)
	}
}
