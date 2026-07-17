package agent

import (
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/conformance/runner"
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
