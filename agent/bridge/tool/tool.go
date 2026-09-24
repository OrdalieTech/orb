// Package tool exposes independently opt-in, source-attributed bridge tools.
package tool

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/engine"
)

func NewTool(call func(context.Context, string, bridge.Call) (json.RawMessage, error)) (engine.AgentTool, error) {
	if call == nil {
		return nil, errors.New("bridge agent calls require an explicit attachment")
	}
	return engine.AgentToolFunc{AgentToolSpec: engine.AgentToolSpec{Name: "bridge_call", Label: "Bridge call", Description: "Call an explicitly authorized remote Orb instance. Inspect first for session, revision and execution targets. A durable accepted receipt does not guarantee execution. Retry with the same operation ID and unchanged payload after connection loss.", Parameters: ai.JSONSchema(`{"type":"object","properties":{"peer_id":{"type":"string"},"call":{"type":"object","properties":{"instance_id":{"type":"string"},"service":{"type":"string","const":"orb.instance/1"},"method":{"type":"string","enum":["inspect","prompt","steer","follow_up","cancel","session.list","session.new","session.switch","session.fork"]},"session_id":{"type":"string"},"expected":{"type":"object","properties":{"registration_generation":{"type":"string"},"session_revision":{"type":"string"}},"required":["registration_generation","session_revision"],"additionalProperties":false},"operation_id":{"type":"string"},"args":{"type":"object"}},"required":["instance_id","service","method","args"],"additionalProperties":false}},"required":["peer_id","call"],"additionalProperties":false}`)}, Run: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
		var p struct {
			Peer string      `json:"peer_id"`
			Call bridge.Call `json:"call"`
		}
		if err := protocol.Decode(bridge.JSON(raw), &p); err != nil {
			return engine.AgentToolResult{}, err
		}
		if err := bridge.ValidateCall(p.Call); err != nil {
			return engine.AgentToolResult{}, err
		}
		result, err := call(ctx, p.Peer, p.Call)
		if err != nil {
			return engine.AgentToolResult{}, err
		}
		return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: string(result)}}}, nil
	}}, nil
}
