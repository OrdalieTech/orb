package plugins

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/memory"
	agentmemory "github.com/OrdalieTech/orb/memory/agent"
)

// MemoryWithStore returns the dormant memory plugin with a caller-supplied,
// tenant-scoped Store.
func MemoryWithStore(store memorysdk.Store) extensions.Factory {
	if store == nil {
		return func(extensions.API) error { return fmt.Errorf("memory: store is required") }
	}
	return memoryExtension(store, "")
}

func memoryExtension(store memorysdk.Store, agentDir string) extensions.Factory {
	return func(api extensions.API) error {
		activeStore := store
		if activeStore == nil {
			dir := agentDir
			if dir == "" {
				var err error
				dir, err = config.GetAgentDir()
				if err != nil {
					return err
				}
			}
			var err error
			activeStore, err = memorysdk.NewFileStore(filepath.Join(dir, "memory"))
			if err != nil {
				return err
			}
		}
		runtime, err := agentmemory.New(activeStore)
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
