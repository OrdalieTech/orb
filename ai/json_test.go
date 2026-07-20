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

func TestNormalizeJSONStringifyJSONPreservesSurrogates(t *testing.T) {
	input := []byte(`{ "\ud800":"high-key", "\udc00":"low-key", "\ud83d\ude00":"pair-key", "values":["\ud800","\udc00","\ud83d\ude00"] }`)
	encoded, err := ai.NormalizeJSONStringifyJSON(input)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"\ud800":"high-key","\udc00":"low-key","😀":"pair-key","values":["\ud800","\udc00","😀"]}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("normalized = %s\nwant       = %s", encoded, want)
	}
}

func TestAssistantMessageCanPreserveErrorBeforeTimestampOrder(t *testing.T) {
	errorMessage := "Request was aborted"
	message := &ai.AssistantMessage{
		Content: ai.AssistantContent{}, API: "faux", Provider: "faux", Model: "faux-1",
		Usage: ai.Usage{Cost: ai.Cost{}}, StopReason: ai.StopReasonAborted,
		ErrorMessage: &errorMessage, Timestamp: 123,
	}
	ai.SetAssistantMessageErrorBeforeTimestamp(message, true)
	encoded, err := ai.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"assistant","content":[],"api":"faux","provider":"faux","model":"faux-1","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"aborted","errorMessage":"Request was aborted","timestamp":123}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, want)
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

func TestUnmarshalMessagePreservesLoneSurrogateAndUnknownBlocks(t *testing.T) {
	message, err := ai.UnmarshalMessage([]byte(`{"role":"user","content":[{"type":"text","text":"\ud800"},{ "type" : "future", "value" : 1e2 }],"timestamp":1}`))
	if err != nil {
		t.Fatal(err)
	}
	user := message.(*ai.UserMessage)
	text := user.Content.Blocks[0].(*ai.TextContent)
	if text.Text != "\xed\xa0\x80" {
		t.Fatalf("text bytes = %x", []byte(text.Text))
	}
	unknown, ok := user.Content.Blocks[1].(*ai.UnknownContentBlock)
	if !ok || string(unknown.Raw) != `{"type":"future","value":100}` {
		t.Fatalf("unknown block = %T %s", user.Content.Blocks[1], unknown.Raw)
	}
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"role":"user","content":[{"type":"text","text":"\ud800"},{"type":"future","value":100}],"timestamp":1}`
	if string(encoded) != want {
		t.Fatalf("encoded = %s, want %s", encoded, want)
	}
}

func TestAssistantMessageOrderDetectionIgnoresNestedMemberNames(t *testing.T) {
	input := []byte(`{"role":"assistant","content":[{"type":"toolCall","id":"1","name":"echo","arguments":{"errorMessage":"nested","timestamp":0}}],"api":"x","provider":"x","model":"x","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"error","timestamp":1,"errorMessage":"top-level"}`)
	message, err := ai.UnmarshalMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, input) {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, input)
	}
}

func TestAssistantMessageSecondaryStringsPreserveLoneSurrogates(t *testing.T) {
	input := []byte(`{"role":"assistant","content":[{"type":"text","text":"\ud800","textSignature":"\ud800"},{"type":"thinking","thinking":"\ud800","thinkingSignature":"\ud800","redacted":false},{"type":"toolCall","id":"\ud800","name":"\ud800","arguments":{"value":"\ud800"},"partialJson":"\ud800","partialArgs":"\ud800","streamIndex":0,"thoughtSignature":"\ud800"}],"api":"\ud800","provider":"\ud800","model":"\ud800","usage":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":0,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"\ud800","timestamp":1,"responseId":"\ud800","responseModel":"\ud800","diagnostics":[{"type":"\ud800","timestamp":2,"error":{"name":"\ud800","message":"\ud800","stack":"\ud800","code":"\ud800"},"details":{"value":"\ud800"}}],"errorMessage":"\ud800"}`)
	message, err := ai.UnmarshalMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	assistant := message.(*ai.AssistantMessage)
	assertWTF8String(t, "api", string(assistant.API))
	assertWTF8String(t, "provider", string(assistant.Provider))
	assertWTF8String(t, "model", assistant.Model)
	assertWTF8String(t, "stop reason", string(assistant.StopReason))
	assertWTF8StringPointer(t, "response ID", assistant.ResponseID)
	assertWTF8StringPointer(t, "response model", assistant.ResponseModel)
	assertWTF8StringPointer(t, "error message", assistant.ErrorMessage)

	text := assistant.Content[0].(*ai.TextContent)
	assertWTF8StringPointer(t, "text signature", text.TextSignature)
	thinking := assistant.Content[1].(*ai.ThinkingContent)
	assertWTF8StringPointer(t, "thinking signature", thinking.ThinkingSignature)
	call := assistant.Content[2].(*ai.ToolCall)
	assertWTF8String(t, "tool-call ID", call.ID)
	assertWTF8String(t, "tool-call name", call.Name)
	assertWTF8StringPointer(t, "tool-call thought signature", call.ThoughtSignature)
	assertWTF8StringPointer(t, "tool-call partial JSON", call.PartialJSON)
	assertWTF8StringPointer(t, "tool-call partial arguments", call.PartialArgs)

	diagnostic := (*assistant.Diagnostics)[0]
	assertWTF8String(t, "diagnostic type", diagnostic.Type)
	assertWTF8StringPointer(t, "diagnostic error name", diagnostic.Error.Name)
	assertWTF8String(t, "diagnostic error message", diagnostic.Error.Message)
	assertWTF8StringPointer(t, "diagnostic error stack", diagnostic.Error.Stack)

	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, input) {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, input)
	}
}

func TestToolResultSecondaryStringsPreserveLoneSurrogates(t *testing.T) {
	input := []byte(`{"role":"toolResult","toolCallId":"\ud800","toolName":"\ud800","content":[{"type":"text","text":"\ud800","textSignature":"\ud800"},{"type":"image","data":"\ud800","mimeType":"\ud800"}],"details":{"value":"\ud800"},"addedToolNames":["\ud800",""],"isError":false,"timestamp":1}`)
	message, err := ai.UnmarshalMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	result := message.(*ai.ToolResultMessage)
	assertWTF8String(t, "tool-result call ID", result.ToolCallID)
	assertWTF8String(t, "tool-result name", result.ToolName)
	assertWTF8StringPointer(t, "tool-result text signature", result.Content[0].(*ai.TextContent).TextSignature)
	assertWTF8String(t, "image data", result.Content[1].(*ai.ImageContent).Data)
	assertWTF8String(t, "image MIME type", result.Content[1].(*ai.ImageContent).MimeType)
	if result.AddedToolNames == nil || len(*result.AddedToolNames) != 2 {
		t.Fatalf("added tool names = %#v", result.AddedToolNames)
	}
	assertWTF8String(t, "added tool name", (*result.AddedToolNames)[0])

	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, input) {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, input)
	}
}

func TestToolResultUsageRoundTripsInUpstreamOrder(t *testing.T) {
	input := []byte(`{"role":"toolResult","toolCallId":"call","toolName":"nested","content":[],"details":{},"usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":10,"cost":{"input":0.1,"output":0.2,"cacheRead":0.3,"cacheWrite":0.4,"total":1}},"isError":false,"timestamp":1}`)
	message, err := ai.UnmarshalMessage(input)
	if err != nil {
		t.Fatal(err)
	}
	result := message.(*ai.ToolResultMessage)
	if result.Usage == nil || result.Usage.TotalTokens != 10 {
		t.Fatalf("usage = %#v", result.Usage)
	}
	encoded, err := ai.MarshalMessage(message)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, input) {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, input)
	}
}

func TestUsagePreservesOptionalMemberPlacement(t *testing.T) {
	for _, input := range [][]byte{
		[]byte(`{"role":"toolResult","toolCallId":"call","toolName":"nested","content":[],"usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"cacheWrite1h":5,"reasoning":6,"totalTokens":21,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"isError":false,"timestamp":1}`),
		[]byte(`{"role":"toolResult","toolCallId":"call","toolName":"nested","content":[],"usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":21,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0},"cacheWrite1h":5,"reasoning":6},"isError":false,"timestamp":1}`),
	} {
		message, err := ai.UnmarshalMessage(input)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := ai.MarshalMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(encoded, input) {
			t.Fatalf("encoded = %s\nwant    = %s", encoded, input)
		}
	}
}

func TestConstructedUsageKeepsProviderOptionalMemberOrder(t *testing.T) {
	cacheWrite1h, reasoning := int64(5), int64(6)
	encoded, err := ai.Marshal(ai.Usage{
		Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, CacheWrite1h: &cacheWrite1h, Reasoning: &reasoning, TotalTokens: 21, Cost: ai.Cost{},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":21,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0},"cacheWrite1h":5,"reasoning":6}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("encoded = %s\nwant    = %s", encoded, want)
	}

	encoded, err = ai.Marshal(ai.Usage{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Reasoning: &reasoning, TotalTokens: 15, Cost: ai.Cost{}})
	if err != nil {
		t.Fatal(err)
	}
	want = []byte(`{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"reasoning":6,"totalTokens":15,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("reasoning-only encoded = %s\nwant                   = %s", encoded, want)
	}
}

func assertWTF8String(t *testing.T, name, got string) {
	t.Helper()
	const want = "\xed\xa0\x80"
	if got != want {
		t.Fatalf("%s bytes = %x, want %x", name, []byte(got), []byte(want))
	}
}

func assertWTF8StringPointer(t *testing.T, name string, got *string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s is nil", name)
	}
	assertWTF8String(t, name, *got)
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
