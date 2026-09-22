package engine

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/conformance/runner"
)

func TestAgentEventsMarshalInUpstreamMemberOrder(t *testing.T) {
	customMessage := map[string]any{"role": "custom"}
	done := ai.DoneEvent{Reason: ai.StopReasonStop}
	emptyTools := []*ai.ToolResultMessage{}

	cases := []struct {
		name  string
		event AgentEvent
		want  string
	}{
		{"agent-start", AgentStartEvent{}, `{"type":"agent_start"}`},
		{"agent-end", AgentEndEvent{Messages: AgentMessages{}}, `{"type":"agent_end","messages":[]}`},
		{"turn-start", TurnStartEvent{}, `{"type":"turn_start"}`},
		{
			"turn-end",
			TurnEndEvent{Message: customMessage, ToolResults: emptyTools},
			`{"type":"turn_end","message":{"role":"custom"},"toolResults":[]}`,
		},
		{
			"message-start",
			MessageStartEvent{Message: customMessage},
			`{"type":"message_start","message":{"role":"custom"}}`,
		},
		{
			"message-update",
			MessageUpdateEvent{AssistantMessageEvent: done, Message: customMessage},
			`{"type":"message_update","assistantMessageEvent":{"type":"done","reason":"stop","message":null},"message":{"role":"custom"}}`,
		},
		{
			"message-end",
			MessageEndEvent{Message: customMessage},
			`{"type":"message_end","message":{"role":"custom"}}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := MarshalAgentEvent(testCase.event)
			if err != nil {
				t.Fatalf("MarshalAgentEvent: %v", err)
			}
			if diff := runner.ByteDiff([]byte(testCase.want), got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestToolEventsPreserveProviderArgumentOrderAndOptionalResultFields(t *testing.T) {
	call := &ai.ToolCall{ID: "call-1", Name: "echo"}
	if err := ai.SetToolCallArgumentsJSON(call, []byte(`{"z":1,"a":{"y":2,"x":3}}`)); err != nil {
		t.Fatalf("SetToolCallArgumentsJSON: %v", err)
	}
	emptyNames := []string{}
	explicitFalse := false
	result := AgentToolResult{
		Content:        ai.ToolResultContent{},
		Details:        json.RawMessage(`{"z":1,"a":2}`),
		AddedToolNames: &emptyNames,
		Terminate:      &explicitFalse,
	}

	cases := []struct {
		name  string
		event AgentEvent
		want  string
	}{
		{
			"start",
			NewToolExecutionStartEvent(call),
			`{"type":"tool_execution_start","toolCallId":"call-1","toolName":"echo","args":{"z":1,"a":{"y":2,"x":3}}}`,
		},
		{
			"update",
			NewToolExecutionUpdateEvent(call, result),
			`{"type":"tool_execution_update","toolCallId":"call-1","toolName":"echo","args":{"z":1,"a":{"y":2,"x":3}},"partialResult":{"content":[],"details":{"z":1,"a":2},"addedToolNames":[],"terminate":false}}`,
		},
		{
			"end",
			ToolExecutionEndEvent{ToolCallID: call.ID, ToolName: call.Name, Result: result, IsError: false},
			`{"type":"tool_execution_end","toolCallId":"call-1","toolName":"echo","result":{"content":[],"details":{"z":1,"a":2},"addedToolNames":[],"terminate":false},"isError":false}`,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := MarshalAgentEvent(testCase.event)
			if err != nil {
				t.Fatalf("MarshalAgentEvent: %v", err)
			}
			if diff := runner.ByteDiff([]byte(testCase.want), got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestAgentToolResultOmitsAbsentOptionalFields(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result AgentToolResult
		want   string
	}{
		{"nil details", AgentToolResult{Content: ai.ToolResultContent{}}, `{"content":[]}`},
		{"empty details", AgentToolResult{Content: ai.ToolResultContent{}, Details: map[string]any{}}, `{"content":[],"details":{}}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := ai.Marshal(testCase.result)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if diff := runner.ByteDiff([]byte(testCase.want), got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestMarshalAgentEventRejectsNil(t *testing.T) {
	if _, err := MarshalAgentEvent(nil); err == nil {
		t.Fatal("expected nil event error")
	}
}

func TestMessageUpdateReusesOnlyIdenticalPartial(t *testing.T) {
	tool := &ai.ToolCall{ID: "call", Name: "test"}
	if err := ai.SetToolCallArgumentsJSON(tool, []byte(`{"z":-0,"10":1,"2":2,"n":1e30,"s":"\ud800","html":"<>&"}`)); err != nil {
		t.Fatal(err)
	}
	partial := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "<>&\u2028\u2029"}, tool}}
	events := []ai.AssistantMessageEvent{
		ai.StartEvent{Partial: partial},
		ai.TextStartEvent{Partial: partial}, ai.TextDeltaEvent{Partial: partial, Delta: "x"}, ai.TextEndEvent{Partial: partial},
		ai.ThinkingStartEvent{Partial: partial}, ai.ThinkingDeltaEvent{Partial: partial}, ai.ThinkingEndEvent{Partial: partial},
		ai.ToolCallStartEvent{Partial: partial}, ai.ToolCallDeltaEvent{Partial: partial}, ai.ToolCallEndEvent{Partial: partial},
		ai.DoneEvent{Message: partial}, ai.ErrorEvent{Error: partial},
		ai.RawAssistantMessageEvent{Raw: json.RawMessage(`{"type":"future","partial":null}`), Partial: partial},
	}
	for _, event := range events {
		pointer := reflect.New(reflect.TypeOf(event))
		pointer.Elem().Set(reflect.ValueOf(event))
		nilPointer := reflect.Zero(pointer.Type()).Interface().(ai.AssistantMessageEvent)
		for _, nested := range []ai.AssistantMessageEvent{event, pointer.Interface().(ai.AssistantMessageEvent), nilPointer, nil} {
			for _, message := range []AgentMessage{partial, &ai.AssistantMessage{}, map[string]any{"role": "custom"}, (*ai.AssistantMessage)(nil), nil} {
				want, err := ai.Marshal(struct {
					Type                  AgentEventType           `json:"type"`
					AssistantMessageEvent ai.AssistantMessageEvent `json:"assistantMessageEvent"`
					Message               AgentMessage             `json:"message"`
				}{EventMessageUpdate, nested, message})
				if err != nil {
					t.Fatal(err)
				}
				got, err := MarshalAgentEvent(MessageUpdateEvent{AssistantMessageEvent: nested, Message: message})
				if err != nil {
					t.Fatal(err)
				}
				if diff := runner.ByteDiff(want, got); diff != "" {
					t.Fatalf("%T / %T: %s", nested, message, diff)
				}
			}
		}
	}
}
