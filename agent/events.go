package agent

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/OrdalieTech/pigo/ai"
)

type AgentEventType string

const (
	EventAgentStart          AgentEventType = "agent_start"
	EventAgentEnd            AgentEventType = "agent_end"
	EventTurnStart           AgentEventType = "turn_start"
	EventTurnEnd             AgentEventType = "turn_end"
	EventMessageStart        AgentEventType = "message_start"
	EventMessageUpdate       AgentEventType = "message_update"
	EventMessageEnd          AgentEventType = "message_end"
	EventToolExecutionStart  AgentEventType = "tool_execution_start"
	EventToolExecutionUpdate AgentEventType = "tool_execution_update"
	EventToolExecutionEnd    AgentEventType = "tool_execution_end"
)

type AgentEvent interface {
	Type() AgentEventType
	isAgentEvent()
}

// EventSink handles one event. Parallel tool calls and successive tool updates
// may invoke the same sink concurrently, matching upstream's overlapping event
// promises, so implementations that mutate shared state must synchronize it.
type EventSink func(ctx context.Context, event AgentEvent) error

type AgentStartEvent struct{}

type AgentEndEvent struct {
	Messages AgentMessages
}

type TurnStartEvent struct{}

type TurnEndEvent struct {
	Message     AgentMessage
	ToolResults []*ai.ToolResultMessage
}

type MessageStartEvent struct {
	Message AgentMessage
}

type MessageUpdateEvent struct {
	// Upstream constructs this literal with assistantMessageEvent before message.
	AssistantMessageEvent ai.AssistantMessageEvent
	Message               AgentMessage
}

type MessageEndEvent struct {
	Message AgentMessage
}

type ToolExecutionStartEvent struct {
	ToolCallID string
	ToolName   string
	Args       map[string]any
	toolCall   *ai.ToolCall
}

type ToolExecutionUpdateEvent struct {
	ToolCallID    string
	ToolName      string
	Args          map[string]any
	PartialResult AgentToolResult
	toolCall      *ai.ToolCall
}

type ToolExecutionEndEvent struct {
	ToolCallID string
	ToolName   string
	Result     AgentToolResult
	IsError    bool
}

func NewToolExecutionStartEvent(toolCall *ai.ToolCall) ToolExecutionStartEvent {
	if toolCall == nil {
		return ToolExecutionStartEvent{}
	}
	return ToolExecutionStartEvent{
		ToolCallID: toolCall.ID,
		ToolName:   toolCall.Name,
		Args:       toolCall.Arguments,
		toolCall:   toolCall,
	}
}

func NewToolExecutionUpdateEvent(toolCall *ai.ToolCall, partial AgentToolResult) ToolExecutionUpdateEvent {
	if toolCall == nil {
		return ToolExecutionUpdateEvent{PartialResult: partial}
	}
	return ToolExecutionUpdateEvent{
		ToolCallID:    toolCall.ID,
		ToolName:      toolCall.Name,
		Args:          toolCall.Arguments,
		PartialResult: partial,
		toolCall:      toolCall,
	}
}

func (AgentStartEvent) Type() AgentEventType          { return EventAgentStart }
func (AgentEndEvent) Type() AgentEventType            { return EventAgentEnd }
func (TurnStartEvent) Type() AgentEventType           { return EventTurnStart }
func (TurnEndEvent) Type() AgentEventType             { return EventTurnEnd }
func (MessageStartEvent) Type() AgentEventType        { return EventMessageStart }
func (MessageUpdateEvent) Type() AgentEventType       { return EventMessageUpdate }
func (MessageEndEvent) Type() AgentEventType          { return EventMessageEnd }
func (ToolExecutionStartEvent) Type() AgentEventType  { return EventToolExecutionStart }
func (ToolExecutionUpdateEvent) Type() AgentEventType { return EventToolExecutionUpdate }
func (ToolExecutionEndEvent) Type() AgentEventType    { return EventToolExecutionEnd }

func (AgentStartEvent) isAgentEvent()          {}
func (AgentEndEvent) isAgentEvent()            {}
func (TurnStartEvent) isAgentEvent()           {}
func (TurnEndEvent) isAgentEvent()             {}
func (MessageStartEvent) isAgentEvent()        {}
func (MessageUpdateEvent) isAgentEvent()       {}
func (MessageEndEvent) isAgentEvent()          {}
func (ToolExecutionStartEvent) isAgentEvent()  {}
func (ToolExecutionUpdateEvent) isAgentEvent() {}
func (ToolExecutionEndEvent) isAgentEvent()    {}

func MarshalAgentEvent(event AgentEvent) ([]byte, error) {
	if event == nil {
		return nil, errors.New("agent: nil event")
	}
	return ai.Marshal(event)
}

func (AgentStartEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type AgentEventType `json:"type"`
	}{Type: EventAgentStart})
}

func (event AgentEndEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type     AgentEventType `json:"type"`
		Messages AgentMessages  `json:"messages"`
	}{Type: EventAgentEnd, Messages: event.Messages})
}

func (TurnStartEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type AgentEventType `json:"type"`
	}{Type: EventTurnStart})
}

func (event TurnEndEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type        AgentEventType          `json:"type"`
		Message     AgentMessage            `json:"message"`
		ToolResults []*ai.ToolResultMessage `json:"toolResults"`
	}{Type: EventTurnEnd, Message: event.Message, ToolResults: event.ToolResults})
}

func (event MessageStartEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type    AgentEventType `json:"type"`
		Message AgentMessage   `json:"message"`
	}{Type: EventMessageStart, Message: event.Message})
}

func (event MessageUpdateEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type                  AgentEventType           `json:"type"`
		AssistantMessageEvent ai.AssistantMessageEvent `json:"assistantMessageEvent"`
		Message               AgentMessage             `json:"message"`
	}{
		Type:                  EventMessageUpdate,
		AssistantMessageEvent: event.AssistantMessageEvent,
		Message:               event.Message,
	})
}

func (event MessageEndEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type    AgentEventType `json:"type"`
		Message AgentMessage   `json:"message"`
	}{Type: EventMessageEnd, Message: event.Message})
}

func (event ToolExecutionStartEvent) MarshalJSON() ([]byte, error) {
	args, err := marshalEventToolArguments(event.toolCall, event.Args)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Type       AgentEventType  `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Args       json.RawMessage `json:"args"`
	}{
		Type:       EventToolExecutionStart,
		ToolCallID: event.ToolCallID,
		ToolName:   event.ToolName,
		Args:       args,
	})
}

func (event ToolExecutionUpdateEvent) MarshalJSON() ([]byte, error) {
	args, err := marshalEventToolArguments(event.toolCall, event.Args)
	if err != nil {
		return nil, err
	}
	return ai.Marshal(struct {
		Type          AgentEventType  `json:"type"`
		ToolCallID    string          `json:"toolCallId"`
		ToolName      string          `json:"toolName"`
		Args          json.RawMessage `json:"args"`
		PartialResult AgentToolResult `json:"partialResult"`
	}{
		Type:          EventToolExecutionUpdate,
		ToolCallID:    event.ToolCallID,
		ToolName:      event.ToolName,
		Args:          args,
		PartialResult: event.PartialResult,
	})
}

func (event ToolExecutionEndEvent) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Type       AgentEventType  `json:"type"`
		ToolCallID string          `json:"toolCallId"`
		ToolName   string          `json:"toolName"`
		Result     AgentToolResult `json:"result"`
		IsError    bool            `json:"isError"`
	}{
		Type:       EventToolExecutionEnd,
		ToolCallID: event.ToolCallID,
		ToolName:   event.ToolName,
		Result:     event.Result,
		IsError:    event.IsError,
	})
}

func (result AgentToolResult) MarshalJSON() ([]byte, error) {
	return ai.Marshal(struct {
		Content        ai.ToolResultContent `json:"content"`
		Details        any                  `json:"details,omitempty"`
		Usage          *ai.Usage            `json:"usage,omitempty"`
		AddedToolNames *[]string            `json:"addedToolNames,omitempty"`
		Terminate      *bool                `json:"terminate,omitempty"`
	}{
		Content:        result.Content,
		Details:        result.Details,
		Usage:          result.Usage,
		AddedToolNames: result.AddedToolNames,
		Terminate:      result.Terminate,
	})
}

func marshalEventToolArguments(toolCall *ai.ToolCall, fallback map[string]any) (json.RawMessage, error) {
	if toolCall != nil {
		return ai.MarshalToolCallArguments(toolCall)
	}
	return ai.Marshal(fallback)
}
