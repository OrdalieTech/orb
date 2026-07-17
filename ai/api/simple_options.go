package api

import (
	"math"
	"unicode/utf16"

	"github.com/OrdalieTech/pi-go/ai"
)

const (
	contextSafetyTokens int64 = 4096
	charsPerToken       int64 = 4
	estimatedImageChars int64 = 4800
	minimumMaxTokens    int64 = 1
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

func clampMaxTokensToContext(model *ai.Model, requestContext ai.Context, maxTokens int64) int64 {
	if model.ContextWindow <= 0 {
		return max(minimumMaxTokens, maxTokens)
	}
	available := model.ContextWindow - estimateContextTokens(requestContext).tokens - contextSafetyTokens
	return min(maxTokens, max(minimumMaxTokens, available))
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
	return (chars + charsPerToken - 1) / charsPerToken
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

func estimateTextTokens(text string) int64 {
	length := jsStringLength(text)
	return (length + charsPerToken - 1) / charsPerToken
}

func jsStringLength(value string) int64 {
	return int64(len(utf16.Encode([]rune(value))))
}

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

var extendedThinkingLevels = []ai.ModelThinkingLevel{
	ai.ModelThinkingOff,
	ai.ModelThinkingMinimal,
	ai.ModelThinkingLow,
	ai.ModelThinkingMedium,
	ai.ModelThinkingHigh,
	ai.ModelThinkingXHigh,
	ai.ModelThinkingMax,
}

func clampSimpleReasoning(model *ai.Model, requested *ai.ThinkingLevel) *ai.ThinkingLevel {
	if requested == nil {
		return nil
	}
	available := supportedThinkingLevels(model)
	wanted := ai.ModelThinkingLevel(*requested)
	for _, level := range available {
		if level == wanted {
			return simpleReasoningLevel(level)
		}
	}
	requestedIndex := -1
	for index, level := range extendedThinkingLevels {
		if level == wanted {
			requestedIndex = index
			break
		}
	}
	if requestedIndex < 0 {
		if len(available) == 0 {
			return nil
		}
		return simpleReasoningLevel(available[0])
	}
	for index := requestedIndex; index < len(extendedThinkingLevels); index++ {
		if containsThinkingLevel(available, extendedThinkingLevels[index]) {
			return simpleReasoningLevel(extendedThinkingLevels[index])
		}
	}
	for index := requestedIndex - 1; index >= 0; index-- {
		if containsThinkingLevel(available, extendedThinkingLevels[index]) {
			return simpleReasoningLevel(extendedThinkingLevels[index])
		}
	}
	return nil
}

func supportedThinkingLevels(model *ai.Model) []ai.ModelThinkingLevel {
	if model == nil || !model.Reasoning {
		return []ai.ModelThinkingLevel{ai.ModelThinkingOff}
	}
	var mappings map[ai.ModelThinkingLevel]*string
	if model.ThinkingLevelMap != nil {
		mappings = *model.ThinkingLevelMap
	}
	available := make([]ai.ModelThinkingLevel, 0, len(extendedThinkingLevels))
	for _, level := range extendedThinkingLevels {
		mapped, present := mappings[level]
		if present && mapped == nil {
			continue
		}
		if (level == ai.ModelThinkingXHigh || level == ai.ModelThinkingMax) && !present {
			continue
		}
		available = append(available, level)
	}
	return available
}

func containsThinkingLevel(levels []ai.ModelThinkingLevel, wanted ai.ModelThinkingLevel) bool {
	for _, level := range levels {
		if level == wanted {
			return true
		}
	}
	return false
}

func simpleReasoningLevel(level ai.ModelThinkingLevel) *ai.ThinkingLevel {
	if level == ai.ModelThinkingOff {
		return nil
	}
	value := ai.ThinkingLevel(level)
	return &value
}
