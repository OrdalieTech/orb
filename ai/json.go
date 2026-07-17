package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"

	"github.com/OrdalieTech/pi-go/internal/jsonwire"
	"github.com/OrdalieTech/pi-go/internal/partialjson"
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
	var errorMessage json.RawMessage
	if message.ErrorMessage != nil {
		var err error
		errorMessage, err = jsonwire.MarshalString(*message.ErrorMessage)
		if err != nil {
			return nil, err
		}
	}
	if message.errorBeforeTimestamp && message.ErrorMessage != nil {
		return marshalJSON(struct {
			Role          string                        `json:"role"`
			Content       AssistantContent              `json:"content"`
			API           API                           `json:"api"`
			Provider      ProviderID                    `json:"provider"`
			Model         string                        `json:"model"`
			Usage         Usage                         `json:"usage"`
			StopReason    StopReason                    `json:"stopReason"`
			ErrorMessage  json.RawMessage               `json:"errorMessage"`
			ResponseID    *string                       `json:"responseId,omitempty"`
			ResponseModel *string                       `json:"responseModel,omitempty"`
			Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
			Timestamp     int64                         `json:"timestamp"`
		}{
			Role:          "assistant",
			Content:       message.Content,
			API:           message.API,
			Provider:      message.Provider,
			Model:         message.Model,
			Usage:         message.Usage,
			StopReason:    message.StopReason,
			ErrorMessage:  errorMessage,
			ResponseID:    message.ResponseID,
			ResponseModel: message.ResponseModel,
			Diagnostics:   message.Diagnostics,
			Timestamp:     message.Timestamp,
		})
	}
	return marshalJSON(struct {
		Role          string                        `json:"role"`
		Content       AssistantContent              `json:"content"`
		API           API                           `json:"api"`
		Provider      ProviderID                    `json:"provider"`
		Model         string                        `json:"model"`
		Usage         Usage                         `json:"usage"`
		StopReason    StopReason                    `json:"stopReason"`
		Timestamp     int64                         `json:"timestamp"`
		ResponseID    *string                       `json:"responseId,omitempty"`
		ResponseModel *string                       `json:"responseModel,omitempty"`
		Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
		ErrorMessage  json.RawMessage               `json:"errorMessage,omitempty"`
	}{
		Role:          "assistant",
		Content:       message.Content,
		API:           message.API,
		Provider:      message.Provider,
		Model:         message.Model,
		Usage:         message.Usage,
		StopReason:    message.StopReason,
		Timestamp:     message.Timestamp,
		ResponseID:    message.ResponseID,
		ResponseModel: message.ResponseModel,
		Diagnostics:   message.Diagnostics,
		ErrorMessage:  errorMessage,
	})
}

// SetAssistantMessageErrorBeforeTimestamp preserves the member order of
// upstream message constructors that insert errorMessage before timestamp.
func SetAssistantMessageErrorBeforeTimestamp(message *AssistantMessage, enabled bool) {
	if message != nil {
		message.errorBeforeTimestamp = enabled
	}
}

func (message ToolResultMessage) MarshalJSON() ([]byte, error) {
	type payload ToolResultMessage
	return marshalJSON(struct {
		Role string `json:"role"`
		payload
	}{Role: "toolResult", payload: payload(message)})
}

func (content TextContent) MarshalJSON() ([]byte, error) {
	text, err := jsonwire.MarshalString(content.Text)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type          string          `json:"type"`
		Text          json.RawMessage `json:"text"`
		TextSignature *string         `json:"textSignature,omitempty"`
	}{Type: "text", Text: text, TextSignature: content.TextSignature})
}

func (content ThinkingContent) MarshalJSON() ([]byte, error) {
	thinking, err := jsonwire.MarshalString(content.Thinking)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type              string          `json:"type"`
		Thinking          json.RawMessage `json:"thinking"`
		ThinkingSignature *string         `json:"thinkingSignature,omitempty"`
		Redacted          *bool           `json:"redacted,omitempty"`
	}{
		Type:              "thinking",
		Thinking:          thinking,
		ThinkingSignature: content.ThinkingSignature,
		Redacted:          content.Redacted,
	})
}

func (content ImageContent) MarshalJSON() ([]byte, error) {
	type payload ImageContent
	return marshalJSON(struct {
		Type string `json:"type"`
		payload
	}{Type: "image", payload: payload(content)})
}

func (content ToolCall) MarshalJSON() ([]byte, error) {
	arguments, err := MarshalToolCallArguments(&content)
	if err != nil {
		return nil, err
	}
	if content.PartialJSON != nil || content.PartialArgs != nil || content.StreamIndex != nil {
		return marshalJSON(struct {
			Type             string          `json:"type"`
			ID               string          `json:"id"`
			Name             string          `json:"name"`
			Arguments        json.RawMessage `json:"arguments"`
			PartialJSON      *string         `json:"partialJson,omitempty"`
			PartialArgs      *string         `json:"partialArgs,omitempty"`
			StreamIndex      *int            `json:"streamIndex,omitempty"`
			ThoughtSignature *string         `json:"thoughtSignature,omitempty"`
		}{
			Type:             "toolCall",
			ID:               content.ID,
			Name:             content.Name,
			Arguments:        arguments,
			PartialJSON:      content.PartialJSON,
			PartialArgs:      content.PartialArgs,
			StreamIndex:      content.StreamIndex,
			ThoughtSignature: content.ThoughtSignature,
		})
	}
	return marshalJSON(struct {
		Type             string          `json:"type"`
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature *string         `json:"thoughtSignature,omitempty"`
	}{
		Type:             "toolCall",
		ID:               content.ID,
		Name:             content.Name,
		Arguments:        arguments,
		ThoughtSignature: content.ThoughtSignature,
	})
}

func (content *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               string          `json:"id"`
		Name             string          `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature *string         `json:"thoughtSignature,omitempty"`
		PartialJSON      *string         `json:"partialJson,omitempty"`
		PartialArgs      *string         `json:"partialArgs,omitempty"`
		StreamIndex      *int            `json:"streamIndex,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*content = ToolCall{
		ID:               raw.ID,
		Name:             raw.Name,
		ThoughtSignature: raw.ThoughtSignature,
		PartialJSON:      raw.PartialJSON,
		PartialArgs:      raw.PartialArgs,
		StreamIndex:      raw.StreamIndex,
	}
	if err := SetToolCallArgumentsJSON(content, raw.Arguments); err != nil {
		return fmt.Errorf("tool call arguments: %w", err)
	}
	return nil
}

// SetToolCallArgumentsJSON records a complete provider-emitted argument object
// so a later replay preserves JSON.stringify's original member order.
func SetToolCallArgumentsJSON(content *ToolCall, data []byte) error {
	if content == nil {
		return errors.New("ai: nil tool call")
	}
	normalizedArguments, err := NormalizeJSONStringifyJSON(data)
	if err != nil {
		return err
	}
	arguments, err := decodeJSONObject(normalizedArguments)
	if err != nil {
		return err
	}
	content.Arguments = arguments
	content.rawArguments = normalizedArguments
	return nil
}

// MarshalToolCallArguments preserves decoded object member order while the
// public argument map remains semantically unchanged.
func MarshalToolCallArguments(content *ToolCall) ([]byte, error) {
	if content == nil {
		return nil, errors.New("ai: nil tool call")
	}
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(content.rawArguments) > 0 {
		original, err := decodeJSONObject(content.rawArguments)
		if err == nil && reflect.DeepEqual(original, arguments) {
			return bytes.Clone(content.rawArguments), nil
		}
	}
	for _, partial := range []*string{content.PartialJSON, content.PartialArgs} {
		if partial == nil {
			continue
		}
		encoded, err := partialjson.StringifyStreamingJSON(*partial)
		if err == nil {
			if _, objectErr := decodeJSONObject(encoded); objectErr == nil {
				return encoded, nil
			}
		}
	}
	return marshalJSON(stringifyJSONObject(arguments))
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

// NormalizeJSONStringifyJSON parses JSON with JavaScript Number semantics and
// re-emits the same value using JSON.stringify's ordering and scalar spelling.
func NormalizeJSONStringifyJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var output bytes.Buffer
	if err := writeJSONStringifyJSONValue(&output, decoder); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return output.Bytes(), nil
}

func writeJSONStringifyJSONValue(output *bytes.Buffer, decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); ok {
		switch delimiter {
		case '{':
			type member struct {
				name  string
				value []byte
			}
			members := make([]member, 0)
			indexes := make(map[string]int)
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				var value bytes.Buffer
				if err := writeJSONStringifyJSONValue(&value, decoder); err != nil {
					return err
				}
				if index, exists := indexes[name]; exists {
					members[index].value = value.Bytes()
				} else {
					indexes[name] = len(members)
					members = append(members, member{name: name, value: value.Bytes()})
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim('}') {
				return errors.New("object is not closed")
			}
			sort.SliceStable(members, func(left, right int) bool {
				leftIndex, leftIsIndex := jsArrayIndex(members[left].name)
				rightIndex, rightIsIndex := jsArrayIndex(members[right].name)
				if leftIsIndex && rightIsIndex {
					return leftIndex < rightIndex
				}
				return leftIsIndex && !rightIsIndex
			})
			output.WriteByte('{')
			for index, member := range members {
				if index > 0 {
					output.WriteByte(',')
				}
				encodedName, err := marshalJSON(member.name)
				if err != nil {
					return err
				}
				output.Write(encodedName)
				output.WriteByte(':')
				output.Write(member.value)
			}
			output.WriteByte('}')
			return nil
		case '[':
			output.WriteByte('[')
			for index := 0; decoder.More(); index++ {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := writeJSONStringifyJSONValue(output, decoder); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil {
				return err
			}
			if closing != json.Delim(']') {
				return errors.New("array is not closed")
			}
			output.WriteByte(']')
			return nil
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}

	if number, ok := token.(json.Number); ok {
		value, err := strconv.ParseFloat(number.String(), 64)
		if err != nil && !math.IsInf(value, 0) {
			return err
		}
		if math.IsInf(value, 0) || math.IsNaN(value) {
			output.WriteString("null")
			return nil
		}
		if value == 0 {
			output.WriteByte('0')
			return nil
		}
		encoded, err := marshalJSON(value)
		if err != nil {
			return err
		}
		output.Write(encoded)
		return nil
	}
	encoded, err := marshalJSON(token)
	if err != nil {
		return err
	}
	output.Write(encoded)
	return nil
}

func jsArrayIndex(name string) (uint64, bool) {
	if name == "0" {
		return 0, true
	}
	if name == "" || name[0] == '0' {
		return 0, false
	}
	value, err := strconv.ParseUint(name, 10, 32)
	if err != nil || value == math.MaxUint32 || strconv.FormatUint(value, 10) != name {
		return 0, false
	}
	return value, true
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
