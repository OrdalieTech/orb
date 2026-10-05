package engine

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/jsonwire"
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

// EventSink handles one event. Parallel tool calls may invoke the same sink
// concurrently, matching upstream's overlapping event promises, so
// implementations that mutate shared state must synchronize it. Updates within
// one tool call arrive one at a time, in the order the tool produced them, and
// on a goroutine the tool does not wait for: a slow sink lags behind the tool
// rather than throttling it, up to toolUpdateQueueDepth pending updates.
type EventSink func(ctx context.Context, event AgentEvent) error

type ephemeralAgentEventKey struct{}

// IsEphemeralAgentEvent reports whether a terminal event closes recovery
// scaffolding rather than durable conversation history. A streamed response can
// emit start/update events before its malformed finish reason is known, so
// transcript stores must make their persistence decision on MessageEnd.
func IsEphemeralAgentEvent(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Value(ephemeralAgentEventKey{}).(struct{})
	return ok
}

func withEphemeralAgentEvent(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ephemeralAgentEventKey{}, struct{}{})
}

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

// Tool execution events of calls a tool made through ExecuteTool carry
// ParentToolCallID.
type ToolExecutionStartEvent struct {
	ToolCallID       string
	ToolName         string
	Args             map[string]any
	ParentToolCallID string
	toolCall         *ai.ToolCall
}

type ToolExecutionUpdateEvent struct {
	ToolCallID       string
	ToolName         string
	Args             map[string]any
	PartialResult    AgentToolResult
	ParentToolCallID string
	toolCall         *ai.ToolCall
}

type ToolExecutionEndEvent struct {
	ToolCallID       string
	ToolName         string
	Result           AgentToolResult
	IsError          bool
	ParentToolCallID string
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
	// Every event type encodes itself into compact wire JSON.
	if marshaler, ok := event.(json.Marshaler); ok {
		return marshaler.MarshalJSON()
	}
	return ai.Marshal(event)
}

// marshalMessageEvent writes {"type":kind,"message":message} from the
// message's own encoding instead of re-encoding it inside a struct.
func marshalMessageEvent(kind AgentEventType, message AgentMessage) ([]byte, error) {
	encoded, err := ai.Marshal(message)
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0, len(encoded)+len(kind)+24)
	output = append(append(append(output, `{"type":"`...), kind...), `","message":`...)
	return append(append(output, encoded...), '}'), nil
}

func (AgentStartEvent) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"agent_start"}`), nil
}

func (event AgentEndEvent) MarshalJSON() ([]byte, error) {
	output, err := appendMessageList([]byte(`{"type":"agent_end","messages":`), event.Messages)
	if err != nil {
		return nil, err
	}
	return append(output, '}'), nil
}

// appendMessageList appends messages as a JSON array (null when nil) from
// each message's own encoding.
func appendMessageList[T any](output []byte, messages []T) ([]byte, error) {
	if messages == nil {
		return append(output, "null"...), nil
	}
	output = append(output, '[')
	for index, message := range messages {
		if index > 0 {
			output = append(output, ',')
		}
		encoded, err := ai.Marshal(message)
		if err != nil {
			return nil, err
		}
		output = append(output, encoded...)
	}
	return append(output, ']'), nil
}

func (TurnStartEvent) MarshalJSON() ([]byte, error) {
	return []byte(`{"type":"turn_start"}`), nil
}

func (event TurnEndEvent) MarshalJSON() ([]byte, error) {
	output, err := marshalMessageEvent(EventTurnEnd, event.Message)
	if err != nil {
		return nil, err
	}
	output, err = appendMessageList(append(output[:len(output)-1], `,"toolResults":`...), event.ToolResults)
	if err != nil {
		return nil, err
	}
	return append(output, '}'), nil
}

func (event MessageStartEvent) MarshalJSON() ([]byte, error) {
	return marshalMessageEvent(EventMessageStart, event.Message)
}

func (event MessageUpdateEvent) MarshalJSON() ([]byte, error) {
	nested, err := ai.Marshal(event.AssistantMessageEvent)
	if err != nil {
		return nil, err
	}
	var fields struct {
		Partial json.RawMessage `json:"partial"`
	}
	if len(nested) > 0 && nested[0] == '{' {
		if err := json.Unmarshal(nested, &fields); err != nil {
			return nil, err
		}
	}
	var message json.RawMessage
	// The loop repeats the same partial in both positions of the wire event.
	if fields.Partial != nil {
		if partial, ok := assistantEventPartial(event.AssistantMessageEvent); ok && event.Message == partial {
			message = fields.Partial
		}
	}
	if message == nil {
		message, err = ai.Marshal(event.Message)
		if err != nil {
			return nil, err
		}
	}
	// Both values are already encoded with the wire escaping rules.
	encoded := make([]byte, 0, len(nested)+len(message)+64)
	encoded = append(encoded, `{"type":"message_update","assistantMessageEvent":`...)
	encoded = append(encoded, nested...)
	encoded = append(encoded, `,"message":`...)
	encoded = append(encoded, message...)
	return append(encoded, '}'), nil
}

func (event MessageEndEvent) MarshalJSON() ([]byte, error) {
	return marshalMessageEvent(EventMessageEnd, event.Message)
}

func (event ToolExecutionStartEvent) MarshalJSON() ([]byte, error) {
	output, err := appendToolExecution(`{"type":"tool_execution_start","toolCallId":`, event.ToolCallID, event.ToolName, event.toolCall, event.Args)
	if err != nil {
		return nil, err
	}
	return appendParentToolCallID(output, event.ParentToolCallID), nil
}

func (event ToolExecutionUpdateEvent) MarshalJSON() ([]byte, error) {
	output, err := appendToolExecution(`{"type":"tool_execution_update","toolCallId":`, event.ToolCallID, event.ToolName, event.toolCall, event.Args)
	if err != nil {
		return nil, err
	}
	if output, err = event.PartialResult.appendWire(append(output, `,"partialResult":`...)); err != nil {
		return nil, err
	}
	return appendParentToolCallID(output, event.ParentToolCallID), nil
}

func (event ToolExecutionEndEvent) MarshalJSON() ([]byte, error) {
	output := jsonwire.AppendString([]byte(`{"type":"tool_execution_end","toolCallId":`), event.ToolCallID)
	output, err := event.Result.appendWire(append(jsonwire.AppendString(append(output, `,"toolName":`...), event.ToolName), `,"result":`...))
	if err != nil {
		return nil, err
	}
	output = strconv.AppendBool(append(output, `,"isError":`...), event.IsError)
	return appendParentToolCallID(output, event.ParentToolCallID), nil
}

// appendToolExecution starts a tool execution event: head, call id, tool name
// and arguments.
func appendToolExecution(head, id, name string, toolCall *ai.ToolCall, fallback map[string]any) ([]byte, error) {
	args, err := marshalEventToolArguments(toolCall, fallback)
	if err != nil {
		return nil, err
	}
	output := jsonwire.AppendString([]byte(head), id)
	output = jsonwire.AppendString(append(output, `,"toolName":`...), name)
	return jsonwire.AppendCompact(append(output, `,"args":`...), args)
}

func appendParentToolCallID(output []byte, id string) []byte {
	if id != "" {
		output = jsonwire.AppendString(append(output, `,"parentToolCallId":`...), id)
	}
	return append(output, '}')
}

func (result AgentToolResult) MarshalJSON() ([]byte, error) { return result.appendWire(nil) }

// appendWire writes the members in declaration order, leaving out the empty
// optional ones.
func (result AgentToolResult) appendWire(output []byte) ([]byte, error) {
	content, err := result.Content.MarshalJSON()
	if err != nil {
		return nil, err
	}
	output = append(append(output, `{"content":`...), content...)
	for _, member := range []struct {
		name  string
		value any
		set   bool
	}{
		{`,"details":`, result.Details, result.Details != nil},
		{`,"structuredContent":`, result.StructuredContent, result.StructuredContent != nil},
		{`,"usage":`, result.Usage, result.Usage != nil},
		{`,"addedToolNames":`, result.AddedToolNames, result.AddedToolNames != nil},
		{`,"isError":`, result.IsError, result.IsError},
		{`,"terminate":`, result.Terminate, result.Terminate != nil},
	} {
		if !member.set {
			continue
		}
		encoded, err := ai.Marshal(member.value)
		if err != nil {
			return nil, err
		}
		output = append(append(output, member.name...), encoded...)
	}
	return append(output, '}'), nil
}

func marshalEventToolArguments(toolCall *ai.ToolCall, fallback map[string]any) (json.RawMessage, error) {
	if toolCall != nil {
		return ai.MarshalToolCallArguments(toolCall)
	}
	return ai.Marshal(fallback)
}
