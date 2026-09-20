package extensions

import (
	"context"

	"github.com/OrdalieTech/orb/engine"
)

type registeredAgentTool struct {
	registered RegisteredTool
	runner     *Runner
}

func WrapRegisteredTool(tool RegisteredTool, runner *Runner) engine.AgentTool {
	return &registeredAgentTool{registered: tool, runner: runner}
}

func (tool *registeredAgentTool) Spec() engine.AgentToolSpec {
	definition := tool.registered.Definition
	return engine.AgentToolSpec{
		Name:                definition.Name,
		Label:               definition.Label,
		Description:         definition.Description,
		Parameters:          definition.Parameters,
		ConstrainedSampling: definition.ConstrainedSampling,
		PrepareArguments:    definition.PrepareArguments,
		ExecutionMode:       definition.ExecutionMode,
	}
}

func (tool *registeredAgentTool) Execute(
	ctx context.Context,
	toolCallID string,
	params any,
	onUpdate engine.AgentToolUpdateCallback,
) (engine.AgentToolResult, error) {
	definition := tool.registered.Definition
	return definition.Execute(ctx, toolCallID, params, onUpdate, tool.runner.CreateContext())
}
