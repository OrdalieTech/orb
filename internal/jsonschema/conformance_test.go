package jsonschema

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/OrdalieTech/pigo/conformance/runner"
)

type schemaGoldenChild struct {
	Name string  `json:"name" jsonschema:"description=Child name"`
	Note *string `json:"note,omitempty" jsonschema_description:"Optional note"`
}

type schemaGoldenFilter struct {
	Pattern string `json:"pattern"`
}

type schemaGoldenInput struct {
	Path     string              `json:"path" jsonschema:"description=Path to inspect <>&"`
	Children []schemaGoldenChild `json:"children" jsonschema:"description=Nested children"`
	Labels   []string            `json:"labels,omitempty" jsonschema:"description=Optional labels"`
	Mode     string              `json:"mode" jsonschema:"enum=read,enum=write,description=Operation mode"`
	Filter   *schemaGoldenFilter `json:"filter,omitempty"`
}

type schemaGoldenRequiredOptional struct {
	Required string `json:"required,omitempty" jsonschema:"required"`
	Optional string `json:"optional" jsonschema:"optional" jsonschema_description:"one, two"`
}

func TestFromStructMatchesUpstreamTypeBoxGoldens(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F1", "schema.json", &fixture)
	if len(fixture.Cases) != 3 {
		t.Fatalf("schema golden case count = %d, want 3", len(fixture.Cases))
	}

	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			got, err := schemaGolden(fixtureCase.Name)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, err := got.MarshalJSON()
			if err != nil {
				t.Fatalf("marshal schema: %v", err)
			}
			gotCanonical, err := runner.CanonicalJSONLexemes(gotJSON)
			if err != nil {
				t.Fatalf("canonicalize generated schema: %v", err)
			}
			wantCanonical, err := runner.CanonicalJSONLexemes(fixtureCase.Schema)
			if err != nil {
				t.Fatalf("canonicalize TypeBox golden: %v", err)
			}
			if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func schemaGolden(name string) (Schema, error) {
	switch name {
	case "nested-object":
		return FromStruct[schemaGoldenInput]()
	case "required-optional":
		return FromStruct[schemaGoldenRequiredOptional]()
	case "string-enum":
		return StringEnum("add", "subtract", "multiply", "divide"), nil
	default:
		return nil, fmt.Errorf("unknown TypeBox schema golden %q", name)
	}
}
