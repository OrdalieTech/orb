package ai_test

import (
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestModelRequiredModalitiesMarshalAsArrays(t *testing.T) {
	model := ai.Model{}
	encoded, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	input, ok := value["input"].([]any)
	if !ok || len(input) != 0 {
		t.Fatalf("input = %#v, want []", value["input"])
	}
}

func TestCompatPreservesExplicitFalseAndEmptyCollections(t *testing.T) {
	falseValue := false
	empty := []string{}
	compat := ai.OpenAICompletionsCompat{
		SupportsStore: &falseValue,
		OpenRouterRouting: &ai.OpenRouterRouting{
			Only: &empty,
		},
	}
	encoded, err := json.Marshal(compat)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"supportsStore":false,"openRouterRouting":{"only":[]}}`
	if string(encoded) != want {
		t.Fatalf("compat = %s, want %s", encoded, want)
	}
}

func TestStreamOptionsPreserveExplicitZeroAndEmpty(t *testing.T) {
	zeroFloat := 0.0
	zeroInt := int64(0)
	empty := ""
	options := ai.StreamOptions{
		Temperature:     &zeroFloat,
		MaxTokens:       &zeroFloat,
		APIKey:          &empty,
		SessionID:       &empty,
		MaxRetryDelayMS: &zeroInt,
	}
	encoded, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"temperature":0,"maxTokens":0,"apiKey":"","sessionId":"","maxRetryDelayMs":0}`
	if string(encoded) != want {
		t.Fatalf("options = %s, want %s", encoded, want)
	}
}

func TestPublicJSONSchemaFacade(t *testing.T) {
	type input struct {
		Path string `json:"path" jsonschema:"description=Path to inspect"`
	}
	schema, err := ai.JSONSchemaFrom[input]()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"object","required":["path"],"properties":{"path":{"type":"string","description":"Path to inspect"}}}`
	if string(encoded) != want {
		t.Fatalf("schema = %s, want %s", encoded, want)
	}
	if enum, err := json.Marshal(ai.JSONStringEnumSchema("read", "write")); err != nil || string(enum) != `{"type":"string","enum":["read","write"]}` {
		t.Fatalf("enum schema = %s, %v", enum, err)
	}
}
