package ai_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestToolCallPreservesJSONNumber(t *testing.T) {
	message, err := ai.UnmarshalMessage([]byte(`{"role":"assistant","content":[{"type":"toolCall","id":"1","name":"n","arguments":{"large":9007199254740993}}],"api":"x","provider":"x","model":"x","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":0}`))
	if err != nil {
		t.Fatal(err)
	}
	assistant, ok := message.(*ai.AssistantMessage)
	if !ok {
		t.Fatalf("message type = %T, want *ai.AssistantMessage", message)
	}
	if _, ok := assistant.Content[0].(*ai.ToolCall); !ok {
		t.Fatalf("content type = %T, want *ai.ToolCall", assistant.Content[0])
	}
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		t.Fatal(err)
	}
	content := raw["content"].([]any)
	arguments := content[0].(map[string]any)["arguments"].(map[string]any)
	if got := arguments["large"].(json.Number).String(); got != "9007199254740993" {
		t.Fatalf("large number = %s", got)
	}
}

func TestUnmarshalMessageRejectsUnknownRole(t *testing.T) {
	if _, err := ai.UnmarshalMessage([]byte(`{"role":"system"}`)); err == nil {
		t.Fatal("unknown role accepted")
	}
}

func TestMarshalMatchesJSONStringifyEscaping(t *testing.T) {
	value := struct {
		Text string `json:"text"`
	}{Text: "<>&\u2028\u2029\\u2028"}
	encoded, err := ai.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"text\":\"<>&\u2028\u2029\\\\u2028\"}"
	if string(encoded) != want {
		t.Fatalf("encoded = %q, want %q", encoded, want)
	}
}

func TestToolCallMarshalUsesJSONStringifyNonFiniteNumbers(t *testing.T) {
	call := &ai.ToolCall{
		ID:   "tool-1",
		Name: "numbers",
		Arguments: map[string]any{
			"infinity":     math.Inf(1),
			"negativeZero": math.Copysign(0, -1),
			"nested":       []any{math.NaN(), float64(1)},
		},
	}
	encoded, err := ai.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"toolCall","id":"tool-1","name":"numbers","arguments":{"infinity":null,"negativeZero":0,"nested":[null,1]}}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
}
