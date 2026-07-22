package extensions

import (
	"encoding/json"

	"github.com/OrdalieTech/pigo/codingagent/tools"
)

// Typed accessors for the built-in tool_call/tool_result event variants.
// Upstream models these events as a discriminated union with exported type
// guards (isBashToolResult, ..., isToolCallEventType("bash", event)); Go keeps
// a single event shape, so each guard becomes an accessor that pairs the
// tool-name check with typed input/details extraction. ok reports that the
// event belongs to the tool AND the typed payload decoded; plain narrowing
// stays event.ToolName == "bash".

func toolCallInput[T any](event ToolCallEvent, name string) (T, bool) {
	var input T
	if event.ToolName != name {
		return input, false
	}
	encoded, err := json.Marshal(event.Input)
	if err != nil {
		return input, false
	}
	if err := json.Unmarshal(encoded, &input); err != nil {
		var zero T
		return zero, false
	}
	return input, true
}

func toolResultDetails[T any](event ToolResultEvent, name string) (T, bool) {
	var zero T
	if event.ToolName != name || event.Details == nil {
		return zero, false
	}
	switch details := event.Details.(type) {
	case T:
		return details, true
	case *T:
		if details == nil {
			return zero, false
		}
		return *details, true
	}
	// JavaScript extensions patch Details through JSON, leaving a decoded map;
	// recover the typed shape from the wire form.
	encoded, err := json.Marshal(event.Details)
	if err != nil {
		return zero, false
	}
	var decoded T
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return zero, false
	}
	return decoded, true
}

// BashToolCall decodes the typed input of a bash tool_call event.
func BashToolCall(event ToolCallEvent) (tools.BashToolInput, bool) {
	return toolCallInput[tools.BashToolInput](event, "bash")
}

// ReadToolCall decodes the typed input of a read tool_call event.
func ReadToolCall(event ToolCallEvent) (tools.ReadToolInput, bool) {
	return toolCallInput[tools.ReadToolInput](event, "read")
}

// EditToolCall decodes the typed input of an edit tool_call event.
func EditToolCall(event ToolCallEvent) (tools.EditToolInput, bool) {
	return toolCallInput[tools.EditToolInput](event, "edit")
}

// WriteToolCall decodes the typed input of a write tool_call event.
func WriteToolCall(event ToolCallEvent) (tools.WriteToolInput, bool) {
	return toolCallInput[tools.WriteToolInput](event, "write")
}

// GrepToolCall decodes the typed input of a grep tool_call event.
func GrepToolCall(event ToolCallEvent) (tools.GrepToolInput, bool) {
	return toolCallInput[tools.GrepToolInput](event, "grep")
}

// FindToolCall decodes the typed input of a find tool_call event.
func FindToolCall(event ToolCallEvent) (tools.FindToolInput, bool) {
	return toolCallInput[tools.FindToolInput](event, "find")
}

// LsToolCall decodes the typed input of an ls tool_call event.
func LsToolCall(event ToolCallEvent) (tools.LsToolInput, bool) {
	return toolCallInput[tools.LsToolInput](event, "ls")
}

// BashToolResult extracts the typed details of a bash tool_result event.
func BashToolResult(event ToolResultEvent) (tools.BashToolDetails, bool) {
	return toolResultDetails[tools.BashToolDetails](event, "bash")
}

// ReadToolResult extracts the typed details of a read tool_result event.
func ReadToolResult(event ToolResultEvent) (tools.ReadToolDetails, bool) {
	return toolResultDetails[tools.ReadToolDetails](event, "read")
}

// EditToolResult extracts the typed details of an edit tool_result event.
func EditToolResult(event ToolResultEvent) (tools.EditToolDetails, bool) {
	return toolResultDetails[tools.EditToolDetails](event, "edit")
}

// WriteToolResult reports whether the event is a write tool_result. The write
// tool carries no details (upstream WriteToolResultEvent.details is always
// undefined), so this accessor is the bare guard.
func WriteToolResult(event ToolResultEvent) bool {
	return event.ToolName == "write"
}

// GrepToolResult extracts the typed details of a grep tool_result event.
func GrepToolResult(event ToolResultEvent) (tools.GrepToolDetails, bool) {
	return toolResultDetails[tools.GrepToolDetails](event, "grep")
}

// FindToolResult extracts the typed details of a find tool_result event.
func FindToolResult(event ToolResultEvent) (tools.FindToolDetails, bool) {
	return toolResultDetails[tools.FindToolDetails](event, "find")
}

// LsToolResult extracts the typed details of an ls tool_result event.
func LsToolResult(event ToolResultEvent) (tools.LsToolDetails, bool) {
	return toolResultDetails[tools.LsToolDetails](event, "ls")
}
