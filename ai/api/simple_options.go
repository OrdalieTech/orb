package api

import (
	"math"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
)

const (
	contextSafetyTokens float64 = 4096
	// OpenRouter counts the input with its own tokenizer, up to 12% above the provider's count, and
	// rejects input + max_tokens above the window before routing (#10497).
	contextSafetyRatio = 0.15
	// The ratio margin is kept only while it leaves this much output; otherwise only the fixed margin
	// is, and output is capped at this so an overcount still fits for a while before compaction.
	minOutputWithRatioMargin float64 = 32768
	estimatedImageChars      int64   = 4800
	minimumMaxTokens         float64 = 1
)

type contextUsageEstimate struct {
	tokens         int64
	usageTokens    int64
	trailingTokens int64
	lastUsageIndex int
}

func buildBaseStreamOptions(model *ai.Model, requestContext ai.Context, options *ai.SimpleStreamOptions) ai.StreamOptions {
	var base ai.StreamOptions
	if options != nil {
		base = options.StreamOptions
	}
	requested := model.MaxTokens
	if options != nil && options.MaxTokens != nil {
		requested = *options.MaxTokens
	}
	clamped := clampMaxTokensToContext(model, requestContext, requested)
	base.MaxTokens = &clamped
	return base
}

func simpleToolChoice(options *ai.SimpleStreamOptions, required string) string {
	if options == nil {
		return ""
	}
	if options.ToolChoice == ai.ToolChoiceRequired {
		return required
	}
	return string(options.ToolChoice)
}

func simpleToolChoiceAny(options *ai.SimpleStreamOptions, required string) any {
	if choice := simpleToolChoice(options, required); choice != "" {
		return choice
	}
	return nil
}

func clampMaxTokensToContext(model *ai.Model, requestContext ai.Context, maxTokens float64) float64 {
	if model.ContextWindow <= 0 {
		return max(minimumMaxTokens, maxTokens)
	}
	input := float64(estimateContextTokens(requestContext).tokens)
	room := model.ContextWindow - input - max(contextSafetyTokens, math.Ceil(input*contextSafetyRatio))
	if room < minOutputWithRatioMargin {
		room = min(minOutputWithRatioMargin, model.ContextWindow-input-contextSafetyTokens)
	}
	return min(maxTokens, max(minimumMaxTokens, room))
}

func estimateContextTokens(requestContext ai.Context) contextUsageEstimate {
	estimate := estimateMessages(requestContext.Messages)
	if estimate.lastUsageIndex >= 0 {
		addedNames := make(map[string]struct{})
		for _, message := range requestContext.Messages[estimate.lastUsageIndex+1:] {
			result, ok := message.(*ai.ToolResultMessage)
			if !ok || result.AddedToolNames == nil {
				continue
			}
			for _, name := range *result.AddedToolNames {
				addedNames[name] = struct{}{}
			}
		}
		addedTools := make([]ai.Tool, 0, len(addedNames))
		if requestContext.Tools != nil {
			for _, tool := range *requestContext.Tools {
				if _, ok := addedNames[tool.Name]; ok {
					addedTools = append(addedTools, tool)
				}
			}
		}
		addedTokens := estimateToolsTokens(addedTools)
		estimate.tokens += addedTokens
		estimate.trailingTokens += addedTokens
		return estimate
	}

	prefixTokens := int64(0)
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		prefixTokens += estimateTextTokens(*requestContext.SystemPrompt)
	}
	if requestContext.Tools != nil {
		prefixTokens += estimateToolsTokens(*requestContext.Tools)
	}
	estimate.tokens += prefixTokens
	estimate.trailingTokens += prefixTokens
	return estimate
}

func estimateMessages(messages ai.MessageList) contextUsageEstimate {
	lastUsageIndex := -1
	latestPrefixTimestamp := int64(math.MinInt64)
	var usage ai.Usage
	for index, message := range messages {
		if assistant, ok := message.(*ai.AssistantMessage); ok {
			usageAppliesToPrefix := assistant.Timestamp >= latestPrefixTimestamp
			if usageAppliesToPrefix && assistant.StopReason != ai.StopReasonAborted && assistant.StopReason != ai.StopReasonError && calculateContextTokens(assistant.Usage) > 0 {
				usage = assistant.Usage
				lastUsageIndex = index
			}
		}
		latestPrefixTimestamp = max(latestPrefixTimestamp, messageTimestamp(message))
	}

	if lastUsageIndex >= 0 {
		usageTokens := calculateContextTokens(usage)
		trailingTokens := int64(0)
		for _, message := range messages[lastUsageIndex+1:] {
			trailingTokens += estimateMessageTokens(message)
		}
		return contextUsageEstimate{
			tokens:         usageTokens + trailingTokens,
			usageTokens:    usageTokens,
			trailingTokens: trailingTokens,
			lastUsageIndex: lastUsageIndex,
		}
	}

	tokens := int64(0)
	for _, message := range messages {
		tokens += estimateMessageTokens(message)
	}
	return contextUsageEstimate{tokens: tokens, trailingTokens: tokens, lastUsageIndex: -1}
}

func calculateContextTokens(usage ai.Usage) int64 {
	if usage.TotalTokens != 0 {
		return usage.TotalTokens
	}
	return usage.Input + usage.Output + usage.CacheRead + usage.CacheWrite
}

func estimateMessageTokens(message ai.Message) int64 {
	chars := int64(0)
	switch message := message.(type) {
	case *ai.UserMessage:
		if message.Content.Text != nil {
			chars = jsStringLength(*message.Content.Text)
		} else {
			for _, item := range message.Content.Blocks {
				switch block := item.(type) {
				case *ai.TextContent:
					chars += jsStringLength(block.Text)
				case *ai.ImageContent:
					chars += estimatedImageChars
				}
			}
		}
	case *ai.ToolResultMessage:
		for _, item := range message.Content {
			switch block := item.(type) {
			case *ai.TextContent:
				chars += jsStringLength(block.Text)
			case *ai.ImageContent:
				chars += estimatedImageChars
			}
		}
	case *ai.AssistantMessage:
		for _, item := range message.Content {
			switch block := item.(type) {
			case *ai.TextContent:
				chars += jsStringLength(block.Text)
			case *ai.ThinkingContent:
				chars += jsStringLength(block.Thinking)
			case *ai.ToolCall:
				chars += jsStringLength(block.Name)
				arguments, err := ai.MarshalToolCallArguments(block)
				if err != nil {
					chars += jsStringLength("[unserializable]")
				} else {
					chars += jsStringLength(string(arguments))
				}
			}
		}
	}
	return ceilTokens(chars)
}

func estimateToolsTokens(tools []ai.Tool) int64 {
	if len(tools) == 0 {
		return 0
	}
	encoded, err := ai.Marshal(tools)
	if err != nil {
		return estimateTextTokens("[unserializable]")
	}
	return estimateTextTokens(string(encoded))
}

func estimateTextTokens(text string) int64 { return ceilTokens(jsStringLength(text)) }

// ceilTokens is pi's ceil(chars / 3.5) (4 before pi 1.1.0).
func ceilTokens(chars int64) int64 { return (2*chars + 6) / 7 }

func jsStringLength(value string) int64 { return int64(jsonwire.UTF16Len(value)) }

func messageTimestamp(message ai.Message) int64 {
	switch message := message.(type) {
	case *ai.UserMessage:
		return message.Timestamp
	case *ai.AssistantMessage:
		return message.Timestamp
	case *ai.ToolResultMessage:
		return message.Timestamp
	default:
		return math.MinInt64
	}
}

func clampSimpleReasoning(model *ai.Model, requested *ai.ThinkingLevel) *ai.ThinkingLevel {
	if requested == nil {
		return nil
	}
	return simpleReasoningLevel(ai.ClampThinkingLevel(model, ai.ModelThinkingLevel(*requested)))
}

func simpleReasoningLevel(level ai.ModelThinkingLevel) *ai.ThinkingLevel {
	if level == ai.ModelThinkingOff {
		return nil
	}
	value := ai.ThinkingLevel(level)
	return &value
}

func adjustMaxTokensForThinking(
	baseMaxTokens float64,
	modelMaxTokens float64,
	reasoning ai.ThinkingLevel,
	custom *ai.ThinkingBudgets,
) (maxTokens float64, thinkingBudget float64) {
	budgets := map[ai.ThinkingLevel]float64{
		ai.ThinkingMinimal: 1024,
		ai.ThinkingLow:     2048,
		ai.ThinkingMedium:  8192,
		ai.ThinkingHigh:    16384,
	}
	if custom != nil {
		if custom.Minimal != nil {
			budgets[ai.ThinkingMinimal] = float64(*custom.Minimal)
		}
		if custom.Low != nil {
			budgets[ai.ThinkingLow] = float64(*custom.Low)
		}
		if custom.Medium != nil {
			budgets[ai.ThinkingMedium] = float64(*custom.Medium)
		}
		if custom.High != nil {
			budgets[ai.ThinkingHigh] = float64(*custom.High)
		}
	}
	level := reasoning
	if level == ai.ThinkingXHigh || level == ai.ThinkingMax {
		level = ai.ThinkingHigh
	}
	thinkingBudget = budgets[level]
	maxTokens = min(baseMaxTokens+thinkingBudget, modelMaxTokens)
	if maxTokens <= thinkingBudget {
		thinkingBudget = max(float64(0), maxTokens-1024)
	}
	return maxTokens, thinkingBudget
}
