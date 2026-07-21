package api

import (
	"strings"

	"github.com/OrdalieTech/pigo/ai"
)

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

type toolCallIDNormalizer func(string, *ai.Model, *ai.AssistantMessage) string

func transformMessages(messages ai.MessageList, model *ai.Model, normalizeToolCallID toolCallIDNormalizer) ai.MessageList {
	toolCallIDs := make(map[string]string)
	transformed := make(ai.MessageList, 0, len(messages))

	for _, message := range messages {
		switch value := message.(type) {
		case *ai.UserMessage:
			transformed = append(transformed, transformUserMessage(value, model))
		case *ai.ToolResultMessage:
			clone := *value
			clone.Content = transformToolResultContent(value.Content, model)
			if normalized, ok := toolCallIDs[value.ToolCallID]; ok && normalized != value.ToolCallID {
				clone.ToolCallID = normalized
			}
			transformed = append(transformed, &clone)
		case *ai.AssistantMessage:
			clone := *value
			clone.Content = make(ai.AssistantContent, 0, len(value.Content))
			isSameModel := value.Provider == model.Provider && value.API == model.API && value.Model == model.ID

			for _, content := range value.Content {
				switch block := content.(type) {
				case *ai.ThinkingContent:
					if block.Redacted != nil && *block.Redacted {
						if isSameModel {
							copy := *block
							clone.Content = append(clone.Content, &copy)
						}
						continue
					}
					if isSameModel && block.ThinkingSignature != nil {
						copy := *block
						clone.Content = append(clone.Content, &copy)
						continue
					}
					if strings.TrimSpace(block.Thinking) == "" {
						continue
					}
					if isSameModel {
						copy := *block
						clone.Content = append(clone.Content, &copy)
					} else {
						clone.Content = append(clone.Content, &ai.TextContent{Text: block.Thinking})
					}
				case *ai.TextContent:
					copy := *block
					if !isSameModel {
						copy.TextSignature = nil
					}
					clone.Content = append(clone.Content, &copy)
				case *ai.ToolCall:
					copy := *block
					if !isSameModel {
						copy.ThoughtSignature = nil
						if normalizeToolCallID != nil {
							normalized := normalizeToolCallID(block.ID, model, value)
							if normalized != block.ID {
								toolCallIDs[block.ID] = normalized
								copy.ID = normalized
							}
						}
					}
					clone.Content = append(clone.Content, &copy)
				}
			}
			transformed = append(transformed, &clone)
		}
	}

	result := make(ai.MessageList, 0, len(transformed))
	pendingToolCalls := make([]*ai.ToolCall, 0)
	existingToolResults := make(map[string]struct{})
	insertSyntheticToolResults := func() {
		for _, call := range pendingToolCalls {
			if _, ok := existingToolResults[call.ID]; ok {
				continue
			}
			result = append(result, &ai.ToolResultMessage{
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content: ai.ToolResultContent{
					&ai.TextContent{Text: "No result provided"},
				},
				IsError:   true,
				Timestamp: openAINowUnixMilli(),
			})
		}
		pendingToolCalls = pendingToolCalls[:0]
		existingToolResults = make(map[string]struct{})
	}

	for _, message := range transformed {
		switch value := message.(type) {
		case *ai.AssistantMessage:
			insertSyntheticToolResults()
			if value.StopReason == ai.StopReasonError || value.StopReason == ai.StopReasonAborted {
				continue
			}
			pendingToolCalls = pendingToolCalls[:0]
			for _, content := range value.Content {
				if call, ok := content.(*ai.ToolCall); ok {
					pendingToolCalls = append(pendingToolCalls, call)
				}
			}
			existingToolResults = make(map[string]struct{})
			result = append(result, value)
		case *ai.ToolResultMessage:
			existingToolResults[value.ToolCallID] = struct{}{}
			result = append(result, value)
		case *ai.UserMessage:
			insertSyntheticToolResults()
			result = append(result, value)
		}
	}
	insertSyntheticToolResults()
	return result
}

func transformUserMessage(message *ai.UserMessage, model *ai.Model) *ai.UserMessage {
	clone := *message
	if message.Content.Text != nil {
		text := *message.Content.Text
		clone.Content = ai.NewUserText(text)
		return &clone
	}
	blocks := message.Content.Blocks
	if blocks == nil {
		blocks = ai.UserContentBlocks{}
	}
	if modelSupportsImage(model) {
		clone.Content = ai.NewUserContent(cloneUserBlocks(blocks)...)
		return &clone
	}
	clone.Content = ai.NewUserContent(replaceUserImagesWithPlaceholder(blocks, nonVisionUserImagePlaceholder)...)
	return &clone
}

func transformToolResultContent(content ai.ToolResultContent, model *ai.Model) ai.ToolResultContent {
	if modelSupportsImage(model) {
		result := make(ai.ToolResultContent, 0, len(content))
		for _, item := range content {
			switch block := item.(type) {
			case *ai.TextContent:
				copy := *block
				result = append(result, &copy)
			case *ai.ImageContent:
				copy := *block
				result = append(result, &copy)
			}
		}
		return result
	}

	result := make(ai.ToolResultContent, 0, len(content))
	previousWasPlaceholder := false
	for _, item := range content {
		switch block := item.(type) {
		case *ai.ImageContent:
			if !previousWasPlaceholder {
				result = append(result, &ai.TextContent{Text: nonVisionToolImagePlaceholder})
			}
			previousWasPlaceholder = true
		case *ai.TextContent:
			copy := *block
			result = append(result, &copy)
			previousWasPlaceholder = block.Text == nonVisionToolImagePlaceholder
		}
	}
	return result
}

func cloneUserBlocks(blocks ai.UserContentBlocks) []ai.UserContentBlock {
	result := make([]ai.UserContentBlock, 0, len(blocks))
	for _, item := range blocks {
		switch block := item.(type) {
		case *ai.TextContent:
			copy := *block
			result = append(result, &copy)
		case *ai.ImageContent:
			copy := *block
			result = append(result, &copy)
		}
	}
	return result
}

func replaceUserImagesWithPlaceholder(blocks ai.UserContentBlocks, placeholder string) []ai.UserContentBlock {
	result := make([]ai.UserContentBlock, 0, len(blocks))
	previousWasPlaceholder := false
	for _, item := range blocks {
		switch block := item.(type) {
		case *ai.ImageContent:
			if !previousWasPlaceholder {
				result = append(result, &ai.TextContent{Text: placeholder})
			}
			previousWasPlaceholder = true
		case *ai.TextContent:
			copy := *block
			result = append(result, &copy)
			previousWasPlaceholder = block.Text == placeholder
		}
	}
	return result
}
