package faux

import (
	"bytes"
	"fmt"

	"github.com/OrdalieTech/pigo/ai"
)

func cloneMessage(source *ai.AssistantMessage) (*ai.AssistantMessage, error) {
	if source == nil {
		return nil, fmt.Errorf("faux: response is nil")
	}
	clone := *source
	clone.ResponseID = cloneString(source.ResponseID)
	clone.ResponseModel = cloneString(source.ResponseModel)
	clone.ErrorMessage = cloneString(source.ErrorMessage)
	if source.Diagnostics != nil {
		diagnostics := make([]ai.AssistantMessageDiagnostic, len(*source.Diagnostics))
		for index, diagnostic := range *source.Diagnostics {
			diagnostics[index] = diagnostic
			diagnostics[index].Details = bytes.Clone(diagnostic.Details)
			if diagnostic.Error != nil {
				errorCopy := *diagnostic.Error
				errorCopy.Name = cloneString(diagnostic.Error.Name)
				errorCopy.Stack = cloneString(diagnostic.Error.Stack)
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
			copy.TextSignature = cloneString(block.TextSignature)
			clone.Content = append(clone.Content, &copy)
		case *ai.ThinkingContent:
			copy := *block
			copy.ThinkingSignature = cloneString(block.ThinkingSignature)
			if block.Redacted != nil {
				redacted := *block.Redacted
				copy.Redacted = &redacted
			}
			clone.Content = append(clone.Content, &copy)
		case *ai.ToolCall:
			arguments, err := ai.MarshalToolCallArguments(block)
			if err != nil {
				return nil, err
			}
			copy := &ai.ToolCall{
				ID:               block.ID,
				Name:             block.Name,
				ThoughtSignature: cloneString(block.ThoughtSignature),
				PartialJSON:      cloneString(block.PartialJSON),
				PartialArgs:      cloneString(block.PartialArgs),
			}
			if block.StreamIndex != nil {
				streamIndex := *block.StreamIndex
				copy.StreamIndex = &streamIndex
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
