// Package tool exposes independently opt-in, source-attributed mitosis tools.
package tool

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// NewTool is mitosis_call: call(peer, call) runs call on that peer's agent (agent.call).
func NewTool(call func(context.Context, string, mitosis.AgentCall) (json.RawMessage, error)) (engine.AgentTool, error) {
	if call == nil {
		return nil, errors.New("mitosis agent calls require an explicit attachment")
	}
	return engine.AgentToolFunc{AgentToolSpec: engine.AgentToolSpec{Name: "mitosis_call", Label: "Mitosis call", Description: "Call an explicitly authorized remote Orb agent, in one of its conversations (session_id) or, without one, its default conversation. Inspect first for the revision and execution targets; the answer's session_id is the conversation to address next. A durable accepted receipt does not guarantee execution. Retry with the same operation ID and unchanged payload after connection loss.", Parameters: ai.JSONSchema(`{"type":"object","properties":{"peer_id":{"type":"string"},"session_id":{"type":"string"},"call":{"type":"object","properties":{"method":{"type":"string","enum":["inspect","prompt","steer","follow_up","cancel","session.list","session.new","session.switch","session.fork","session.name"]},"expected":{"type":"object","properties":{"registration_generation":{"type":"string"},"session_revision":{"type":"string"}},"additionalProperties":false},"operation_id":{"type":"string"},"args":{"type":"object"}},"required":["method","args"],"additionalProperties":false}},"required":["peer_id","call"],"additionalProperties":false}`)}, Run: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
		var p struct {
			Peer      string            `json:"peer_id"`
			SessionID string            `json:"session_id,omitempty"`
			Call      mitosis.AgentCall `json:"call"`
		}
		if err := protocol.Decode(mitosis.JSON(raw), &p); err != nil {
			return engine.AgentToolResult{}, err
		}
		p.Call.SessionID = cmp.Or(p.SessionID, p.Call.SessionID)
		result, err := call(ctx, p.Peer, p.Call)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: string(result)}}}, nil
	}}, nil
}
