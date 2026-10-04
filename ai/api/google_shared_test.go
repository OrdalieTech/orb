package api

import (
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

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
