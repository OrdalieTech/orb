package api

import (
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestTransformMessagesDowngradesImagesAndFillsOrphanedCalls(t *testing.T) {
	model := responsesTestModel()
	model.Input = ai.InputModalities{ai.InputText}
	call := &ai.ToolCall{ID: "call-1", Name: "read", Arguments: map[string]any{}}
	messages := ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserContent(
			&ai.ImageContent{Data: "one", MimeType: "image/png"},
			&ai.ImageContent{Data: "two", MimeType: "image/png"},
		), Timestamp: 1},
		&ai.AssistantMessage{
			Content:    ai.AssistantContent{call},
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      zeroUsage(),
			StopReason: ai.StopReasonToolUse,
			Timestamp:  2,
		},
		&ai.UserMessage{Content: ai.NewUserText("continue"), Timestamp: 3},
	}

	got := transformMessages(messages, model, nil)
	if len(got) != 4 {
		t.Fatalf("message count = %d, want 4", len(got))
	}
	first := got[0].(*ai.UserMessage)
	if len(first.Content.Blocks) != 1 {
		t.Fatalf("image placeholders = %d, want one", len(first.Content.Blocks))
	}
	placeholder, ok := first.Content.Blocks[0].(*ai.TextContent)
	if !ok || placeholder.Text != nonVisionUserImagePlaceholder {
		t.Fatalf("placeholder = %#v", first.Content.Blocks[0])
	}
	synthetic, ok := got[2].(*ai.ToolResultMessage)
	if !ok {
		t.Fatalf("message 2 = %T, want synthetic tool result", got[2])
	}
	if synthetic.ToolCallID != "call-1" || synthetic.ToolName != "read" || !synthetic.IsError {
		t.Fatalf("synthetic result = %#v", synthetic)
	}
	text, ok := synthetic.Content[0].(*ai.TextContent)
	if !ok || text.Text != "No result provided" {
		t.Fatalf("synthetic content = %#v", synthetic.Content)
	}
}

func TestTransformMessagesDropsIncompleteAssistantAndCrossModelSecrets(t *testing.T) {
	model := responsesTestModel()
	thought := "secret"
	signature := `{"type":"reasoning","id":"rs_1","summary":[]}`
	messages := ai.MessageList{
		&ai.AssistantMessage{
			Content: ai.AssistantContent{
				&ai.ThinkingContent{Thinking: "explanation", ThinkingSignature: &signature},
				&ai.TextContent{Text: "answer", TextSignature: &signature},
				&ai.ToolCall{ID: "call|foreign", Name: "tool", Arguments: map[string]any{}, ThoughtSignature: &thought},
			},
			API:        ai.APIAnthropicMessages,
			Provider:   "anthropic",
			Model:      "claude",
			Usage:      zeroUsage(),
			StopReason: ai.StopReasonStop,
		},
		&ai.AssistantMessage{
			Content:    ai.AssistantContent{&ai.TextContent{Text: "partial"}},
			API:        model.API,
			Provider:   model.Provider,
			Model:      model.ID,
			Usage:      zeroUsage(),
			StopReason: ai.StopReasonError,
		},
	}

	got := transformMessages(messages, model, func(id string, _ *ai.Model, _ *ai.AssistantMessage) string {
		return "normalized"
	})
	if len(got) != 2 {
		t.Fatalf("message count = %d, want assistant plus synthetic result", len(got))
	}
	assistant := got[0].(*ai.AssistantMessage)
	if _, ok := assistant.Content[0].(*ai.TextContent); !ok {
		t.Fatalf("cross-model thinking was not converted to text: %T", assistant.Content[0])
	}
	text := assistant.Content[1].(*ai.TextContent)
	if text.TextSignature != nil {
		t.Fatal("cross-model text signature was retained")
	}
	tool := assistant.Content[2].(*ai.ToolCall)
	if tool.ID != "normalized" || tool.ThoughtSignature != nil {
		t.Fatalf("cross-model tool = %#v", tool)
	}
}
