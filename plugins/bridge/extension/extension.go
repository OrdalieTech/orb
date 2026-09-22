package extension

import (
	"context"
	"encoding/json"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/engine"
	bridgeagent "github.com/OrdalieTech/orb/plugins/bridge/agent"
)

func Extension(call func(context.Context, string, connect.Call) (json.RawMessage, error)) extensions.Factory {
	return func(api extensions.API) error {
		tool, err := bridgeagent.NewTool(call)
		if err != nil {
			return err
		}
		spec := tool.Spec()
		api.RegisterTool(extensions.ToolDefinition{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters,
			Execute: func(ctx context.Context, id string, raw any, update engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
				return tool.Execute(ctx, id, raw, update)
			},
		})
		return nil
	}
}
