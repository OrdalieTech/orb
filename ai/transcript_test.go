package ai_test

import (
	"reflect"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func text(value string) *string { return &value }

func TestTranscriptReplayAndLegacyNormalization(t *testing.T) {
	read := ai.Tool{Name: "read", Description: "read", Parameters: ai.JSONSchema(`{"type":"object"}`)}
	bash := ai.Tool{Name: "bash", Description: "bash", Parameters: ai.JSONSchema(`{"type":"object"}`)}
	messages := ai.MessageList{
		&ai.SystemMessage{Content: "base", Sections: ai.SystemPromptSections{{Name: "rules", Text: text("rules-v1")}}, ToolsAdded: []ai.Tool{read}, Timestamp: 1},
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 2},
		&ai.SystemMessage{Content: "extra", Sections: ai.SystemPromptSections{{Name: "rules", Text: text("rules-v2")}}, ToolsAdded: []ai.Tool{bash}, ToolsRemoved: []ai.ToolReference{{Name: "read"}}, Timestamp: 3},
	}
	if got, want := ai.CurrentSystemPrompt(messages), "base\n\nextra\n\nrules-v2"; got != want {
		t.Fatalf("prompt = %q, want %q", got, want)
	}
	if got := ai.CurrentTools(messages); !reflect.DeepEqual(got, []ai.Tool{bash}) {
		t.Fatalf("tools = %#v", got)
	}
	prompt := "legacy"
	tools := []ai.Tool{read}
	normalized := ai.NormalizeContext(ai.Context{SystemPrompt: &prompt, Messages: ai.MessageList{messages[1]}, Tools: &tools})
	if len(normalized.Messages) != 2 || ai.CurrentSystemPrompt(normalized.Messages) != prompt {
		t.Fatalf("legacy normalization = %#v", normalized.Messages)
	}
	// Legacy fields are always folded into a leading message, matching the
	// upstream compatibility boundary even when messages already contain state.
	normalized = ai.NormalizeContext(ai.Context{SystemPrompt: &prompt, Messages: messages, Tools: &tools})
	if len(normalized.Messages) != len(messages)+1 {
		t.Fatalf("normalized transcript length = %d, want %d", len(normalized.Messages), len(messages)+1)
	}
}
