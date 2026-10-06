package faux

import (
	"bytes"
	"fmt"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/ptr"
)

func cloneMessage(source *ai.AssistantMessage) (*ai.AssistantMessage, error) {
	if source == nil {
		return nil, fmt.Errorf("faux: response is nil")
	}
	clone := *source
	clone.ResponseID = ptr.Clone(source.ResponseID)
	clone.ResponseModel = ptr.Clone(source.ResponseModel)
	clone.ErrorMessage = ptr.Clone(source.ErrorMessage)
	if source.Diagnostics != nil {
		diagnostics := make([]ai.AssistantMessageDiagnostic, len(*source.Diagnostics))
		for index, diagnostic := range *source.Diagnostics {
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
		clone.Diagnostics = &diagnostics
	}
	clone.Content = make(ai.AssistantContent, 0, len(source.Content))
	for _, rawBlock := range source.Content {
		switch block := rawBlock.(type) {
		case *ai.TextContent:
			copy := *block
			copy.TextSignature = ptr.Clone(block.TextSignature)
			clone.Content = append(clone.Content, &copy)
		case *ai.ThinkingContent:
			copy := *block
			copy.ThinkingSignature = ptr.Clone(block.ThinkingSignature)
			copy.Redacted = ptr.Clone(block.Redacted)
			clone.Content = append(clone.Content, &copy)
		case *ai.ToolCall:
			arguments, err := ai.MarshalToolCallArguments(block)
			if err != nil {
				return nil, err
			}
			copy := &ai.ToolCall{
				ID:               block.ID,
				Name:             block.Name,
				ThoughtSignature: ptr.Clone(block.ThoughtSignature),
				PartialJSON:      ptr.Clone(block.PartialJSON),
				PartialArgs:      ptr.Clone(block.PartialArgs),
				StreamIndex:      ptr.Clone(block.StreamIndex),
			}
			if err := ai.SetToolCallArgumentsJSON(copy, arguments); err != nil {
				return nil, err
			}
			clone.Content = append(clone.Content, copy)
		default:
			return nil, fmt.Errorf("faux: unsupported assistant content block %T", rawBlock)
		}
	}
	return &clone, nil
}
