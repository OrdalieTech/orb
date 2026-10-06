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
		// A compaction rewrites the context anyway: it takes the memory as it is now.
		api.On(extensions.EventSessionCompact, func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
			return nil, runtime.Load(ctx)
		})
		// The prompt keeps the memory the session started with; what the agent's
		// other sessions changed since joins the conversation at the next turn.
		api.On(extensions.EventBeforeAgentStart, func(ctx context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.BeforeAgentStartEvent)
			var result extensions.BeforeAgentStartResult
			if prompt := runtime.SystemPrompt(event.SystemPrompt); prompt != event.SystemPrompt {
				result.SystemPrompt = &prompt
			}
			changes, err := runtime.Changes(ctx)
			if err != nil {
				return nil, err
			}
			if changes != "" {
				result.Message = &extensions.CustomMessage{CustomType: "orb.memory", Content: changes}
			}
			if result.SystemPrompt == nil && result.Message == nil {
				return nil, nil
			}
			return result, nil
		})
		return nil
	}
}
