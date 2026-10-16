package api

import (
	"errors"
	"strings"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/partialjson"
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

// transformAssistantMessage drops another model's thinking and signatures, recording the tool call ids it
// renames. Its thinking replayed as text would read to the new model as part of its replies.
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
			case !isSameModel:
				next = nil
			// Upstream keeps redacted blocks and blocks with a truthy signature, so an empty string does not
			// count (OA-m3).
			case block.Redacted != nil && *block.Redacted, block.ThinkingSignature != nil && *block.ThinkingSignature != "":
			case strings.TrimSpace(block.Thinking) == "":
				next = nil
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

// splitErrorImages takes the images out of an error tool result, which Claude rejects: the result keeps a
// placeholder per run, and after holds a caption and the images, to follow the turn's tool results.
func splitErrorImages(message *ai.ToolResultMessage) (content, after ai.ToolResultContent) {
	for _, item := range message.Content {
		if _, image := item.(*ai.ImageContent); image && message.IsError {
			if after == nil {
				after = ai.ToolResultContent{&ai.TextContent{Text: "Images from the error result of tool call " + message.ToolCallID + " (" + message.ToolName + "):"}}
			}
			after = append(after, item)
		}
	}
	if after == nil {
		return message.Content, nil
	}
	content, _ = adaptImages(message.Content, false, "(image attached after the tool results)")
	return content, after
}

// setStreamingToolArguments sets a call's arguments from their streamed prefix, keeping the wire member order.
func setStreamingToolArguments(call *ai.ToolCall, partial string) {
	if ai.SetToolCallPartialJSON(call, partial) != nil {
		_ = ai.SetToolCallArgumentsJSON(call, []byte(`{}`))
	}
}

// parseToolArguments reads streaming tool arguments, repairing what it can.
func parseToolArguments(text string) map[string]any {
	if object, ok := partialjson.ParseStreamingJSON(text).(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

// finishToolArguments sets a finished call's arguments. Text that is no JSON object even repaired leaves them
// empty and sets ArgumentsError, so the model sees what it sent instead of a run on the fields parsed before
// the error.
func finishToolArguments(call *ai.ToolCall, text string) {
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	if ai.SetToolCallArgumentsJSON(call, []byte(text)) == nil {
		return
	}
	value, err := partialjson.ParseJSONWithRepair(text)
	if object, ok := value.(map[string]any); ok && err == nil {
		call.Arguments = object
		return
	}
	if err == nil {
		err = errors.New("tool arguments must be a JSON object")
	}
	_ = ai.SetToolCallArgumentsJSON(call, []byte(`{}`))
	call.ArgumentsError = err.Error() + "\n\nReceived arguments:\n" + text
}
