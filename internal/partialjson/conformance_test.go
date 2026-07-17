package partialjson

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/OrdalieTech/pi-go/conformance/runner"
)

type conformanceFixture struct {
	Cases []conformanceCase `json:"cases"`
}

type conformanceCase struct {
	Name          string          `json:"name"`
	Operation     string          `json:"operation"`
	Input         *string         `json:"input"`
	Allow         *int            `json:"allow"`
	Expected      json.RawMessage `json:"expected"`
	ExpectedError string          `json:"expectedError"`
}

func TestUpstreamPartialJSONConformance(t *testing.T) {
	var fixture conformanceFixture
	runner.LoadJSON(t, "F1", "partialjson.json", &fixture)

	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			input := ""
			if test.Input != nil {
				input = *test.Input
			}

			var (
				got any
				err error
			)
			switch test.Operation {
			case "partialParse":
				if test.Allow == nil {
					got, err = Parse(input)
				} else {
					got, err = Parse(input, Allow(*test.Allow))
				}
			case "repairJson":
				got = RepairJSON(input)
			case "parseJsonWithRepair":
				got, err = ParseJSONWithRepair(input)
			case "parseStreamingJson":
				got = ParseStreamingJSON(input)
			default:
				t.Fatalf("unknown operation %q", test.Operation)
			}

			if test.ExpectedError != "" {
				if err == nil {
					t.Fatalf("expected %s error, got value %#v", test.ExpectedError, got)
				}
				if kind := conformanceErrorKind(err); kind != test.ExpectedError {
					t.Fatalf("error kind = %q (%v), want %q", kind, err, test.ExpectedError)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			gotJSON, err := json.Marshal(normalizeConformanceValue(got))
			if err != nil {
				t.Fatalf("encode result: %v", err)
			}
			gotCanonical, err := runner.CanonicalJSON(gotJSON)
			if err != nil {
				t.Fatalf("canonicalize result: %v", err)
			}
			wantCanonical, err := runner.CanonicalJSON(test.Expected)
			if err != nil {
				t.Fatalf("canonicalize fixture: %v", err)
			}
			if diff := runner.ByteDiff(wantCanonical, gotCanonical); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func conformanceErrorKind(err error) string {
	var partial *PartialJSONError
	if errors.As(err, &partial) {
		return "PartialJSON"
	}
	var malformed *MalformedJSONError
	if errors.As(err, &malformed) {
		return "MalformedJSON"
	}
	return "SyntaxError"
}

func normalizeConformanceValue(value any) any {
	switch value := value.(type) {
	case float64:
		switch {
		case math.IsNaN(value):
			return map[string]any{"$number": "NaN"}
		case math.IsInf(value, 1):
			return map[string]any{"$number": "Infinity"}
		case math.IsInf(value, -1):
			return map[string]any{"$number": "-Infinity"}
		default:
			return value
		}
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = normalizeConformanceValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, item := range value {
			result[key] = normalizeConformanceValue(item)
		}
		return result
	default:
		return value
	}
}
