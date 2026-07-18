package api

import (
	"encoding/json"
	"testing"
)

func TestNormalizeGoogleResponseSchema(t *testing.T) {
	input := json.RawMessage(`{"type":"object","properties":{"answer":{"type":["string","null"]}},"additionalProperties":false}`)
	got, err := normalizeGoogleResponseSchema(input)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"OBJECT","properties":{"answer":{"nullable":true,"type":"STRING"}}}`
	if string(got) != want {
		t.Fatalf("normalized schema\nwant: %s\n got: %s", want, got)
	}
}

func TestNormalizeGoogleResponseSchemaRejectsTypeAndAnyOf(t *testing.T) {
	_, err := normalizeGoogleResponseSchema(json.RawMessage(`{"type":"string","anyOf":[{"type":"string"}]}`))
	if err == nil || err.Error() != "type and anyOf cannot be both populated." {
		t.Fatalf("schema error = %v", err)
	}
}

func TestNormalizeGoogleResponseSchemaUsesJSTruthiness(t *testing.T) {
	got, err := normalizeGoogleResponseSchema(json.RawMessage(`{"type":"","anyOf":[{"type":"string"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"TYPE_UNSPECIFIED","anyOf":[{"type":"STRING"}]}`
	if string(got) != want {
		t.Fatalf("normalized schema\nwant: %s\n got: %s", want, got)
	}
}

func TestGoogleSystemContentSkipsPartUnionConversion(t *testing.T) {
	got, err := googleSystemInstruction(json.RawMessage(`{"parts":["raw part"],"role":"model"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"parts":[{}],"role":"model"}`
	if string(got) != want {
		t.Fatalf("system instruction\nwant: %s\n got: %s", want, got)
	}

	got, err = googleSystemInstruction(json.RawMessage(`{"parts":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"parts":[]}` {
		t.Fatalf("empty system content = %s", got)
	}
}

func TestGooglePayloadErrorPrecedenceMatchesMldev(t *testing.T) {
	_, err := googleWirePayload(googleDecodedParameters{
		Contents: json.RawMessage(`[{"parts":[{"text":"hello"}],"role":"user"}]`),
		Config:   json.RawMessage(`{"responseSchema":{"type":"null"},"routingConfig":null}`),
	})
	if err == nil || err.Error() != "type: null can not be the only possible type for the field." {
		t.Fatalf("payload error = %v", err)
	}
}

func TestGoogleToolSchemaErrorPrecedesUnsupportedField(t *testing.T) {
	_, err := googleWireTool(json.RawMessage(`{"retrieval":null,"functionDeclarations":[{"parameters":{"type":"null"}}]}`))
	if err == nil || err.Error() != "type: null can not be the only possible type for the field." {
		t.Fatalf("tool error = %v", err)
	}
}

func TestGoogleFunctionSchemaKeepsLegacyWhenJSONSchemaExists(t *testing.T) {
	input := json.RawMessage(`[{"name":"f","parameters":{"$schema":"draft","type":"object"},"parametersJsonSchema":{"type":"number"}}]`)
	got, err := googleFunctionDeclarations(input)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(input) {
		t.Fatalf("function declarations\nwant: %s\n got: %s", input, got)
	}
}

func TestGoogleImageConfigRejectsUnsupportedMldevField(t *testing.T) {
	for _, input := range []string{`{"personGeneration":"ALLOW_ALL"}`, `{"personGeneration":null}`} {
		_, err := googleImageConfig(json.RawMessage(input))
		if err == nil || err.Error() != "personGeneration parameter is not supported in Gemini API." {
			t.Fatalf("image config %s error = %v", input, err)
		}
	}
}
