package api

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

func TestOpenAIResponsesProjectsTranscriptSystemAndToolAdditions(t *testing.T) {
	model := responsesTestModel()
	model.Compat = json.RawMessage(`{"supportsMidConvoSystemMessages":true,"supportsAdditionalTools":true}`)
	context := transcriptProjectionContext()
	payload, _, err := buildOpenAIResponsesPayload(model, context, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	if len(payload.Tools) != 1 || payload.Tools[0].Name != "echo" {
		t.Fatalf("request tools = %#v, want only initial echo", payload.Tools)
	}
	if !strings.Contains(wire, `"type":"additional_tools","role":"developer"`) ||
		!strings.Contains(wire, `"name":"late"`) || !strings.Contains(wire, `"content":"Use late tools."`) {
		t.Fatalf("transcript additions missing from payload: %s", wire)
	}
}

func TestAnthropicProjectsNativeTranscriptToolChanges(t *testing.T) {
	model := anthropicTestModel()
	model.Compat = json.RawMessage(`{"supportsMidConvoSystemMessages":true,"supportsMidConvoToolChanges":true}`)
	payload, _, err := buildAnthropicMessagesPayload(model, transcriptProjectionContext(), nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := ai.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	wire := string(encoded)
	if len(payload.Tools) != 3 || payload.Tools[0].Name != "echo" || payload.Tools[1].Name != "__pi_deferred_placeholder__" || payload.Tools[2].Name != "late" {
		t.Fatalf("native Anthropic tools = %#v", payload.Tools)
	}
	if !strings.Contains(wire, `mid-conversation-tool-changes-2026-07-01`) ||
		!strings.Contains(wire, `"type":"tool_addition"`) || !strings.Contains(wire, `"name":"late"`) ||
		!strings.Contains(wire, `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("native Anthropic tool change missing: %s", wire)
	}
}

func transcriptProjectionContext() ai.Context {
	base := ai.Tool{Name: "echo", Description: "Echo", Parameters: jsonschema.Schema(`{"type":"object"}`)}
	late := ai.Tool{Name: "late", Description: "Late", Parameters: jsonschema.Schema(`{"type":"object"}`)}
	return ai.Context{Messages: ai.MessageList{
		&ai.SystemMessage{Content: "Base prompt.", ToolsAdded: []ai.Tool{base}},
		&ai.UserMessage{Content: ai.NewUserText("hello")},
		&ai.SystemMessage{Content: "Use late tools.", ToolsAdded: []ai.Tool{late}},
	}}
}
