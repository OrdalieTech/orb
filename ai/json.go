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

	"github.com/OrdalieTech/pigo/internal/jsonwire"
	"github.com/OrdalieTech/pigo/internal/partialjson"
)

var (
	errUnknownMessageRole = errors.New("ai: unknown message role")
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

const (
	usageOptionalsDefault uint8 = iota
	usageOptionalsBeforeTotals
	usageOptionalsAfterCost
)

func (usage Usage) MarshalJSON() ([]byte, error) {
	beforeTotals := usage.optionalOrder == usageOptionalsBeforeTotals ||
		usage.optionalOrder == usageOptionalsDefault && usage.CacheWrite1h == nil
	if beforeTotals {
		return marshalJSON(struct {
			Input        int64  `json:"input"`
			Output       int64  `json:"output"`
			CacheRead    int64  `json:"cacheRead"`
			CacheWrite   int64  `json:"cacheWrite"`
			CacheWrite1h *int64 `json:"cacheWrite1h,omitempty"`
			Reasoning    *int64 `json:"reasoning,omitempty"`
			TotalTokens  int64  `json:"totalTokens"`
			Cost         Cost   `json:"cost"`
		}{
			Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite,
			CacheWrite1h: usage.CacheWrite1h, Reasoning: usage.Reasoning, TotalTokens: usage.TotalTokens, Cost: usage.Cost,
		})
	}
	return marshalJSON(struct {
		Input        int64  `json:"input"`
		Output       int64  `json:"output"`
		CacheRead    int64  `json:"cacheRead"`
		CacheWrite   int64  `json:"cacheWrite"`
		TotalTokens  int64  `json:"totalTokens"`
		Cost         Cost   `json:"cost"`
		CacheWrite1h *int64 `json:"cacheWrite1h,omitempty"`
		Reasoning    *int64 `json:"reasoning,omitempty"`
	}{
		Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, CacheWrite: usage.CacheWrite,
		TotalTokens: usage.TotalTokens, Cost: usage.Cost, CacheWrite1h: usage.CacheWrite1h, Reasoning: usage.Reasoning,
	})
}

func (usage *Usage) UnmarshalJSON(data []byte) error {
	type plainUsage Usage
	var decoded plainUsage
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*usage = Usage(decoded)
	if usage.CacheWrite1h != nil || usage.Reasoning != nil {
		usage.optionalOrder = usageOptionalsAfterCost
		if topLevelMemberBefore(data, "cacheWrite1h", "totalTokens") || topLevelMemberBefore(data, "reasoning", "totalTokens") {
			usage.optionalOrder = usageOptionalsBeforeTotals
		}
	}
	return nil
}

// SetUsageOptionalsBeforeTotals preserves the order of upstream usage objects built by compaction aggregation.
func SetUsageOptionalsBeforeTotals(usage *Usage) {
	if usage != nil && (usage.CacheWrite1h != nil || usage.Reasoning != nil) {
		usage.optionalOrder = usageOptionalsBeforeTotals
	}
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
	api, err := jsonwire.MarshalString(string(message.API))
	if err != nil {
		return nil, err
	}
	provider, err := jsonwire.MarshalString(string(message.Provider))
	if err != nil {
		return nil, err
	}
	model, err := jsonwire.MarshalString(message.Model)
	if err != nil {
		return nil, err
	}
	stopReason, err := jsonwire.MarshalString(string(message.StopReason))
	if err != nil {
		return nil, err
	}
	errorMessage, err := marshalOptionalWireString(message.ErrorMessage)
	if err != nil {
		return nil, err
	}
	responseID, err := marshalOptionalWireString(message.ResponseID)
	if err != nil {
		return nil, err
	}
	responseModel, err := marshalOptionalWireString(message.ResponseModel)
	if err != nil {
		return nil, err
	}
	if message.errorBeforeTimestamp && message.ErrorMessage != nil {
		return marshalJSON(struct {
			Role          string                        `json:"role"`
			Content       AssistantContent              `json:"content"`
			API           json.RawMessage               `json:"api"`
			Provider      json.RawMessage               `json:"provider"`
			Model         json.RawMessage               `json:"model"`
			Usage         Usage                         `json:"usage"`
			StopReason    json.RawMessage               `json:"stopReason"`
			ErrorMessage  json.RawMessage               `json:"errorMessage"`
			ResponseID    json.RawMessage               `json:"responseId,omitempty"`
			ResponseModel json.RawMessage               `json:"responseModel,omitempty"`
			Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
			Timestamp     int64                         `json:"timestamp"`
		}{
			Role:          "assistant",
			Content:       message.Content,
			API:           api,
			Provider:      provider,
			Model:         model,
			Usage:         message.Usage,
			StopReason:    stopReason,
			ErrorMessage:  errorMessage,
			ResponseID:    responseID,
			ResponseModel: responseModel,
			Diagnostics:   message.Diagnostics,
			Timestamp:     message.Timestamp,
		})
	}
	if message.errorBeforeResponseID && message.ErrorMessage != nil {
		return marshalJSON(struct {
			Role          string                        `json:"role"`
			Content       AssistantContent              `json:"content"`
			API           json.RawMessage               `json:"api"`
			Provider      json.RawMessage               `json:"provider"`
			Model         json.RawMessage               `json:"model"`
			Usage         Usage                         `json:"usage"`
			StopReason    json.RawMessage               `json:"stopReason"`
			Timestamp     int64                         `json:"timestamp"`
			ErrorMessage  json.RawMessage               `json:"errorMessage"`
			ResponseID    json.RawMessage               `json:"responseId,omitempty"`
			ResponseModel json.RawMessage               `json:"responseModel,omitempty"`
			Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
		}{
			Role: "assistant", Content: message.Content, API: api, Provider: provider, Model: model,
			Usage: message.Usage, StopReason: stopReason, Timestamp: message.Timestamp,
			ErrorMessage: errorMessage, ResponseID: responseID, ResponseModel: responseModel, Diagnostics: message.Diagnostics,
		})
	}
	return marshalJSON(struct {
		Role          string                        `json:"role"`
		Content       AssistantContent              `json:"content"`
		API           json.RawMessage               `json:"api"`
		Provider      json.RawMessage               `json:"provider"`
		Model         json.RawMessage               `json:"model"`
		Usage         Usage                         `json:"usage"`
		StopReason    json.RawMessage               `json:"stopReason"`
		Timestamp     int64                         `json:"timestamp"`
		ResponseID    json.RawMessage               `json:"responseId,omitempty"`
		ResponseModel json.RawMessage               `json:"responseModel,omitempty"`
		Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics,omitempty"`
		ErrorMessage  json.RawMessage               `json:"errorMessage,omitempty"`
	}{
		Role:          "assistant",
		Content:       message.Content,
		API:           api,
		Provider:      provider,
		Model:         model,
		Usage:         message.Usage,
		StopReason:    stopReason,
		Timestamp:     message.Timestamp,
		ResponseID:    responseID,
		ResponseModel: responseModel,
		Diagnostics:   message.Diagnostics,
		ErrorMessage:  errorMessage,
	})
}

func (message *AssistantMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		Content       AssistantContent              `json:"content"`
		API           json.RawMessage               `json:"api"`
		Provider      json.RawMessage               `json:"provider"`
		Model         json.RawMessage               `json:"model"`
		Usage         Usage                         `json:"usage"`
		StopReason    json.RawMessage               `json:"stopReason"`
		Timestamp     int64                         `json:"timestamp"`
		ResponseID    json.RawMessage               `json:"responseId"`
		ResponseModel json.RawMessage               `json:"responseModel"`
		Diagnostics   *[]AssistantMessageDiagnostic `json:"diagnostics"`
		ErrorMessage  json.RawMessage               `json:"errorMessage"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	api, err := unmarshalWireString(raw.API)
	if err != nil {
		return err
	}
	provider, err := unmarshalWireString(raw.Provider)
	if err != nil {
		return err
	}
	model, err := unmarshalWireString(raw.Model)
	if err != nil {
		return err
	}
	stopReason, err := unmarshalWireString(raw.StopReason)
	if err != nil {
		return err
	}
	responseID, err := unmarshalOptionalWireString(raw.ResponseID)
	if err != nil {
		return err
	}
	responseModel, err := unmarshalOptionalWireString(raw.ResponseModel)
	if err != nil {
		return err
	}
	errorMessage, err := unmarshalOptionalWireString(raw.ErrorMessage)
	if err != nil {
		return err
	}
	*message = AssistantMessage{
		Content:       raw.Content,
		API:           API(api),
		Provider:      ProviderID(provider),
		Model:         model,
		Usage:         raw.Usage,
		StopReason:    StopReason(stopReason),
		Timestamp:     raw.Timestamp,
		ResponseID:    responseID,
		ResponseModel: responseModel,
		Diagnostics:   raw.Diagnostics,
		ErrorMessage:  errorMessage,
	}
	message.errorBeforeTimestamp = topLevelMemberBefore(data, "errorMessage", "timestamp")
	message.errorBeforeResponseID = !message.errorBeforeTimestamp && topLevelMemberBefore(data, "errorMessage", "responseId")
	return nil
}

func topLevelMemberBefore(data []byte, first, second string) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seenFirst := false
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := token.(string)
		if !ok {
			return false
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return false
		}
		switch name {
		case first:
			seenFirst = true
		case second:
			return seenFirst
		}
	}
	return false
}

// SetAssistantMessageErrorBeforeTimestamp preserves the member order of
// upstream message constructors that insert errorMessage before timestamp.
func SetAssistantMessageErrorBeforeTimestamp(message *AssistantMessage, enabled bool) {
	if message != nil {
		message.errorBeforeTimestamp = enabled
	}
}

// SetAssistantMessageErrorBeforeResponseID preserves the order produced when a
// streaming backend appends errorMessage before responseId to an existing message.
func SetAssistantMessageErrorBeforeResponseID(message *AssistantMessage, enabled bool) {
	if message != nil {
		message.errorBeforeResponseID = enabled
	}
}

func (message ToolResultMessage) MarshalJSON() ([]byte, error) {
	toolCallID, err := jsonwire.MarshalString(message.ToolCallID)
	if err != nil {
		return nil, err
	}
	toolName, err := jsonwire.MarshalString(message.ToolName)
	if err != nil {
		return nil, err
	}
	addedToolNames, err := marshalOptionalWireStringSlice(message.AddedToolNames)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Role           string            `json:"role"`
		ToolCallID     json.RawMessage   `json:"toolCallId"`
		ToolName       json.RawMessage   `json:"toolName"`
		Content        ToolResultContent `json:"content"`
		Details        json.RawMessage   `json:"details,omitempty"`
		Usage          *Usage            `json:"usage,omitempty"`
		AddedToolNames json.RawMessage   `json:"addedToolNames,omitempty"`
		IsError        bool              `json:"isError"`
		Timestamp      int64             `json:"timestamp"`
	}{
		Role:           "toolResult",
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Content:        message.Content,
		Details:        message.Details,
		Usage:          message.Usage,
		AddedToolNames: addedToolNames,
		IsError:        message.IsError,
		Timestamp:      message.Timestamp,
	})
}

func (message *ToolResultMessage) UnmarshalJSON(data []byte) error {
	var raw struct {
		ToolCallID     json.RawMessage   `json:"toolCallId"`
		ToolName       json.RawMessage   `json:"toolName"`
		Content        ToolResultContent `json:"content"`
		Details        json.RawMessage   `json:"details"`
		Usage          *Usage            `json:"usage"`
		AddedToolNames json.RawMessage   `json:"addedToolNames"`
		IsError        bool              `json:"isError"`
		Timestamp      int64             `json:"timestamp"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	toolCallID, err := unmarshalWireString(raw.ToolCallID)
	if err != nil {
		return err
	}
	toolName, err := unmarshalWireString(raw.ToolName)
	if err != nil {
		return err
	}
	addedToolNames, err := unmarshalOptionalWireStringSlice(raw.AddedToolNames)
	if err != nil {
		return err
	}
	*message = ToolResultMessage{
		ToolCallID:     toolCallID,
		ToolName:       toolName,
		Content:        raw.Content,
		Details:        bytes.Clone(raw.Details),
		Usage:          cloneUsage(raw.Usage),
		AddedToolNames: addedToolNames,
		IsError:        raw.IsError,
		Timestamp:      raw.Timestamp,
	}
	return nil
}

func cloneUsage(usage *Usage) *Usage {
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

func (info DiagnosticErrorInfo) MarshalJSON() ([]byte, error) {
	name, err := marshalOptionalWireString(info.Name)
	if err != nil {
		return nil, err
	}
	message, err := jsonwire.MarshalString(info.Message)
	if err != nil {
		return nil, err
	}
	stack, err := marshalOptionalWireString(info.Stack)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Name    json.RawMessage `json:"name,omitempty"`
		Message json.RawMessage `json:"message"`
		Stack   json.RawMessage `json:"stack,omitempty"`
		Code    json.RawMessage `json:"code,omitempty"`
	}{Name: name, Message: message, Stack: stack, Code: info.Code})
}

func (info *DiagnosticErrorInfo) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name    json.RawMessage `json:"name"`
		Message json.RawMessage `json:"message"`
		Stack   json.RawMessage `json:"stack"`
		Code    json.RawMessage `json:"code"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	name, err := unmarshalOptionalWireString(raw.Name)
	if err != nil {
		return err
	}
	message, err := unmarshalWireString(raw.Message)
	if err != nil {
		return err
	}
	stack, err := unmarshalOptionalWireString(raw.Stack)
	if err != nil {
		return err
	}
	*info = DiagnosticErrorInfo{Name: name, Message: message, Stack: stack, Code: bytes.Clone(raw.Code)}
	return nil
}

func (diagnostic AssistantMessageDiagnostic) MarshalJSON() ([]byte, error) {
	typeValue, err := jsonwire.MarshalString(diagnostic.Type)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type      json.RawMessage      `json:"type"`
		Timestamp int64                `json:"timestamp"`
		Error     *DiagnosticErrorInfo `json:"error,omitempty"`
		Details   json.RawMessage      `json:"details,omitempty"`
	}{Type: typeValue, Timestamp: diagnostic.Timestamp, Error: diagnostic.Error, Details: diagnostic.Details})
}

func (diagnostic *AssistantMessageDiagnostic) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type      json.RawMessage      `json:"type"`
		Timestamp int64                `json:"timestamp"`
		Error     *DiagnosticErrorInfo `json:"error"`
		Details   json.RawMessage      `json:"details"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	typeValue, err := unmarshalWireString(raw.Type)
	if err != nil {
		return err
	}
	*diagnostic = AssistantMessageDiagnostic{
		Type:      typeValue,
		Timestamp: raw.Timestamp,
		Error:     raw.Error,
		Details:   bytes.Clone(raw.Details),
	}
	return nil
}

func (content TextContent) MarshalJSON() ([]byte, error) {
	text, err := jsonwire.MarshalString(content.Text)
	if err != nil {
		return nil, err
	}
	signature, err := marshalOptionalWireString(content.TextSignature)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type          string          `json:"type"`
		Text          json.RawMessage `json:"text"`
		TextSignature json.RawMessage `json:"textSignature,omitempty"`
		Index         *int            `json:"index,omitempty"`
	}{Type: "text", Text: text, TextSignature: signature, Index: content.Index})
}

func (content *TextContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Text          json.RawMessage `json:"text"`
		TextSignature json.RawMessage `json:"textSignature"`
		Index         *int            `json:"index"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	text, err := unmarshalWireString(raw.Text)
	if err != nil {
		return err
	}
	signature, err := unmarshalOptionalWireString(raw.TextSignature)
	if err != nil {
		return err
	}
	*content = TextContent{Text: text, TextSignature: signature, Index: raw.Index}
	return nil
}

func (content ThinkingContent) MarshalJSON() ([]byte, error) {
	thinking, err := jsonwire.MarshalString(content.Thinking)
	if err != nil {
		return nil, err
	}
	signature, err := marshalOptionalWireString(content.ThinkingSignature)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type              string          `json:"type"`
		Thinking          json.RawMessage `json:"thinking"`
		ThinkingSignature json.RawMessage `json:"thinkingSignature,omitempty"`
		Redacted          *bool           `json:"redacted,omitempty"`
		Index             *int            `json:"index,omitempty"`
	}{
		Type:              "thinking",
		Thinking:          thinking,
		ThinkingSignature: signature,
		Redacted:          content.Redacted,
		Index:             content.Index,
	})
}

func (content *ThinkingContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Thinking          json.RawMessage `json:"thinking"`
		ThinkingSignature json.RawMessage `json:"thinkingSignature"`
		Redacted          *bool           `json:"redacted"`
		Index             *int            `json:"index"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	thinking, err := unmarshalWireString(raw.Thinking)
	if err != nil {
		return err
	}
	signature, err := unmarshalOptionalWireString(raw.ThinkingSignature)
	if err != nil {
		return err
	}
	*content = ThinkingContent{Thinking: thinking, ThinkingSignature: signature, Redacted: raw.Redacted, Index: raw.Index}
	return nil
}

func (content ImageContent) MarshalJSON() ([]byte, error) {
	data, err := jsonwire.MarshalString(content.Data)
	if err != nil {
		return nil, err
	}
	mimeType, err := jsonwire.MarshalString(content.MimeType)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type     string          `json:"type"`
		Data     json.RawMessage `json:"data"`
		MimeType json.RawMessage `json:"mimeType"`
	}{Type: "image", Data: data, MimeType: mimeType})
}

func (content *ImageContent) UnmarshalJSON(data []byte) error {
	var raw struct {
		Data     json.RawMessage `json:"data"`
		MimeType json.RawMessage `json:"mimeType"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	imageData, err := unmarshalWireString(raw.Data)
	if err != nil {
		return err
	}
	mimeType, err := unmarshalWireString(raw.MimeType)
	if err != nil {
		return err
	}
	*content = ImageContent{Data: imageData, MimeType: mimeType}
	return nil
}

func (content UnknownContentBlock) MarshalJSON() ([]byte, error) {
	if len(bytes.TrimSpace(content.Raw)) == 0 {
		return []byte("null"), nil
	}
	return NormalizeJSONStringifyJSON(content.Raw)
}

func (content ToolCall) MarshalJSON() ([]byte, error) {
	arguments, err := MarshalToolCallArguments(&content)
	if err != nil {
		return nil, err
	}
	id, err := jsonwire.MarshalString(content.ID)
	if err != nil {
		return nil, err
	}
	name, err := jsonwire.MarshalString(content.Name)
	if err != nil {
		return nil, err
	}
	thoughtSignature, err := marshalOptionalWireString(content.ThoughtSignature)
	if err != nil {
		return nil, err
	}
	partialJSON, err := marshalOptionalWireString(content.PartialJSON)
	if err != nil {
		return nil, err
	}
	partialArgs, err := marshalOptionalWireString(content.PartialArgs)
	if err != nil {
		return nil, err
	}
	if content.PartialJSON != nil || content.PartialArgs != nil || content.StreamIndex != nil || content.Index != nil {
		return marshalJSON(struct {
			Type             string          `json:"type"`
			ID               json.RawMessage `json:"id"`
			Name             json.RawMessage `json:"name"`
			Arguments        json.RawMessage `json:"arguments"`
			PartialJSON      json.RawMessage `json:"partialJson,omitempty"`
			PartialArgs      json.RawMessage `json:"partialArgs,omitempty"`
			StreamIndex      *int            `json:"streamIndex,omitempty"`
			Index            *int            `json:"index,omitempty"`
			ThoughtSignature json.RawMessage `json:"thoughtSignature,omitempty"`
		}{
			Type:             "toolCall",
			ID:               id,
			Name:             name,
			Arguments:        arguments,
			PartialJSON:      partialJSON,
			PartialArgs:      partialArgs,
			StreamIndex:      content.StreamIndex,
			Index:            content.Index,
			ThoughtSignature: thoughtSignature,
		})
	}
	return marshalJSON(struct {
		Type             string          `json:"type"`
		ID               json.RawMessage `json:"id"`
		Name             json.RawMessage `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature json.RawMessage `json:"thoughtSignature,omitempty"`
	}{
		Type:             "toolCall",
		ID:               id,
		Name:             name,
		Arguments:        arguments,
		ThoughtSignature: thoughtSignature,
	})
}

func (content *ToolCall) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID               json.RawMessage `json:"id"`
		Name             json.RawMessage `json:"name"`
		Arguments        json.RawMessage `json:"arguments"`
		ThoughtSignature json.RawMessage `json:"thoughtSignature"`
		PartialJSON      json.RawMessage `json:"partialJson"`
		PartialArgs      json.RawMessage `json:"partialArgs"`
		StreamIndex      *int            `json:"streamIndex,omitempty"`
		Index            *int            `json:"index,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	id, err := unmarshalWireString(raw.ID)
	if err != nil {
		return err
	}
	name, err := unmarshalWireString(raw.Name)
	if err != nil {
		return err
	}
	thoughtSignature, err := unmarshalOptionalWireString(raw.ThoughtSignature)
	if err != nil {
		return err
	}
	partialJSON, err := unmarshalOptionalWireString(raw.PartialJSON)
	if err != nil {
		return err
	}
	partialArgs, err := unmarshalOptionalWireString(raw.PartialArgs)
	if err != nil {
		return err
	}
	*content = ToolCall{
		ID:               id,
		Name:             name,
		ThoughtSignature: thoughtSignature,
		PartialJSON:      partialJSON,
		PartialArgs:      partialArgs,
		StreamIndex:      raw.StreamIndex,
		Index:            raw.Index,
	}
	if err := SetToolCallArgumentsJSON(content, raw.Arguments); err != nil {
		return fmt.Errorf("tool call arguments: %w", err)
	}
	return nil
}

// SetToolCallArgumentsJSON records a complete provider-emitted argument value
// so a later replay preserves JSON.stringify's original shape and member order.
// ToolCall.Arguments remains an object-oriented Go convenience; malformed
// provider values are retained in the wire representation and exposed through
// ToolCallArgumentsValue.
func SetToolCallArgumentsJSON(content *ToolCall, data []byte) error {
	if content == nil {
		return errors.New("ai: nil tool call")
	}
	normalizedArguments, err := NormalizeJSONStringifyJSON(data)
	if err != nil {
		return err
	}
	value, err := decodeJSONValue(normalizedArguments)
	if err != nil {
		return err
	}
	arguments, ok := value.(map[string]any)
	if !ok {
		arguments = map[string]any{}
	}
	content.Arguments = arguments
	content.rawArguments = normalizedArguments
	return nil
}

// ToolCallArgumentsValue returns the provider-emitted JSON value. Valid tool
// calls return the public argument map; malformed non-object values remain
// observable so schema validation and transforms see the same runtime value as
// upstream.
func ToolCallArgumentsValue(content *ToolCall) any {
	if content == nil {
		return nil
	}
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(content.rawArguments) > 0 {
		original, err := decodeJSONValue(content.rawArguments)
		if err == nil {
			if object, ok := original.(map[string]any); ok {
				if reflect.DeepEqual(object, arguments) {
					return arguments
				}
			} else if len(arguments) == 0 {
				return original
			}
		}
	}
	return arguments
}

// MarshalToolCallArguments preserves the provider's decoded JSON shape and
// object member order while the public argument map remains convenient for
// ordinary object-shaped tool calls.
func MarshalToolCallArguments(content *ToolCall) ([]byte, error) {
	if content == nil {
		return nil, errors.New("ai: nil tool call")
	}
	arguments := content.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(content.rawArguments) > 0 {
		original, err := decodeJSONValue(content.rawArguments)
		if err == nil {
			if object, ok := original.(map[string]any); ok {
				if reflect.DeepEqual(object, arguments) {
					return bytes.Clone(content.rawArguments), nil
				}
			} else if len(arguments) == 0 {
				return bytes.Clone(content.rawArguments), nil
			}
		}
	}
	for _, partial := range []*string{content.PartialJSON, content.PartialArgs} {
		if partial == nil {
			continue
		}
		encoded, err := partialjson.StringifyStreamingJSON(*partial)
		if err == nil {
			return encoded, nil
		}
	}
	return marshalJSON(stringifyJSONObject(arguments))
}

func (content UserContent) MarshalJSON() ([]byte, error) {
	if content.Text != nil {
		if content.Blocks != nil {
			return nil, errors.New("ai: user content has both text and blocks")
		}
		return jsonwire.MarshalString(*content.Text)
	}
	if content.Blocks == nil {
		return []byte("[]"), nil
	}
	return marshalJSON(content.Blocks)
}

func (content *UserContent) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '"' {
		text, err := jsonwire.UnmarshalString(data)
		if err != nil {
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

func unmarshalWireString(data json.RawMessage) (string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return "", nil
	}
	return jsonwire.UnmarshalString(data)
}

func marshalOptionalWireString(value *string) (json.RawMessage, error) {
	if value == nil {
		return nil, nil
	}
	encoded, err := jsonwire.MarshalString(*value)
	return json.RawMessage(encoded), err
}

func marshalOptionalWireStringSlice(values *[]string) (json.RawMessage, error) {
	if values == nil {
		return nil, nil
	}
	if *values == nil {
		return json.RawMessage("null"), nil
	}
	var output bytes.Buffer
	output.WriteByte('[')
	for index, value := range *values {
		if index > 0 {
			output.WriteByte(',')
		}
		encoded, err := jsonwire.MarshalString(value)
		if err != nil {
			return nil, err
		}
		output.Write(encoded)
	}
	output.WriteByte(']')
	return output.Bytes(), nil
}

func unmarshalOptionalWireStringSlice(data json.RawMessage) (*[]string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	values := make([]string, len(raw))
	for index, item := range raw {
		value, err := unmarshalWireString(item)
		if err != nil {
			return nil, err
		}
		values[index] = value
	}
	return &values, nil
}

func unmarshalOptionalWireString(data json.RawMessage) (*string, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, nil
	}
	value, err := jsonwire.UnmarshalString(data)
	if err != nil {
		return nil, err
	}
	return &value, nil
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

var textImageBlockFactories = map[string]func() any{
	"text":  func() any { return &TextContent{} },
	"image": func() any { return &ImageContent{} },
}

var assistantBlockFactories = map[string]func() any{
	"text":     func() any { return &TextContent{} },
	"thinking": func() any { return &ThinkingContent{} },
	"toolCall": func() any { return &ToolCall{} },
}

func (blocks *UserContentBlocks) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[UserContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *AssistantContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[AssistantContentBlock](data, assistantBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *ToolResultContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[ToolResultContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
}

func (blocks *ImagesContent) UnmarshalJSON(data []byte) error {
	decoded, err := unmarshalTypedBlocks[ImagesContentBlock](data, textImageBlockFactories)
	if err == nil {
		*blocks = decoded
	}
	return err
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
			normalized, err := NormalizeJSONStringifyJSON(item)
			if err != nil {
				return nil, fmt.Errorf("content %d unknown: %w", index, err)
			}
			decoded = append(decoded, &UnknownContentBlock{Raw: normalized})
			continue
		}
		value := factory()
		if err := json.Unmarshal(item, value); err != nil {
			return nil, fmt.Errorf("content %d %s: %w", index, header.Type, err)
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

func unmarshalTypedBlocks[T any](data []byte, factories map[string]func() any) ([]T, error) {
	decoded, err := unmarshalBlocks(data, factories)
	if err != nil {
		return nil, err
	}
	result := make([]T, 0, len(decoded))
	for _, block := range decoded {
		if value, ok := block.(T); ok {
			result = append(result, value)
		}
	}
	return result, nil
}

func decodeJSONValue(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, errors.New("missing JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

// NormalizeJSONStringifyJSON parses JSON with JavaScript Number semantics and
// re-emits the same value using JSON.stringify's ordering and scalar spelling.
func NormalizeJSONStringifyJSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	source := jsonStringifyDecoder{decoder: decoder, data: data}
	var output bytes.Buffer
	if err := writeJSONStringifyJSONValue(&output, &source); err != nil {
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

type jsonStringifyDecoder struct {
	decoder *json.Decoder
	data    []byte
}

func (source *jsonStringifyDecoder) token() (json.Token, error) {
	start := source.decoder.InputOffset()
	token, err := source.decoder.Token()
	if err != nil {
		return nil, err
	}
	if _, ok := token.(string); !ok {
		return token, nil
	}
	end := source.decoder.InputOffset()
	value, err := jsonwire.UnmarshalStringToken(source.data[start:end])
	if err != nil {
		return nil, err
	}
	return value, nil
}

func writeJSONStringifyJSONValue(output *bytes.Buffer, source *jsonStringifyDecoder) error {
	token, err := source.token()
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
			for source.decoder.More() {
				key, err := source.token()
				if err != nil {
					return err
				}
				_, ok := key.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				name := key.(string)
				var value bytes.Buffer
				if err := writeJSONStringifyJSONValue(&value, source); err != nil {
					return err
				}
				if index, exists := indexes[name]; exists {
					members[index].value = value.Bytes()
				} else {
					indexes[name] = len(members)
					members = append(members, member{name: name, value: value.Bytes()})
				}
			}
			closing, err := source.token()
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
				encodedName, err := jsonwire.MarshalString(member.name)
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
			for index := 0; source.decoder.More(); index++ {
				if index > 0 {
					output.WriteByte(',')
				}
				if err := writeJSONStringifyJSONValue(output, source); err != nil {
					return err
				}
			}
			closing, err := source.token()
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
	if value, ok := token.(string); ok {
		encoded, err := jsonwire.MarshalString(value)
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
