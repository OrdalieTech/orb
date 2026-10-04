package api

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func bedrockTestModel(id, name string) *ai.Model {
	return &ai.Model{
		ID: id, Name: name, API: ai.APIBedrockConverse, Provider: "amazon-bedrock",
		BaseURL: "https://bedrock-runtime.us-east-1.amazonaws.com", Reasoning: true,
		Input:         ai.InputModalities{ai.InputText, ai.InputImage},
		Cost:          ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}},
		ContextWindow: 200_000, MaxTokens: 64_000,
	}
}

// TestBedrockTypeMismatchedDeltaDropped_OTm5 pins upstream
// handleContentBlockDelta (bedrock-converse-stream.ts:471-518): a delta whose
// block index resolves to a block of a different type is dropped; a new block
// is only created when NO block exists at that stream index. (OT-m5)
func TestBedrockTypeMismatchedDeltaDropped_OTm5(t *testing.T) {
	mismatchedText := "dropped"
	mismatchedReasoning := "also dropped"
	freshText := "kept"
	backend := &BedrockBackend{NewTransport: func(context.Context, BedrockTransportConfig) (BedrockTransport, error) {
		return bedrockTransportFunc(func(context.Context, *BedrockConverseStreamPayload) (BedrockResponse, error) {
			return &fixtureBedrockResponse{items: []BedrockStreamItem{
				{Kind: BedrockItemMessageStart, Role: "assistant"},
				{Kind: BedrockItemContentStart, ContentBlockIndex: 1, ToolUseID: "tool-1", ToolName: "echo"},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 1, Text: &mismatchedText},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 1, ReasoningText: &mismatchedReasoning},
				{Kind: BedrockItemContentDelta, ContentBlockIndex: 2, Text: &freshText},
				{Kind: BedrockItemContentStop, ContentBlockIndex: 1},
				{Kind: BedrockItemContentStop, ContentBlockIndex: 2},
				{Kind: BedrockItemMessageStop, StopReason: "tool_use"},
			}}, nil
		}), nil
	}}
	stream, err := StreamBedrockConverseWithOptions(context.Background(), bedrockTestModel("anthropic.claude-sonnet-4-5", "Claude"), ai.Context{
		Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hello")}},
	}, &BedrockConverseStreamOptions{Backend: backend})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(message.Content) != 2 {
		t.Fatalf("content = %#v, want tool call plus fresh text only", message.Content)
	}
	call, ok := message.Content[0].(*ai.ToolCall)
	if !ok || call.Name != "echo" {
		t.Fatalf("first block = %#v, want the tool call", message.Content[0])
	}
	text, ok := message.Content[1].(*ai.TextContent)
	if !ok || text.Text != freshText {
		t.Fatalf("second block = %#v, want fresh text %q", message.Content[1], freshText)
	}
}

type bedrockTransportFunc func(context.Context, *BedrockConverseStreamPayload) (BedrockResponse, error)

func (function bedrockTransportFunc) Send(ctx context.Context, payload *BedrockConverseStreamPayload) (BedrockResponse, error) {
	return function(ctx, payload)
}
