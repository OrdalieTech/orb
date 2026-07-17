package jsonschema

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestSchemaRawJSON(t *testing.T) {
	raw := []byte(`{ "type": "object", "properties": { "x": { "type": "string" } } }`)
	var schema Schema
	if err := schema.UnmarshalJSON(raw); err != nil {
		t.Fatalf("UnmarshalJSON: %v", err)
	}

	got, err := schema.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Fatalf("raw schema changed:\n got: %s\nwant: %s", got, raw)
	}

	wrapped, err := json.Marshal(struct {
		Parameters Schema `json:"parameters"`
	}{Parameters: schema})
	if err != nil {
		t.Fatalf("marshal wrapper: %v", err)
	}
	if !bytes.Equal(wrapped, []byte(`{"parameters":{"type":"object","properties":{"x":{"type":"string"}}}}`)) {
		t.Fatalf("schema was not embedded as a JSON value: %s", wrapped)
	}
}

func TestSchemaZeroValueIsEmptySchema(t *testing.T) {
	var schema Schema
	got, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(got) != `{}` {
		t.Fatalf("got %s, want {}", got)
	}
}

func TestSchemaRejectsInvalidJSONWithoutMutation(t *testing.T) {
	schema := Schema(`{"type":"string"}`)
	if err := schema.UnmarshalJSON([]byte(`{"type":`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if string(schema) != `{"type":"string"}` {
		t.Fatalf("invalid input mutated schema: %s", schema)
	}

	invalid := Schema(`{"type":`)
	if _, err := invalid.MarshalJSON(); err == nil {
		t.Fatal("expected invalid schema error")
	}
}

func TestStringEnumUsesProviderCompatibleShape(t *testing.T) {
	got, err := json.Marshal(StringEnum("add", "subtract", "multiply", "divide"))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"type":"string","enum":["add","subtract","multiply","divide"]}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestFromStructMatchesPlainTypeBoxShape(t *testing.T) {
	type child struct {
		Name string  `json:"name" jsonschema:"description=Child name"`
		Note *string `json:"note,omitempty" jsonschema_description:"Optional note"`
	}
	type filter struct {
		Pattern string `json:"pattern"`
	}
	type input struct {
		Path     string            `json:"path" jsonschema:"description=Path to inspect"`
		Children []child           `json:"children" jsonschema:"description=Nested children"`
		Labels   []string          `json:"labels,omitempty" jsonschema:"description=Optional labels"`
		Mode     string            `json:"mode" jsonschema:"enum=read,enum=write,description=Operation mode"`
		Limit    *int              `json:"limit,omitempty"`
		Filter   *filter           `json:"filter,omitempty"`
		Metadata map[string]string `json:"metadata,omitempty"`
		Hidden   string            `json:"-"`
		ignored  string
	}
	_ = input{ignored: "not included"}

	schema, err := FromStruct[*input]()
	if err != nil {
		t.Fatalf("FromStruct: %v", err)
	}
	want := strings.Join([]string{
		`{"type":"object","required":["path","children","mode"],"properties":{`,
		`"path":{"type":"string","description":"Path to inspect"},`,
		`"children":{"type":"array","items":{"type":"object","required":["name"],"properties":{`,
		`"name":{"type":"string","description":"Child name"},`,
		`"note":{"type":"string","description":"Optional note"}}},"description":"Nested children"},`,
		`"labels":{"type":"array","items":{"type":"string"},"description":"Optional labels"},`,
		`"mode":{"type":"string","enum":["read","write"],"description":"Operation mode"},`,
		`"limit":{"type":"integer"},`,
		`"filter":{"type":"object","required":["pattern"],"properties":{"pattern":{"type":"string"}}},`,
		`"metadata":{"type":"object","additionalProperties":{"type":"string"}}}}`,
	}, "")
	if string(schema) != want {
		t.Fatalf("schema mismatch:\n got: %s\nwant: %s", schema, want)
	}
}

func TestFromStructRequiredOverridesAndDescription(t *testing.T) {
	type input struct {
		Required string `json:"required,omitempty" jsonschema:"required"`
		Optional string `json:"optional" jsonschema:"optional" jsonschema_description:"one, two"`
	}

	schema, err := FromStruct[input]()
	if err != nil {
		t.Fatalf("FromStruct: %v", err)
	}
	want := `{"type":"object","required":["required"],"properties":{"required":{"type":"string"},"optional":{"type":"string","description":"one, two"}}}`
	if string(schema) != want {
		t.Fatalf("got %s, want %s", schema, want)
	}
}

func TestFromStructRejectsUnsupportedShapes(t *testing.T) {
	if _, err := FromStruct[string](); err == nil {
		t.Fatal("expected non-struct error")
	}

	type recursive struct {
		Next *recursive `json:"next,omitempty"`
	}
	if _, err := FromStruct[recursive](); err == nil {
		t.Fatal("expected recursive type error")
	}

	type numericEnum struct {
		Value int `json:"value" jsonschema:"enum=one"`
	}
	if _, err := FromStruct[numericEnum](); err == nil {
		t.Fatal("expected non-string enum error")
	}
}
