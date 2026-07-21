package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

var ErrStreamIncomplete = errors.New("ai: stream ended without a terminal event")

type AssistantMessageEventStream = iter.Seq2[AssistantMessageEvent, error]

type StreamFn func(ctx context.Context, request Request) (AssistantMessageEventStream, error)

type AssistantMessageEvent interface {
	isAssistantMessageEvent()
}

type StartEvent struct {
	Partial *AssistantMessage `json:"partial"`
}

type TextStartEvent struct {
	ContentIndex int               `json:"contentIndex"`
	Partial      *AssistantMessage `json:"partial"`
}

type TextDeltaEvent struct {
	ContentIndex int               `json:"contentIndex"`
	Delta        string            `json:"delta"`
	Partial      *AssistantMessage `json:"partial"`
}

type TextEndEvent struct {
	ContentIndex     int               `json:"contentIndex"`
	Content          string            `json:"content"`
	ContentSignature *string           `json:"contentSignature,omitempty"`
	Partial          *AssistantMessage `json:"partial"`
}

type ThinkingStartEvent struct {
	ContentIndex int               `json:"contentIndex"`
	Partial      *AssistantMessage `json:"partial"`
}

type ThinkingDeltaEvent struct {
	ContentIndex int               `json:"contentIndex"`
	Delta        string            `json:"delta"`
	Partial      *AssistantMessage `json:"partial"`
}

type ThinkingEndEvent struct {
	ContentIndex     int               `json:"contentIndex"`
	Content          string            `json:"content"`
	ContentSignature *string           `json:"contentSignature,omitempty"`
	Redacted         *bool             `json:"redacted,omitempty"`
	Partial          *AssistantMessage `json:"partial"`
}

type ToolCallStartEvent struct {
	ContentIndex int               `json:"contentIndex"`
	ID           string            `json:"id,omitempty"`
	ToolName     string            `json:"toolName,omitempty"`
	Partial      *AssistantMessage `json:"partial"`
}

type ToolCallDeltaEvent struct {
	ContentIndex int               `json:"contentIndex"`
	Delta        string            `json:"delta"`
	Partial      *AssistantMessage `json:"partial"`
}

type ToolCallEndEvent struct {
	ContentIndex int               `json:"contentIndex"`
	ToolCall     *ToolCall         `json:"toolCall"`
	Partial      *AssistantMessage `json:"partial"`
}

type DoneEvent struct {
	Reason  StopReason        `json:"reason"`
	Message *AssistantMessage `json:"message"`
}

type ErrorEvent struct {
	Reason StopReason        `json:"reason"`
	Error  *AssistantMessage `json:"error"`
}

// RawAssistantMessageEvent retains a future event shape emitted by a provider
// while attaching the partial assistant message expected by stream consumers.
type RawAssistantMessageEvent struct {
	Raw     json.RawMessage
	Partial *AssistantMessage
}

func (StartEvent) isAssistantMessageEvent()               {}
func (TextStartEvent) isAssistantMessageEvent()           {}
func (TextDeltaEvent) isAssistantMessageEvent()           {}
func (TextEndEvent) isAssistantMessageEvent()             {}
func (ThinkingStartEvent) isAssistantMessageEvent()       {}
func (ThinkingDeltaEvent) isAssistantMessageEvent()       {}
func (ThinkingEndEvent) isAssistantMessageEvent()         {}
func (ToolCallStartEvent) isAssistantMessageEvent()       {}
func (ToolCallDeltaEvent) isAssistantMessageEvent()       {}
func (ToolCallEndEvent) isAssistantMessageEvent()         {}
func (DoneEvent) isAssistantMessageEvent()                {}
func (ErrorEvent) isAssistantMessageEvent()               {}
func (RawAssistantMessageEvent) isAssistantMessageEvent() {}

func MarshalAssistantMessageEvent(event AssistantMessageEvent) ([]byte, error) {
	if event == nil {
		return nil, errors.New("ai: nil assistant message event")
	}
	return Marshal(event)
}

func UnmarshalAssistantMessageEvent(data []byte) (AssistantMessageEvent, error) {
	var header struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return nil, fmt.Errorf("ai: decode event type: %w", err)
	}
	var event AssistantMessageEvent
	switch header.Type {
	case "start":
		event = &StartEvent{}
	case "text_start":
		event = &TextStartEvent{}
	case "text_delta":
		event = &TextDeltaEvent{}
	case "text_end":
		event = &TextEndEvent{}
	case "thinking_start":
		event = &ThinkingStartEvent{}
	case "thinking_delta":
		event = &ThinkingDeltaEvent{}
	case "thinking_end":
		event = &ThinkingEndEvent{}
	case "toolcall_start":
		event = &ToolCallStartEvent{}
	case "toolcall_delta":
		event = &ToolCallDeltaEvent{}
	case "toolcall_end":
		event = &ToolCallEndEvent{}
	case "done":
		event = &DoneEvent{}
	case "error":
		event = &ErrorEvent{}
	default:
		return nil, fmt.Errorf("ai: unknown assistant message event type %q", header.Type)
	}
	if err := json.Unmarshal(data, event); err != nil {
		return nil, fmt.Errorf("ai: decode %s event: %w", header.Type, err)
	}
	switch value := event.(type) {
	case *StartEvent:
		return *value, nil
	case *TextStartEvent:
		return *value, nil
	case *TextDeltaEvent:
		return *value, nil
	case *TextEndEvent:
		return *value, nil
	case *ThinkingStartEvent:
		return *value, nil
	case *ThinkingDeltaEvent:
		return *value, nil
	case *ThinkingEndEvent:
		return *value, nil
	case *ToolCallStartEvent:
		return *value, nil
	case *ToolCallDeltaEvent:
		return *value, nil
	case *ToolCallEndEvent:
		return *value, nil
	case *DoneEvent:
		return *value, nil
	case *ErrorEvent:
		return *value, nil
	default:
		panic("unreachable event type")
	}
}

func Collect(events AssistantMessageEventStream) (*AssistantMessage, error) {
	if events == nil {
		return nil, ErrStreamIncomplete
	}
	for event, err := range events {
		if err != nil {
			return nil, err
		}
		switch terminal := event.(type) {
		case DoneEvent:
			return terminal.Message, nil
		case ErrorEvent:
			return terminal.Error, nil
		case *DoneEvent:
			return terminal.Message, nil
		case *ErrorEvent:
			return terminal.Error, nil
		}
	}
	return nil, ErrStreamIncomplete
}

func (event StartEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type    string            `json:"type"`
		Partial *AssistantMessage `json:"partial"`
	}{Type: "start", Partial: event.Partial})
}
func (event TextStartEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "text_start", ContentIndex: event.ContentIndex, Partial: event.Partial})
}
func (event TextDeltaEvent) MarshalJSON() ([]byte, error) {
	delta, err := jsonwire.MarshalString(event.Delta)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		Delta        json.RawMessage   `json:"delta"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "text_delta", ContentIndex: event.ContentIndex, Delta: delta, Partial: event.Partial})
}
func (event TextEndEvent) MarshalJSON() ([]byte, error) {
	content, err := jsonwire.MarshalString(event.Content)
	if err != nil {
		return nil, err
	}
	signature, err := marshalOptionalWireString(event.ContentSignature)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type             string            `json:"type"`
		ContentIndex     int               `json:"contentIndex"`
		Content          json.RawMessage   `json:"content"`
		ContentSignature json.RawMessage   `json:"contentSignature,omitempty"`
		Partial          *AssistantMessage `json:"partial"`
	}{
		Type: "text_end", ContentIndex: event.ContentIndex, Content: content,
		ContentSignature: signature, Partial: event.Partial,
	})
}
func (event ThinkingStartEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "thinking_start", ContentIndex: event.ContentIndex, Partial: event.Partial})
}
func (event ThinkingDeltaEvent) MarshalJSON() ([]byte, error) {
	delta, err := jsonwire.MarshalString(event.Delta)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		Delta        json.RawMessage   `json:"delta"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "thinking_delta", ContentIndex: event.ContentIndex, Delta: delta, Partial: event.Partial})
}
func (event ThinkingEndEvent) MarshalJSON() ([]byte, error) {
	content, err := jsonwire.MarshalString(event.Content)
	if err != nil {
		return nil, err
	}
	signature, err := marshalOptionalWireString(event.ContentSignature)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type             string            `json:"type"`
		ContentIndex     int               `json:"contentIndex"`
		Content          json.RawMessage   `json:"content"`
		ContentSignature json.RawMessage   `json:"contentSignature,omitempty"`
		Redacted         *bool             `json:"redacted,omitempty"`
		Partial          *AssistantMessage `json:"partial"`
	}{
		Type: "thinking_end", ContentIndex: event.ContentIndex, Content: content,
		ContentSignature: signature, Redacted: event.Redacted, Partial: event.Partial,
	})
}
func (event ToolCallStartEvent) MarshalJSON() ([]byte, error) {
	var id json.RawMessage
	if event.ID != "" {
		encoded, err := jsonwire.MarshalString(event.ID)
		if err != nil {
			return nil, err
		}
		id = encoded
	}
	var toolName json.RawMessage
	if event.ToolName != "" {
		encoded, err := jsonwire.MarshalString(event.ToolName)
		if err != nil {
			return nil, err
		}
		toolName = encoded
	}
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		ID           json.RawMessage   `json:"id,omitempty"`
		ToolName     json.RawMessage   `json:"toolName,omitempty"`
		Partial      *AssistantMessage `json:"partial"`
	}{
		Type: "toolcall_start", ContentIndex: event.ContentIndex,
		ID: id, ToolName: toolName, Partial: event.Partial,
	})
}
func (event ToolCallDeltaEvent) MarshalJSON() ([]byte, error) {
	delta, err := jsonwire.MarshalString(event.Delta)
	if err != nil {
		return nil, err
	}
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		Delta        json.RawMessage   `json:"delta"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "toolcall_delta", ContentIndex: event.ContentIndex, Delta: delta, Partial: event.Partial})
}
func (event ToolCallEndEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type         string            `json:"type"`
		ContentIndex int               `json:"contentIndex"`
		ToolCall     *ToolCall         `json:"toolCall"`
		Partial      *AssistantMessage `json:"partial"`
	}{Type: "toolcall_end", ContentIndex: event.ContentIndex, ToolCall: event.ToolCall, Partial: event.Partial})
}
func (event DoneEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type    string            `json:"type"`
		Reason  StopReason        `json:"reason"`
		Message *AssistantMessage `json:"message"`
	}{Type: "done", Reason: event.Reason, Message: event.Message})
}
func (event ErrorEvent) MarshalJSON() ([]byte, error) {
	return marshalJSON(struct {
		Type   string            `json:"type"`
		Reason StopReason        `json:"reason"`
		Error  *AssistantMessage `json:"error"`
	}{Type: "error", Reason: event.Reason, Error: event.Error})
}

func (event RawAssistantMessageEvent) MarshalJSON() ([]byte, error) {
	normalized, err := NormalizeJSONStringifyJSON(event.Raw)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(normalized))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("ai: raw assistant message event must be an object")
	}
	object := jsonwire.OrderedObject{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		name, ok := token.(string)
		if !ok {
			return nil, errors.New("ai: raw assistant message event has a non-string member name")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		object.Set(name, value)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, errors.New("ai: raw assistant message event is incomplete")
	}
	object.Set("partial", event.Partial)
	return jsonwire.Marshal(object)
}
