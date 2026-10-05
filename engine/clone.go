package engine

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/OrdalieTech/orb/ai"
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
		copy.Usage = cloneUsage(value.Usage)
		copy.AddedToolNames = cloneStringSlicePointer(value.AddedToolNames)
		if value.NestedCalls != nil {
			nested := *value.NestedCalls
			nested.Calls = append([]ai.NestedToolCallRecord(nil), value.NestedCalls.Calls...)
			copy.NestedCalls = &nested
		}
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
				blockCopy.TextSignature = clonePointer(block.TextSignature)
				copy.Content[index] = &blockCopy
			}
		case *ai.ThinkingContent:
			if block != nil {
				blockCopy := *block
				blockCopy.ThinkingSignature = clonePointer(block.ThinkingSignature)
				blockCopy.Redacted = clonePointer(block.Redacted)
				copy.Content[index] = &blockCopy
			}
		case *ai.ToolCall:
			if block != nil {
				blockCopy := *block
				blockCopy.Arguments = cloneJSONObject(block.Arguments)
				blockCopy.ThoughtSignature = clonePointer(block.ThoughtSignature)
				blockCopy.PartialJSON = clonePointer(block.PartialJSON)
				blockCopy.PartialArgs = clonePointer(block.PartialArgs)
				blockCopy.StreamIndex = clonePointer(block.StreamIndex)
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
	copy.ResponseID = clonePointer(message.ResponseID)
	copy.ResponseModel = clonePointer(message.ResponseModel)
	copy.ErrorMessage = clonePointer(message.ErrorMessage)
	if message.Diagnostics != nil {
		diagnostics := make([]ai.AssistantMessageDiagnostic, len(*message.Diagnostics))
		for index, diagnostic := range *message.Diagnostics {
			diagnostics[index] = diagnostic
			diagnostics[index].Details = bytes.Clone(diagnostic.Details)
			if diagnostic.Error != nil {
				errorCopy := *diagnostic.Error
				errorCopy.Name = clonePointer(diagnostic.Error.Name)
				errorCopy.Stack = clonePointer(diagnostic.Error.Stack)
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
	copy.Text = clonePointer(content.Text)
	if content.Blocks != nil {
		copy.Blocks = make(ai.UserContentBlocks, len(content.Blocks))
		for index, rawBlock := range content.Blocks {
			switch block := rawBlock.(type) {
			case *ai.TextContent:
				if block != nil {
					blockCopy := *block
					blockCopy.TextSignature = clonePointer(block.TextSignature)
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
				blockCopy.TextSignature = clonePointer(block.TextSignature)
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
	copy.StructuredContent = cloneJSONValue(result.StructuredContent)
	copy.Usage = cloneUsage(result.Usage)
	copy.AddedToolNames = cloneStringSlicePointer(result.AddedToolNames)
	copy.Terminate = clonePointer(result.Terminate)
	return copy
}

func cloneUsage(usage *ai.Usage) *ai.Usage {
	if usage == nil {
		return nil
	}
	copy := *usage
	if usage.Reasoning != nil {
		value := *usage.Reasoning
		copy.Reasoning = &value
	}
	if usage.CacheWrite1h != nil {
		value := *usage.CacheWrite1h
		copy.CacheWrite1h = &value
	}
	return &copy
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

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	return new(*value)
}

func cloneStringSlicePointer(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	copy := append([]string(nil), (*value)...)
	return &copy
}
