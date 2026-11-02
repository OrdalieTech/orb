package jsonschema_test

import (
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/conformance/runner"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

// orbOwnedValidation are the F1 cases where Orb drops optional nulls behind a
// local $ref, which pi-schema keeps (DECISIONS.md divergence ledger, "Strict
// sampling with internal $ref"): Orb samples such schemas strict, so a null
// there is the model omitting the property.
var orbOwnedValidation = map[string]string{
	"optional-null-ref-property": `{}`,
	"optional-null-behind-ref":   `{"a":{}}`,
	"ref-root-to-defs":           `{"n":7}`,
}

func TestF1ToolArgumentValidation(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name           string          `json:"name"`
			Tool           string          `json:"tool"`
			Schema         json.RawMessage `json:"schema"`
			Arguments      string          `json:"arguments"`
			ArgumentsError string          `json:"argumentsError"`
			Value          json.RawMessage `json:"value"`
			Error          *string         `json:"error"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F1", "validation.json", &fixture)
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			value, err := jsonschema.ValidateToolArgumentsJSON(fixtureCase.Tool, jsonschema.Schema(fixtureCase.Schema), []byte(fixtureCase.Arguments), fixtureCase.ArgumentsError)
			want := fixtureCase.Value
			if owned, ok := orbOwnedValidation[fixtureCase.Name]; ok {
				if canonical(t, want) == canonical(t, []byte(owned)) {
					t.Fatalf("upstream now agrees with the Orb-owned value %s", owned)
				}
				want = json.RawMessage(owned)
			}
			if fixtureCase.Error != nil {
				if err == nil || err.Error() != *fixtureCase.Error {
					t.Fatalf("error = %v\nwant %s", err, *fixtureCase.Error)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if diff := runner.ByteDiff([]byte(canonical(t, want)), []byte(canonical(t, got))); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func canonical(t *testing.T, data []byte) string {
	t.Helper()
	normalized, err := runner.CanonicalJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	return string(normalized)
}
