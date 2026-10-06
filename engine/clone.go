package engine

import (
	"bytes"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/ptr"
)

func cloneAgentMessage(message AgentMessage) AgentMessage {
	switch value := message.(type) {
	case *ai.SystemMessage:
		if value == nil {
			return (*ai.SystemMessage)(nil)
		}
		if clone, ok := ai.CloneSystemMessage(value); ok {
			return clone
		}
		encoded, err := ai.Marshal(value)
		if err != nil {
			copy := *value
			return &copy
		}
		decoded, err := ai.UnmarshalMessage(encoded)
		if err != nil {
			copy := *value
			return &copy
		}
		return decoded
	case *ai.UserMessage:
		if value == nil {
			return (*ai.UserMessage)(nil)
		}
		copy := *value
		copy.Content = cloneUserContent(value.Content)
		return &copy
	case *ai.AssistantMessage:
		return cloneAssistantMessage(value)
	case *ai.ToolResultMessage:
		if value == nil {
			return (*ai.ToolResultMessage)(nil)
		}
		copy := *value
		copy.Content = cloneToolResultContent(value.Content)
		copy.Details = bytes.Clone(value.Details)
		copy.Usage = value.Usage.Clone()
		copy.AddedToolNames = cloneStringSlicePointer(value.AddedToolNames)
		if value.NestedCalls != nil {
			nested := *value.NestedCalls
			nested.Calls = append([]ai.NestedToolCallRecord(nil), value.NestedCalls.Calls...)
			copy.NestedCalls = &nested
		}
		return &copy
	default:
		return ai.CloneJSONValue(message)
	}
}

func cloneAssistantMessage(message *ai.AssistantMessage) *ai.AssistantMessage {
	if message == nil {
		return nil
	}
	copy := *message
	copy.Content = make(ai.AssistantContent, len(message.Content))
	for index, rawBlock := range message.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			if block != nil {
				blockCopy := *block
				blockCopy.TextSignature = ptr.Clone(block.TextSignature)
				copy.Content[index] = &blockCopy
			}
		case *ai.ThinkingContent:
			if block != nil {
				blockCopy := *block
				blockCopy.ThinkingSignature = ptr.Clone(block.ThinkingSignature)
				blockCopy.Redacted = ptr.Clone(block.Redacted)
				copy.Content[index] = &blockCopy
			}
		case *ai.ToolCall:
			if block != nil {
				blockCopy := *block
				blockCopy.Arguments, _ = ai.CloneJSONValue(block.Arguments).(map[string]any)
				blockCopy.ThoughtSignature = ptr.Clone(block.ThoughtSignature)
				blockCopy.PartialJSON = ptr.Clone(block.PartialJSON)
				blockCopy.PartialArgs = ptr.Clone(block.PartialArgs)
				blockCopy.StreamIndex = ptr.Clone(block.StreamIndex)
				copy.Content[index] = &blockCopy
			}
		case *ai.UnknownContentBlock:
			if block != nil {
				copy.Content[index] = &ai.UnknownContentBlock{Raw: bytes.Clone(block.Raw)}
			}
		default:
			copy.Content[index] = rawBlock
		}
	}
	copy.ResponseID = ptr.Clone(message.ResponseID)
	copy.ResponseModel = ptr.Clone(message.ResponseModel)
	copy.ErrorMessage = ptr.Clone(message.ErrorMessage)
	if message.Diagnostics != nil {
		diagnostics := make([]ai.AssistantMessageDiagnostic, len(*message.Diagnostics))
		for index, diagnostic := range *message.Diagnostics {
			diagnostics[index] = diagnostic
			diagnostics[index].Details = bytes.Clone(diagnostic.Details)
			if diagnostic.Error != nil {
				errorCopy := *diagnostic.Error
				errorCopy.Name = ptr.Clone(diagnostic.Error.Name)
				errorCopy.Stack = ptr.Clone(diagnostic.Error.Stack)
				errorCopy.Code = bytes.Clone(diagnostic.Error.Code)
				diagnostics[index].Error = &errorCopy
			}
		}
		copy.Diagnostics = &diagnostics
	}
	return &copy
}

func cloneUserContent(content ai.UserContent) ai.UserContent {
	copy := content
	copy.Text = ptr.Clone(content.Text)
	if content.Blocks != nil {
		copy.Blocks = make(ai.UserContentBlocks, len(content.Blocks))
		for index, rawBlock := range content.Blocks {
			switch block := rawBlock.(type) {
			case *ai.TextContent:
				if block != nil {
					blockCopy := *block
					blockCopy.TextSignature = ptr.Clone(block.TextSignature)
					copy.Blocks[index] = &blockCopy
				}
			case *ai.ImageContent:
				if block != nil {
					blockCopy := *block
					copy.Blocks[index] = &blockCopy
				}
			case *ai.UnknownContentBlock:
				if block != nil {
					copy.Blocks[index] = &ai.UnknownContentBlock{Raw: bytes.Clone(block.Raw)}
				}
			default:
				copy.Blocks[index] = rawBlock
			}
		}
	}
	return copy
}

func cloneToolResultContent(content ai.ToolResultContent) ai.ToolResultContent {
	if content == nil {
		return nil
	}
	copy := make(ai.ToolResultContent, len(content))
	for index, rawBlock := range content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			if block != nil {
				blockCopy := *block
				blockCopy.TextSignature = ptr.Clone(block.TextSignature)
				copy[index] = &blockCopy
			}
		case *ai.ImageContent:
			if block != nil {
				blockCopy := *block
				copy[index] = &blockCopy
			}
		case *ai.UnknownContentBlock:
			if block != nil {
				copy[index] = &ai.UnknownContentBlock{Raw: bytes.Clone(block.Raw)}
			}
		default:
			copy[index] = rawBlock
		}
	}
	return copy
}

func cloneAgentToolResult(result AgentToolResult) AgentToolResult {
	copy := result
	copy.Content = cloneToolResultContent(result.Content)
	copy.Details = ai.CloneJSONValue(result.Details)
	copy.StructuredContent = ai.CloneJSONValue(result.StructuredContent)
	copy.Usage = result.Usage.Clone()
	copy.AddedToolNames = cloneStringSlicePointer(result.AddedToolNames)
	copy.Terminate = ptr.Clone(result.Terminate)
	return copy
}

func cloneStringSlicePointer(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	copy := append([]string(nil), (*value)...)
	return &copy
}
