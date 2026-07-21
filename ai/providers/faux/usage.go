package faux

import (
	"fmt"

	"github.com/OrdalieTech/pigo/ai"
)

func estimateTokens(text string) int64 {
	return int64((len(utf16Units(text)) + 3) / 4)
}

func contentToText(content ai.UserContent) string {
	if content.Text != nil {
		return *content.Text
	}
	parts := make([]string, 0, len(content.Blocks))
	for _, rawBlock := range content.Blocks {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			parts = append(parts, block.Text)
		case *ai.ImageContent:
			parts = append(parts, fmt.Sprintf("[image:%s:%d]", block.MimeType, len(utf16Units(block.Data))))
		}
	}
	return joinLines(parts)
}

func assistantContentToText(content ai.AssistantContent) (string, error) {
	parts := make([]string, 0, len(content))
	for _, rawBlock := range content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			parts = append(parts, block.Text)
		case *ai.ThinkingContent:
			parts = append(parts, block.Thinking)
		case *ai.ToolCall:
			arguments, err := ai.MarshalToolCallArguments(block)
			if err != nil {
				return "", err
			}
			parts = append(parts, block.Name+":"+string(arguments))
		}
	}
	return joinLines(parts), nil
}

func toolResultToText(message *ai.ToolResultMessage) string {
	parts := make([]string, 0, len(message.Content)+1)
	parts = append(parts, message.ToolName)
	for _, rawBlock := range message.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			parts = append(parts, block.Text)
		case *ai.ImageContent:
			parts = append(parts, fmt.Sprintf("[image:%s:%d]", block.MimeType, len(utf16Units(block.Data))))
		}
	}
	return joinLines(parts)
}

func messageToText(message ai.Message) (string, error) {
	switch typed := message.(type) {
	case *ai.UserMessage:
		return contentToText(typed.Content), nil
	case *ai.AssistantMessage:
		return assistantContentToText(typed.Content)
	case *ai.ToolResultMessage:
		return toolResultToText(typed), nil
	default:
		return "", fmt.Errorf("faux: unsupported message %T", message)
	}
}

func serializeContext(requestContext ai.Context) (string, error) {
	parts := make([]string, 0, len(requestContext.Messages)+2)
	if requestContext.SystemPrompt != nil && *requestContext.SystemPrompt != "" {
		parts = append(parts, "system:"+*requestContext.SystemPrompt)
	}
	for _, message := range requestContext.Messages {
		text, err := messageToText(message)
		if err != nil {
			return "", err
		}
		role := ""
		switch message.(type) {
		case *ai.UserMessage:
			role = "user"
		case *ai.AssistantMessage:
			role = "assistant"
		case *ai.ToolResultMessage:
			role = "toolResult"
		}
		parts = append(parts, role+":"+text)
	}
	if requestContext.Tools != nil && len(*requestContext.Tools) > 0 {
		tools, err := ai.Marshal(*requestContext.Tools)
		if err != nil {
			return "", err
		}
		parts = append(parts, "tools:"+string(tools))
	}
	return joinContextParts(parts), nil
}

func (provider *Provider) withUsageEstimate(
	message *ai.AssistantMessage,
	requestContext ai.Context,
	options *ai.StreamOptions,
) error {
	promptText, err := serializeContext(requestContext)
	if err != nil {
		return err
	}
	outputText, err := assistantContentToText(message.Content)
	if err != nil {
		return err
	}
	promptUnits := utf16Units(promptText)
	promptTokens := int64((len(promptUnits) + 3) / 4)
	outputTokens := estimateTokens(outputText)
	input := promptTokens
	var cacheRead, cacheWrite int64

	if options != nil && options.SessionID != nil && *options.SessionID != "" &&
		(options.CacheRetention == nil || *options.CacheRetention != ai.CacheRetentionNone) {
		sessionID := *options.SessionID
		provider.mu.Lock()
		previousPrompt, exists := provider.promptCache[sessionID]
		if exists {
			previousUnits := utf16Units(previousPrompt)
			cachedUnits := commonPrefixLength(previousUnits, promptUnits)
			cacheRead = int64((cachedUnits + 3) / 4)
			remainingUnits := len(promptUnits) - cachedUnits
			cacheWrite = int64((remainingUnits + 3) / 4)
			input = max(int64(0), promptTokens-cacheRead)
		} else {
			cacheWrite = promptTokens
		}
		provider.promptCache[sessionID] = promptText
		provider.mu.Unlock()
	}

	message.Usage = ai.Usage{
		Input:       input,
		Output:      outputTokens,
		CacheRead:   cacheRead,
		CacheWrite:  cacheWrite,
		TotalTokens: input + outputTokens + cacheRead + cacheWrite,
		Cost:        ai.Cost{},
	}
	return nil
}

func commonPrefixLength(left, right []uint16) int {
	length := min(len(left), len(right))
	index := 0
	for index < length && left[index] == right[index] {
		index++
	}
	return index
}

func joinLines(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, part := range parts[1:] {
		result += "\n" + part
	}
	return result
}
