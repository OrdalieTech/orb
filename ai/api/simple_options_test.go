package api

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestEstimateContextTokensIgnoresStaleUsage(t *testing.T) {
	system := "system"
	requestContext := ai.Context{
		SystemPrompt: &system,
		Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("summary"), Timestamp: 200},
			estimateAssistant(100, 9_500),
			&ai.UserMessage{Content: ai.NewUserText(strings.Repeat("x", 4_000)), Timestamp: 300},
		},
	}
	estimate := estimateContextTokens(requestContext)
	if estimate.tokens != 1_005 || estimate.usageTokens != 0 || estimate.trailingTokens != 1_005 || estimate.lastUsageIndex != -1 {
		t.Fatalf("estimate = %#v", estimate)
	}
	model := &ai.Model{ContextWindow: 10_000, MaxTokens: 8_000}
	options := buildBaseStreamOptions(model, requestContext, nil)
	if options.MaxTokens == nil || *options.MaxTokens != 4_899 {
		t.Fatalf("max tokens = %v, want 4899", options.MaxTokens)
	}
}

func TestEstimateContextTokensUsesNewApplicableUsage(t *testing.T) {
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("summary"), Timestamp: 200},
		estimateAssistant(100, 9_500),
		&ai.UserMessage{Content: ai.NewUserText("new prompt"), Timestamp: 300},
		estimateAssistant(400, 2_000),
		&ai.UserMessage{Content: ai.NewUserText("tail"), Timestamp: 500},
	}}
	estimate := estimateContextTokens(requestContext)
	if estimate.tokens != 2_001 || estimate.usageTokens != 2_000 || estimate.trailingTokens != 1 || estimate.lastUsageIndex != 3 {
		t.Fatalf("estimate = %#v", estimate)
	}
}

func estimateAssistant(timestamp, totalTokens int64) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content:    ai.AssistantContent{&ai.TextContent{Text: "kept"}},
		API:        ai.APIOpenAIResponses,
		Provider:   "openai",
		Model:      "test-model",
		Usage:      ai.Usage{Input: totalTokens, TotalTokens: totalTokens},
		StopReason: ai.StopReasonStop,
		Timestamp:  timestamp,
	}
}
