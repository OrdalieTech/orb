package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

var (
	errUnknownMessageRole = errors.New("ai: unknown message role")
	errUnknownContentType = errors.New("ai: unknown content type")
)

func MarshalMessage(message Message) ([]byte, error) {
	if message == nil {
		return nil, errors.New("ai: nil message")
	}
	return Marshal(message)
}

// Marshal encodes the ai wire format with JSON.stringify-compatible string
// escaping. Internal protocol and persistence surfaces must use this instead
// of encoding/json's HTML-safe default.
func Marshal(value any) ([]byte, error) {
	return marshalJSON(value)
}

func UnmarshalMessage(data []byte) (Message, error) {
	var header struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("ai: decode message role: %w", err)
	}
	var message Message
	switch header.Role {
	case "user":
		message = &UserMessage{}
	case "assistant":
		message = &AssistantMessage{}
	case "toolResult":
		message = &ToolResultMessage{}
	default:
		return nil, fmt.Errorf("%w %q", errUnknownMessageRole, header.Role)
	}
	if err := json.Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("ai: decode %s message: %w", header.Role, err)
	}
	return message, nil
}

func (message UserMessage) MarshalJSON() ([]byte, error) {
	type payload UserMessage
	return marshalJSON(struct {
		Role string `json:"role"`
		payload
	}{Role: "user", payload: payload(message)})
}

func (message AssistantMessage) MarshalJSON() ([]byte, error) {
	type payload AssistantMessage
	return marshalJSON(struct {
		Role string `json:"role"`
		payload
	}{Role: "assistant", payload: payload(message)})
}

func (message ToolResultMessage) MarshalJSON() ([]byte, error) {
	type payload ToolResultMessage
	return marshalJSON(struct {
		Role string `json:"role"`
		payload
	}{Role: "toolResult", payload: payload(message)})
}

func (content TextContent) MarshalJSON() ([]byte, error) {
	type payload TextContent
	return marshalJSON(struct {
		Type string `json:"type"`
		payload
	}{Type: "text", payload: payload(content)})
}

func (content ThinkingContent) MarshalJSON() ([]byte, error) {
	type payload ThinkingContent
	return marshalJSON(struct {
		Type string `json:"type"`
		payload
	}{Type: "thinking", payload: payload(content)})
}

func (content ImageContent) MarshalJSON() ([]byte, error) {
	type payload ImageContent
	return marshalJSON(struct {
		Type string `json:"type"`
		payload
	}{Type: "image", payload: payload(content)})
}

func (content ToolCall) MarshalJSON() ([]byte, error) {
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	return marshalJSON(struct {
		Type             string         `json:"type"`
		ID               string         `json:"id"`
		Name             string         `json:"name"`
		Arguments        map[string]any `json:"arguments"`
		ThoughtSignature *string        `json:"thoughtSignature,omitempty"`
	}{
		Type:             "toolCall",
		ID:               content.ID,
		Name:             content.Name,
		Arguments:        stringifyJSONObject(arguments),
		ThoughtSignature: content.ThoughtSignature,
	})
}

func (content *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature *string         `json:"thoughtSignature,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	arguments, err := decodeJSONObject(raw.Arguments)
	if err != nil {
		return fmt.Errorf("tool call arguments: %w", err)
	}
	*content = ToolCall{
		ID:               raw.ID,
		Name:             raw.Name,
		Arguments:        arguments,
		ThoughtSignature: raw.ThoughtSignature,
	}
	return nil
}

func (content UserContent) MarshalJSON() ([]byte, error) {
	if content.Text != nil {
		if content.Blocks != nil {
			return nil, errors.New("ai: user content has both text and blocks")
		}
		return marshalJSON(*content.Text)
	}
	if content.Blocks == nil {
		return []byte("[]"), nil
	}
	return marshalJSON(content.Blocks)
}

func (content *UserContent) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return err
		}
		content.Text = &text
		content.Blocks = nil
		return nil
	}
	var blocks UserContentBlocks
	if err := json.Unmarshal(data, &blocks); err != nil {
		return err
	}
	content.Text = nil
	content.Blocks = blocks
	return nil
}

func (blocks UserContentBlocks) MarshalJSON() ([]byte, error) {
	return marshalRequiredSlice(blocks)
}

func (blocks AssistantContent) MarshalJSON() ([]byte, error) {
	return marshalRequiredSlice(blocks)
}

func (blocks ToolResultContent) MarshalJSON() ([]byte, error) {
	return marshalRequiredSlice(blocks)
}

func (blocks ImagesContent) MarshalJSON() ([]byte, error) {
	return marshalRequiredSlice(blocks)
}

func (blocks *UserContentBlocks) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalBlocks(data, map[string]func() any{
		"text":  func() any { return &TextContent{} },
		"image": func() any { return &ImageContent{} },
	})
	if err != nil {
		return err
	}
	result := make(UserContentBlocks, 0, len(decoded))
	for _, block := range decoded {
		switch value := block.(type) {
		case *TextContent:
			result = append(result, value)
		case *ImageContent:
			result = append(result, value)
		}
	}
	*blocks = result
	return nil
}

func (blocks *AssistantContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalBlocks(data, map[string]func() any{
		"text":     func() any { return &TextContent{} },
		"thinking": func() any { return &ThinkingContent{} },
		"toolCall": func() any { return &ToolCall{} },
	})
	if err != nil {
		return err
	}
	result := make(AssistantContent, 0, len(decoded))
	for _, block := range decoded {
		switch value := block.(type) {
		case *TextContent:
			result = append(result, value)
		case *ThinkingContent:
			result = append(result, value)
		case *ToolCall:
			result = append(result, value)
		}
	}
	*blocks = result
	return nil
}

func (blocks *ToolResultContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalBlocks(data, map[string]func() any{
		"text":  func() any { return &TextContent{} },
		"image": func() any { return &ImageContent{} },
	})
	if err != nil {
		return err
	}
	result := make(ToolResultContent, 0, len(decoded))
	for _, block := range decoded {
		switch value := block.(type) {
		case *TextContent:
			result = append(result, value)
		case *ImageContent:
			result = append(result, value)
		}
	}
	*blocks = result
	return nil
}

func (blocks *ImagesContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalBlocks(data, map[string]func() any{
		"text":  func() any { return &TextContent{} },
		"image": func() any { return &ImageContent{} },
	})
	if err != nil {
		return err
	}
	result := make(ImagesContent, 0, len(decoded))
	for _, block := range decoded {
		switch value := block.(type) {
		case *TextContent:
			result = append(result, value)
		case *ImageContent:
			result = append(result, value)
		}
	}
	*blocks = result
	return nil
}

func (messages MessageList) MarshalJSON() ([]byte, error) {
	if messages == nil {
		return []byte("[]"), nil
	}
	type messageList MessageList
	return marshalJSON(messageList(messages))
}

func (messages *MessageList) UnmarshalJSON(data []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	decoded := make(MessageList, 0, len(raw))
	for index, item := range raw {
		message, err := UnmarshalMessage(item)
		if err != nil {
			return fmt.Errorf("message %d: %w", index, err)
		}
		decoded = append(decoded, message)
	}
	*messages = decoded
	return nil
}

func unmarshalBlocks(data []byte, factories map[string]func() any) ([]any, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	decoded := make([]any, 0, len(raw))
	for index, item := range raw {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(item, &header); err != nil {
			return nil, fmt.Errorf("content %d type: %w", index, err)
		}
		factory := factories[header.Type]
		if factory == nil {
			return nil, fmt.Errorf("content %d: %w %q", index, errUnknownContentType, header.Type)
		}
		value := factory()
		if err := json.Unmarshal(item, value); err != nil {
			return nil, fmt.Errorf("content %d %s: %w", index, header.Type, err)
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

func decodeJSONObject(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, errors.New("missing object")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	if value == nil {
		return nil, errors.New("arguments must be an object")
	}
	return value, nil
}

func marshalRequiredSlice[T any](values []T) ([]byte, error) {
	if values == nil {
		return []byte("[]"), nil
	}
	type slice []T
	return marshalJSON(slice(values))
}

func marshalJSON(value any) ([]byte, error) {
	return jsonwire.Marshal(value)
}

func stringifyJSONObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = stringifyJSONValue(item)
	}
	return result
}

func stringifyJSONValue(value any) any {
	switch value := value.(type) {
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return nil
		}
		if value == 0 {
			return float64(0)
		}
		return value
	case float32:
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil
		}
		if value == 0 {
			return float32(0)
		}
		return value
	case map[string]any:
		return stringifyJSONObject(value)
	case []any:
		result := make([]any, len(value))
		for index, item := range value {
			result[index] = stringifyJSONValue(item)
		}
		return result
	default:
		return value
	}
}
