package toolutil

import (
	"encoding/json"
	"fmt"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func Decode(raw any, target any) error {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	return nil
}

func TextResult(text string) engine.AgentToolResult {
	return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}
}
