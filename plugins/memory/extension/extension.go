package extension

import (
	"context"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
	agentmemory "github.com/OrdalieTech/orb/plugins/memory/agent"
)

// Extension attaches memory to an agent using an explicit, instance-owned store.
func Extension(store memorysdk.Store) extensions.Factory {
	return func(api extensions.API) error {
		runtime, err := agentmemory.New(store)
		if err != nil {
			return err
		}
		for _, tool := range runtime.Tools() {
			spec := tool.Spec()
			api.RegisterTool(extensions.ToolDefinition{
				Name: spec.Name, Label: spec.Label, Description: spec.Description,
				Parameters: spec.Parameters, ConstrainedSampling: spec.ConstrainedSampling,
				PrepareArguments: spec.PrepareArguments, ExecutionMode: spec.ExecutionMode,
				Execute: func(ctx context.Context, callID string, raw any, onUpdate engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
					return tool.Execute(ctx, callID, raw, onUpdate)
				},
			})
		}
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			return nil, runtime.Load(ctx)
		})
		api.On(extensions.EventBeforeAgentStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.BeforeAgentStartEvent)
			prompt := runtime.SystemPrompt(event.SystemPrompt)
			if prompt == event.SystemPrompt {
				return nil, nil
			}
			return extensions.BeforeAgentStartResult{SystemPrompt: &prompt}, nil
		})
		return nil
	}
}
