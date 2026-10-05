package api

import (
	"strings"

	"github.com/OrdalieTech/orb/ai"
)

const (
	nonVisionUserImagePlaceholder = "(image omitted: model does not support images)"
	nonVisionToolImagePlaceholder = "(tool image omitted: model does not support images)"
)

type toolCallIDNormalizer func(string, *ai.Model, *ai.AssistantMessage) string

// transformMessages adapts a conversation to model: it downgrades images the
// model cannot see, keeps another model's turns only as text and tool calls,
// drops failed turns and answers orphaned tool calls. A message needing no
// change comes back as it is, so providers recognize it across requests.
func transformMessages(messages ai.MessageList, model *ai.Model, normalizeToolCallID toolCallIDNormalizer) ai.MessageList {
	toolCallIDs := make(map[string]string)
	result := make(ai.MessageList, 0, len(messages))
	images := modelSupportsImage(model)
	var pendingToolCalls []*ai.ToolCall
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
		clear(existingToolResults)
	}

	for _, message := range messages {
		switch value := message.(type) {
		case *ai.SystemMessage:
			result = append(result, value)
		case *ai.UserMessage:
			if value.Content.Text == nil {
				if blocks, changed := adaptImages(value.Content.Blocks, images, nonVisionUserImagePlaceholder); changed {
					clone := *value
					clone.Content = ai.UserContent{Blocks: blocks}
					value = &clone
				}
			}
			insertSyntheticToolResults()
			result = append(result, value)
		case *ai.ToolResultMessage:
			content, changed := adaptImages(value.Content, images, nonVisionToolImagePlaceholder)
			normalized, renamed := toolCallIDs[value.ToolCallID]
			if changed || renamed {
				clone := *value
				clone.Content = content
				if renamed {
					clone.ToolCallID = normalized
				}
				value = &clone
			}
			existingToolResults[value.ToolCallID] = struct{}{}
			result = append(result, value)
		case *ai.AssistantMessage:
			// A failed turn is dropped, but the ids it renamed still apply.
			value = transformAssistantMessage(value, model, normalizeToolCallID, toolCallIDs)
			insertSyntheticToolResults()
			if value.StopReason == ai.StopReasonError || value.StopReason == ai.StopReasonAborted {
				continue
			}
			for _, content := range value.Content {
				if call, ok := content.(*ai.ToolCall); ok {
					pendingToolCalls = append(pendingToolCalls, call)
				}
			}
			result = append(result, value)
		}
	}
	insertSyntheticToolResults()
	return result
}

// transformAssistantMessage rewrites another model's thinking as text and
// drops its signatures, recording the tool call ids it renames.
func transformAssistantMessage(
	message *ai.AssistantMessage, model *ai.Model, normalizeToolCallID toolCallIDNormalizer, toolCallIDs map[string]string,
) *ai.AssistantMessage {
	isSameModel := message.Provider == model.Provider && message.API == model.API && message.Model == model.ID
	var content ai.AssistantContent // set once a block changes
	for index, rawBlock := range message.Content {
		next := rawBlock
		switch block := rawBlock.(type) {
		case *ai.ThinkingContent:
			switch {
			case block.Redacted != nil && *block.Redacted:
				if !isSameModel {
					next = nil
				}
			// Upstream keeps same-model thinking blocks on a truthy
			// signature, so an empty string does not count (OA-m3).
			case isSameModel && block.ThinkingSignature != nil && *block.ThinkingSignature != "":
			case strings.TrimSpace(block.Thinking) == "":
				next = nil
			case !isSameModel:
				next = &ai.TextContent{Text: block.Thinking}
			}
		case *ai.TextContent:
			if !isSameModel && block.TextSignature != nil {
				copy := *block
				copy.TextSignature = nil
				next = &copy
			}
		case *ai.ToolCall:
			if isSameModel {
				break
			}
			copy := *block
			copy.ThoughtSignature = nil
			if normalizeToolCallID != nil {
				if normalized := normalizeToolCallID(block.ID, model, message); normalized != block.ID {
					toolCallIDs[block.ID] = normalized
					copy.ID = normalized
				}
			}
			if copy.ID != block.ID || block.ThoughtSignature != nil {
				next = &copy
			}
		default:
			next = nil
		}
		if content == nil && (next != rawBlock || next == nil) {
			content = append(make(ai.AssistantContent, 0, len(message.Content)), message.Content[:index]...)
		}
		if content != nil && next != nil {
			content = append(content, next)
		}
	}
	if content == nil {
		return message
	}
	clone := *message
	clone.Content = content
	return &clone
}

// adaptImages returns blocks as they are unless the model cannot see their
// images, which become one placeholder per run, or they hold unknown blocks,
// which are dropped.
func adaptImages[S ~[]E, E any](blocks S, images bool, placeholder string) (S, bool) {
	changed := false
	for _, item := range blocks {
		switch any(item).(type) {
		case *ai.TextContent:
		case *ai.ImageContent:
			changed = changed || !images
		default:
			changed = true
		}
	}
	if !changed {
		return blocks, false
	}
	result := make(S, 0, len(blocks))
	previousWasPlaceholder := false
	for _, item := range blocks {
		switch block := any(item).(type) {
		case *ai.ImageContent:
			if images {
				result = append(result, item)
			} else if !previousWasPlaceholder {
				result = append(result, any(&ai.TextContent{Text: placeholder}).(E))
			}
			previousWasPlaceholder = !images
		case *ai.TextContent:
			result = append(result, item)
			previousWasPlaceholder = block.Text == placeholder
		}
	}
	return result, true
}
