package ai_test

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestToolCallMatchesJSONStringifyNumberSemantics(t *testing.T) {
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
	if got := arguments["large"].(json.Number).String(); got != "9007199254740992" {
		t.Fatalf("large number = %s, want JavaScript Number rounding", got)
	}
}

func TestToolCallPreservesArgumentOrderWhileUnchanged(t *testing.T) {
	message, err := ai.UnmarshalMessage([]byte(`{"role":"assistant","content":[{"type":"toolCall","id":"1","name":"n","arguments": { "text":"first", "mode":"plain", "metadata":{"count":1}, "escaped":"\u003c" }}],"api":"x","provider":"x","model":"x","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":0}`))
	if err != nil {
		t.Fatal(err)
	}
	assistant := message.(*ai.AssistantMessage)
	call := assistant.Content[0].(*ai.ToolCall)
	encoded, err := ai.MarshalToolCallArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"text":"first","mode":"plain","metadata":{"count":1},"escaped":"<"}`
	if string(encoded) != want {
		t.Fatalf("arguments = %s, want %s", encoded, want)
	}
	messageJSON, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(messageJSON, []byte(`"arguments":{"text":"first","mode":"plain","metadata":{"count":1},"escaped":"<"}`)) {
		t.Fatalf("message lost argument order: %s", messageJSON)
	}
}

func TestToolCallDiscardsRetainedOrderAfterMutation(t *testing.T) {
	message, err := ai.UnmarshalMessage([]byte(`{"role":"assistant","content":[{"type":"toolCall","id":"1","name":"n","arguments":{"z":1,"a":2}}],"api":"x","provider":"x","model":"x","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"toolUse","timestamp":0}`))
	if err != nil {
		t.Fatal(err)
	}
	call := message.(*ai.AssistantMessage).Content[0].(*ai.ToolCall)
	call.Arguments["z"] = json.Number("3")
	encoded, err := ai.MarshalToolCallArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"a":2,"z":3}` {
		t.Fatalf("mutated arguments = %s", encoded)
	}
}

func TestSetToolCallArgumentsJSONPreservesProviderOrder(t *testing.T) {
	partial := `{"partial":true}`
	streamIndex := 2
	call := &ai.ToolCall{ID: "1", Name: "n", PartialJSON: &partial, StreamIndex: &streamIndex}
	if err := ai.SetToolCallArgumentsJSON(call, []byte(` { "text":"first", "mode":"plain", "escaped":"\u003c" } `)); err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.MarshalToolCallArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"text":"first","mode":"plain","escaped":"<"}` {
		t.Fatalf("arguments = %s", encoded)
	}
	if call.Arguments["text"] != "first" || call.Arguments["mode"] != "plain" {
		t.Fatalf("decoded arguments = %#v", call.Arguments)
	}
}

func TestSetToolCallArgumentsJSONNormalizesNumbersLikeJSONStringify(t *testing.T) {
	call := &ai.ToolCall{}
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"integerExponent":1e2,"small":1e-6,"scientific":1e-7,"large":1e20,"huge":1e21,"negativeZero":-0,"unsafe":9007199254740993,"overflow":1e400}`)); err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.MarshalToolCallArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"integerExponent":100,"small":0.000001,"scientific":1e-7,"large":100000000000000000000,"huge":1e+21,"negativeZero":0,"unsafe":9007199254740992,"overflow":null}`
	if string(encoded) != want {
		t.Fatalf("arguments = %s, want %s", encoded, want)
	}
}

func TestSetToolCallArgumentsJSONMatchesJavaScriptPropertyOrder(t *testing.T) {
	call := &ai.ToolCall{}
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"2":"two","keep":"first","1":"one","keep":"last","01":"not-index","4294967294":"max-index","4294967295":"not-index"}`)); err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.MarshalToolCallArguments(call)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"1":"one","2":"two","4294967294":"max-index","keep":"last","01":"not-index","4294967295":"not-index"}`
	if string(encoded) != want {
		t.Fatalf("arguments = %s, want %s", encoded, want)
	}
}

func TestToolCallUnmarshalPreservesStreamingScratch(t *testing.T) {
	var call ai.ToolCall
	if err := json.Unmarshal([]byte(`{"id":"1","name":"n","arguments":{},"partialJson":"{","partialArgs":"{\"x\"","streamIndex":3}`), &call); err != nil {
		t.Fatal(err)
	}
	if call.PartialJSON == nil || *call.PartialJSON != "{" || call.PartialArgs == nil || *call.PartialArgs != `{"x"` || call.StreamIndex == nil || *call.StreamIndex != 3 {
		t.Fatalf("streaming scratch was not retained: %#v", call)
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

func TestToolCallStreamingScratchIsOptional(t *testing.T) {
	partialArgs := `{"value":`
	streamIndex := 0
	call := &ai.ToolCall{
		ID:          "tool-1",
		Name:        "streaming",
		Arguments:   map[string]any{},
		PartialArgs: &partialArgs,
		StreamIndex: &streamIndex,
	}
	encoded, err := ai.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"toolCall","id":"tool-1","name":"streaming","arguments":{},"partialArgs":"{\"value\":","streamIndex":0}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
	call.PartialArgs = nil
	call.StreamIndex = nil
	encoded, err = ai.Marshal(call)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("partialArgs")) || bytes.Contains(encoded, []byte("streamIndex")) {
		t.Fatalf("terminal call retained scratch fields: %s", encoded)
	}
}
