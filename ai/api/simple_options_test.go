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
	if estimate.tokens != 1_149 || estimate.usageTokens != 0 || estimate.trailingTokens != 1_149 || estimate.lastUsageIndex != -1 {
		t.Fatalf("estimate = %#v", estimate)
	}
	model := &ai.Model{ContextWindow: 10_000, MaxTokens: 8_000}
	options := buildBaseStreamOptions(model, requestContext, nil)
	if options.MaxTokens == nil || *options.MaxTokens != 4_755 {
		t.Fatalf("max tokens = %v, want 4755", options.MaxTokens)
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
	if estimate.tokens != 2_002 || estimate.usageTokens != 2_000 || estimate.trailingTokens != 2 || estimate.lastUsageIndex != 3 {
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

// pi 1.5.1 (#10497): 15% of the estimated input stays unused while that leaves 32,768 output
// tokens; past that only the fixed 4,096 margin does, with output capped at 32,768.
func TestClampMaxTokensLeavesRoomForAnInputOvercount(t *testing.T) {
	const window = 1_048_576
	for _, test := range []struct {
		window, maxTokens float64
		input             int64
		want              float64
	}{
		{window, 943_718, 220_815, window - 220_815 - 33_123},
		{window, window, 10_000, window - 10_000 - 4096},
		{window, 64_000, 100_000, 64_000},
		{window, 943_718, 912_000, 32_768},
		{window, 943_718, 1_030_000, window - 1_030_000 - 4096},
		{32_768, 32_768, 20_000, 32_768 - 20_000 - 4096},
		{window, 943_718, 1_050_000, 1},
	} {
		requestContext := ai.Context{Messages: ai.MessageList{&ai.UserMessage{Content: ai.NewUserText("hi"), Timestamp: 1}, estimateAssistant(2, test.input)}}
		if got := clampMaxTokensToContext(&ai.Model{ContextWindow: test.window}, requestContext, test.maxTokens); got != test.want {
			t.Errorf("window %v, input %d: max tokens = %v, want %v", test.window, test.input, got, test.want)
		}
	}
}
