package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"strconv"

	"github.com/OrdalieTech/orb/internal/jsonwire"
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

// The events append their members directly; each ends with its message, null
// when nil.

func (event StartEvent) MarshalJSON() ([]byte, error) {
	return appendEventMessage([]byte(`{"type":"start"`), `,"partial":`, event.Partial)
}
func (event TextStartEvent) MarshalJSON() ([]byte, error) {
	return appendEventMessage(appendEventHead("text_start", event.ContentIndex), `,"partial":`, event.Partial)
}
func (event TextDeltaEvent) MarshalJSON() ([]byte, error) {
	return marshalDeltaEvent("text_delta", event.ContentIndex, event.Delta, event.Partial)
}
func (event TextEndEvent) MarshalJSON() ([]byte, error) {
	dst := jsonwire.AppendString(append(appendEventHead("text_end", event.ContentIndex), `,"content":`...), event.Content)
	dst = appendOptionalString(dst, `,"contentSignature":`, event.ContentSignature)
	return appendEventMessage(dst, `,"partial":`, event.Partial)
}
func (event ThinkingStartEvent) MarshalJSON() ([]byte, error) {
	return appendEventMessage(appendEventHead("thinking_start", event.ContentIndex), `,"partial":`, event.Partial)
}
func (event ThinkingDeltaEvent) MarshalJSON() ([]byte, error) {
	return marshalDeltaEvent("thinking_delta", event.ContentIndex, event.Delta, event.Partial)
}
func (event ThinkingEndEvent) MarshalJSON() ([]byte, error) {
	dst := jsonwire.AppendString(append(appendEventHead("thinking_end", event.ContentIndex), `,"content":`...), event.Content)
	dst = appendOptionalString(dst, `,"contentSignature":`, event.ContentSignature)
	dst = appendOptionalBool(dst, `,"redacted":`, event.Redacted)
	return appendEventMessage(dst, `,"partial":`, event.Partial)
}
func (event ToolCallStartEvent) MarshalJSON() ([]byte, error) {
	dst := appendEventHead("toolcall_start", event.ContentIndex)
	if event.ID != "" {
		dst = jsonwire.AppendString(append(dst, `,"id":`...), event.ID)
	}
	if event.ToolName != "" {
		dst = jsonwire.AppendString(append(dst, `,"toolName":`...), event.ToolName)
	}
	return appendEventMessage(dst, `,"partial":`, event.Partial)
}
func (event ToolCallDeltaEvent) MarshalJSON() ([]byte, error) {
	return marshalDeltaEvent("toolcall_delta", event.ContentIndex, event.Delta, event.Partial)
}
func (event ToolCallEndEvent) MarshalJSON() ([]byte, error) {
	dst := append(appendEventHead("toolcall_end", event.ContentIndex), `,"toolCall":`...)
	if event.ToolCall == nil {
		dst = append(dst, "null"...)
	} else {
		var err error
		if dst, err = event.ToolCall.appendWire(dst); err != nil {
			return nil, err
		}
	}
	return appendEventMessage(dst, `,"partial":`, event.Partial)
}
func (event DoneEvent) MarshalJSON() ([]byte, error) {
	dst := jsonwire.AppendString([]byte(`{"type":"done","reason":`), string(event.Reason))
	return appendEventMessage(dst, `,"message":`, event.Message)
}
func (event ErrorEvent) MarshalJSON() ([]byte, error) {
	dst := jsonwire.AppendString([]byte(`{"type":"error","reason":`), string(event.Reason))
	return appendEventMessage(dst, `,"error":`, event.Error)
}

func appendEventHead(kind string, contentIndex int) []byte {
	dst := append(append([]byte(`{"type":"`), kind...), `","contentIndex":`...)
	return strconv.AppendInt(dst, int64(contentIndex), 10)
}

func marshalDeltaEvent(kind string, contentIndex int, delta string, partial *AssistantMessage) ([]byte, error) {
	dst := jsonwire.AppendString(append(appendEventHead(kind, contentIndex), `,"delta":`...), delta)
	return appendEventMessage(dst, `,"partial":`, partial)
}

func appendEventMessage(dst []byte, member string, message *AssistantMessage) ([]byte, error) {
	dst = append(dst, member...)
	if message == nil {
		return append(dst, "null}"...), nil
	}
	dst, err := message.appendWire(dst)
	if err != nil {
		return nil, err
	}
	return append(dst, '}'), nil
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
