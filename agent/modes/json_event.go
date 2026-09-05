package modes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

// marshalJSONEvent ports upstream toJsonEvent (modes/json-event.ts): the
// stdout JSON and RPC protocols emit message_update delta-only, dropping the
// cumulative partial snapshot and the top-level message echo. message_start
// provides the initial message, deltas build it, and message_end provides the
// final authoritative message. Cumulative usage remains available because its
// size is constant.
func marshalJSONEvent(event any) ([]byte, error) {
	update, ok := event.(engine.MessageUpdateEvent)
	if !ok {
		return agent.MarshalSessionEvent(event)
	}
	message, ok := update.Message.(*ai.AssistantMessage)
	if !ok || message == nil {
		return nil, errors.New("message_update message is not an assistant message")
	}
	encoded, err := ai.MarshalAssistantMessageEvent(update.AssistantMessageEvent)
	if err != nil {
		return nil, err
	}
	delta, err := deleteObjectMember(encoded, "partial")
	if err != nil {
		return nil, err
	}
	var toolStart *ai.ToolCallStartEvent
	switch event := update.AssistantMessageEvent.(type) {
	case ai.ToolCallStartEvent:
		toolStart = &event
	case *ai.ToolCallStartEvent:
		toolStart = event
	}
	if toolStart != nil {
		var toolCall *ai.ToolCall
		if toolStart.Partial != nil && toolStart.ContentIndex >= 0 && toolStart.ContentIndex < len(toolStart.Partial.Content) {
			toolCall, _ = toolStart.Partial.Content[toolStart.ContentIndex].(*ai.ToolCall)
		}
		if toolCall == nil {
			return nil, fmt.Errorf("toolcall_start content at index %d is not a tool call", toolStart.ContentIndex)
		}
		delta, err = ai.Marshal(struct {
			Type         string `json:"type"`
			ContentIndex int    `json:"contentIndex"`
			ID           string `json:"id"`
			ToolName     string `json:"toolName"`
		}{"toolcall_start", toolStart.ContentIndex, toolCall.ID, toolCall.Name})
		if err != nil {
			return nil, err
		}
	}
	// Member order matches upstream's object literal: type, usage, assistantMessageEvent.
	return ai.Marshal(struct {
		Type                  engine.AgentEventType `json:"type"`
		Usage                 ai.Usage              `json:"usage"`
		AssistantMessageEvent json.RawMessage       `json:"assistantMessageEvent"`
	}{engine.EventMessageUpdate, message.Usage, delta})
}

// deleteObjectMember removes one member from an encoded JSON object while
// leaving every other member's bytes and order untouched.
func deleteObjectMember(object []byte, name string) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(object))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return nil, errors.New("modes: assistant message event must be an object")
	}
	var output bytes.Buffer
	output.WriteByte('{')
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		member, ok := token.(string)
		if !ok {
			return nil, errors.New("modes: assistant message event has a non-string member name")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		if member == name {
			continue
		}
		encodedName, err := ai.Marshal(member)
		if err != nil {
			return nil, err
		}
		if output.Len() > 1 {
			output.WriteByte(',')
		}
		output.Write(encodedName)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes(), nil
}
