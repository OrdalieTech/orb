package agent

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/OrdalieTech/pi-go/ai"
)

func cloneAgentMessage(message AgentMessage) AgentMessage {
	switch value := message.(type) {
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
		copy.AddedToolNames = cloneStringSlicePointer(value.AddedToolNames)
		return &copy
	default:
		return cloneJSONValue(message)
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
				blockCopy.TextSignature = cloneStringPointer(block.TextSignature)
				copy.Content[index] = &blockCopy
			}
		case *ai.ThinkingContent:
			if block != nil {
				blockCopy := *block
				blockCopy.ThinkingSignature = cloneStringPointer(block.ThinkingSignature)
				blockCopy.Redacted = cloneBoolPointer(block.Redacted)
				copy.Content[index] = &blockCopy
			}
		case *ai.ToolCall:
			if block != nil {
				blockCopy := *block
				blockCopy.Arguments = cloneJSONObject(block.Arguments)
				blockCopy.ThoughtSignature = cloneStringPointer(block.ThoughtSignature)
				blockCopy.PartialJSON = cloneStringPointer(block.PartialJSON)
				blockCopy.PartialArgs = cloneStringPointer(block.PartialArgs)
				blockCopy.StreamIndex = cloneIntPointer(block.StreamIndex)
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
	copy.ResponseID = cloneStringPointer(message.ResponseID)
	copy.ResponseModel = cloneStringPointer(message.ResponseModel)
	copy.ErrorMessage = cloneStringPointer(message.ErrorMessage)
	if message.Diagnostics != nil {
		diagnostics := make([]ai.AssistantMessageDiagnostic, len(*message.Diagnostics))
		for index, diagnostic := range *message.Diagnostics {
			diagnostics[index] = diagnostic
			diagnostics[index].Details = bytes.Clone(diagnostic.Details)
			if diagnostic.Error != nil {
				errorCopy := *diagnostic.Error
				errorCopy.Name = cloneStringPointer(diagnostic.Error.Name)
				errorCopy.Stack = cloneStringPointer(diagnostic.Error.Stack)
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
	copy.Text = cloneStringPointer(content.Text)
	if content.Blocks != nil {
		copy.Blocks = make(ai.UserContentBlocks, len(content.Blocks))
		for index, rawBlock := range content.Blocks {
			switch block := rawBlock.(type) {
			case *ai.TextContent:
				if block != nil {
					blockCopy := *block
					blockCopy.TextSignature = cloneStringPointer(block.TextSignature)
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
				blockCopy.TextSignature = cloneStringPointer(block.TextSignature)
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
	copy.Details = cloneJSONValue(result.Details)
	copy.AddedToolNames = cloneStringSlicePointer(result.AddedToolNames)
	copy.Terminate = cloneBoolPointer(result.Terminate)
	return copy
}

func cloneJSONObject(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	copy := make(map[string]any, len(source))
	for key, value := range source {
		copy[key] = cloneJSONValue(value)
	}
	return copy
}

func cloneJSONValue(value any) any {
	if value == nil {
		return nil
	}
	switch typed := value.(type) {
	case map[string]any:
		return cloneJSONObject(typed)
	case []any:
		copy := make([]any, len(typed))
		for index, item := range typed {
			copy[index] = cloneJSONValue(item)
		}
		return copy
	case json.RawMessage:
		return json.RawMessage(bytes.Clone(typed))
	}
	return cloneJSONReflect(reflect.ValueOf(value)).Interface()
}

func cloneJSONReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(cloneJSONReflect(value.Elem()))
		return copy
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			copy.SetMapIndex(iterator.Key(), cloneJSONReflect(iterator.Value()))
		}
		return copy
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type().Elem())
		copy.Elem().Set(cloneJSONReflect(value.Elem()))
		return copy
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneJSONReflect(value.Index(index)))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			copy.Index(index).Set(cloneJSONReflect(value.Index(index)))
		}
		return copy
	case reflect.Struct:
		copy := reflect.New(value.Type()).Elem()
		copy.Set(value)
		for index := 0; index < value.NumField(); index++ {
			if copy.Field(index).CanSet() && value.Field(index).CanInterface() {
				copy.Field(index).Set(cloneJSONReflect(value.Field(index)))
			}
		}
		return copy
	default:
		return value
	}
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneBoolPointer(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneIntPointer(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneStringSlicePointer(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	copy := append([]string(nil), (*value)...)
	return &copy
}
