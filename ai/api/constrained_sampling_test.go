package api

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

func TestMakeStrictJSONSchemaDerivesProviderSchema(t *testing.T) {
	parameters := jsonschema.Schema(`{"type":"object","properties":` +
		`{"path":{"type":"string"},"offset":{"type":"number"},` +
		`"metadata":{"type":"object","properties":{"enabled":{"type":"boolean"}},"required":[]},` +
		`"nullable":{"anyOf":[{"type":"string"},{"type":"null"}]}},"required":["path","metadata"]}`)
	original := string(parameters)

	strict, err := makeStrictJSONSchema(parameters)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","properties":` +
		`{"path":{"type":"string"},"offset":{"anyOf":[{"type":"number"},{"type":"null"}]},` +
		`"metadata":{"type":"object","properties":{"enabled":{"anyOf":[{"type":"boolean"},{"type":"null"}]}},` +
		`"required":["enabled"],"additionalProperties":false},` +
		`"nullable":{"anyOf":[{"type":"string"},{"type":"null"}]}},` +
		`"required":["path","offset","metadata","nullable"],"additionalProperties":false}`
	if string(strict) != want {
		t.Fatalf("strict schema =\n%s\nwant\n%s", strict, want)
	}
	if string(parameters) != original {
		t.Fatalf("tool schema was mutated: %s", parameters)
	}
}

func TestStrictJSONSchemaRejectsUnsupportedShapes(t *testing.T) {
	for _, test := range []struct {
		parameters jsonschema.Schema
		reason     string
	}{
		{
			jsonschema.Schema(`{"type":"object","properties":{"metadata":{"type":"object","additionalProperties":{"type":"string"}}},"required":["metadata"]}`),
			"schema-valued or true additionalProperties is unsupported",
		},
		{
			jsonschema.Schema(`{"allOf":[{"type":"object"},{"type":"object"}],"type":"object"}`),
			"allOf schemas are unsupported",
		},
		{
			jsonschema.Schema(`{"type":"object","properties":{"value":{"anyOf":[{"type":"object","properties":{"nested":{"type":"string"}}},{"type":"null"}]}},"required":["value"]}`),
			"object and array unions are unsupported",
		},
		{
			jsonschema.Schema(`{"type":"object","properties":{"child":{"$ref":"https://example.com/child.json"}},"required":["child"]}`),
			"$ref schemas are unsupported",
		},
	} {
		if _, err := makeStrictJSONSchema(test.parameters); err == nil || err.Error() != test.reason {
			t.Fatalf("makeStrictJSONSchema error = %v, want %q", err, test.reason)
		}
		tool := ai.Tool{
			Name: "echo", Description: "emit text", Parameters: test.parameters,
			ConstrainedSampling: strictSamplingTestConfig(ai.ConstrainedSamplingPrefer),
		}
		strict, err := resolveJSONSchemaStrictSampling(tool, true)
		if err != nil || strict != nil {
			t.Fatalf("prefer resolved to %v, %v", strict, err)
		}
		converted, err := convertResponsesToolsWithOptions([]ai.Tool{tool}, responsesToolOptions{supportsStrictMode: true})
		if err != nil {
			t.Fatal(err)
		}
		if converted[0].Strict == nil || *converted[0].Strict ||
			string(converted[0].Parameters) != string(test.parameters) {
			t.Fatalf("converted tool = %#v", converted[0])
		}

		tool.ConstrainedSampling = strictSamplingTestConfig(ai.ConstrainedSamplingRequire)
		_, err = resolveJSONSchemaStrictSampling(tool, true)
		if err == nil || !strings.Contains(err.Error(), test.reason) {
			t.Fatalf("require error = %v, want it to mention %q", err, test.reason)
		}
	}
}
