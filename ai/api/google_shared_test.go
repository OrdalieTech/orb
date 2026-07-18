package api

import (
	"bytes"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestConvertGoogleToolsPreservesStringEnumSchema(t *testing.T) {
	tool := ai.Tool{
		Name: "calculate", Description: "calculate",
		Parameters: jsonschema.Schema(`{"type":"object","properties":{"operation":{"type":"string","enum":["add","subtract"]}},"required":["operation"]}`),
	}
	converted := convertGoogleTools([]ai.Tool{tool})
	if len(converted) != 1 || len(converted[0].FunctionDeclarations) != 1 {
		t.Fatalf("converted tools = %#v", converted)
	}
	got := converted[0].FunctionDeclarations[0].ParametersJSONSchema
	if !bytes.Equal(got, tool.Parameters) {
		t.Fatalf("schema changed\nwant: %s\n got: %s", tool.Parameters, got)
	}
}

func TestGoogleThoughtSignatureValidation(t *testing.T) {
	valid := "AAAAAAAAAAAAAAAAAAAAAA=="
	if got := resolveGoogleThoughtSignature(true, &valid); got == nil || *got != valid {
		t.Fatalf("valid signature = %v", got)
	}
	invalid := "not-base64"
	if got := resolveGoogleThoughtSignature(true, &invalid); got != nil {
		t.Fatalf("invalid signature survived: %q", *got)
	}
	nonCanonical := "AB=="
	if got := resolveGoogleThoughtSignature(true, &nonCanonical); got == nil || *got != nonCanonical {
		t.Fatalf("upstream-compatible signature = %v", got)
	}
	if got := resolveGoogleThoughtSignature(false, &valid); got != nil {
		t.Fatalf("cross-model signature survived: %q", *got)
	}
	first := "first"
	if got := retainGoogleThoughtSignature(&first, nil); got == nil || *got != first {
		t.Fatalf("missing delta erased signature: %v", got)
	}
}

func TestGoogleToolResultImageRouting(t *testing.T) {
	result := &ai.ToolResultMessage{
		ToolCallID: "call", ToolName: "read",
		Content: ai.ToolResultContent{&ai.ImageContent{Data: "abc", MimeType: "image/png"}},
	}
	gemini2 := &ai.Model{ID: "gemini-2.5-flash", Input: ai.InputModalities{ai.InputText, ai.InputImage}}
	contents := appendGoogleToolResult(nil, gemini2, result)
	if len(contents) != 2 || contents[0].Parts[0].FunctionResponse == nil || contents[1].Parts[1].InlineData == nil {
		t.Fatalf("Gemini 2 image routing = %#v", contents)
	}
	gemini3 := &ai.Model{ID: "gemini-3-pro-preview", Input: ai.InputModalities{ai.InputText, ai.InputImage}}
	contents = appendGoogleToolResult(nil, gemini3, result)
	response := contents[0].Parts[0].FunctionResponse
	if len(contents) != 1 || response == nil || len(response.Parts) != 1 || response.Parts[0].InlineData == nil {
		t.Fatalf("Gemini 3 image routing = %#v", contents)
	}
}

func TestTransformGoogleMessagesSynthesizesMissingToolResult(t *testing.T) {
	model := &ai.Model{ID: "gemini-2.5-flash", API: ai.APIGoogleGenerativeAI, Provider: "google", Input: ai.InputModalities{ai.InputText}}
	messages := ai.MessageList{
		&ai.AssistantMessage{
			API: model.API, Provider: model.Provider, Model: model.ID, StopReason: ai.StopReasonToolUse,
			Content: ai.AssistantContent{&ai.ToolCall{ID: "call", Name: "read", Arguments: map[string]any{}}},
		},
		&ai.UserMessage{Content: ai.NewUserText("continue")},
	}
	transformed := transformMessages(messages, model, normalizeGoogleToolCallIDForModel)
	if len(transformed) != 3 {
		t.Fatalf("transformed messages = %#v", transformed)
	}
	result, ok := transformed[1].(*ai.ToolResultMessage)
	if !ok || !result.IsError || result.ToolCallID != "call" {
		t.Fatalf("synthetic result = %#v", transformed[1])
	}
}

func TestNormalizeGoogleToolCallID(t *testing.T) {
	value := normalizeGoogleToolCallID("call|with spaces/and?punctuation")
	if value != "call_with_spaces_and_punctuation" {
		t.Fatalf("normalized id = %q", value)
	}
	if len(normalizeGoogleToolCallID(string(bytes.Repeat([]byte("a"), 80)))) != 64 {
		t.Fatal("normalized id was not clamped to 64 bytes")
	}
	if got := normalizeGoogleToolCallID("call🙈id"); got != "call__id" {
		t.Fatalf("astral normalized id = %q", got)
	}
}
