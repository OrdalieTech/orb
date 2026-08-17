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
	activeBefore, err := tool.runner.runtime.actionsSnapshot().GetActiveTools()
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	result, err := definition.Execute(ctx, toolCallID, params, onUpdate, tool.runner.CreateContext())
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	activeAfter, err := tool.runner.runtime.actionsSnapshot().GetActiveTools()
	if err != nil {
		return engine.AgentToolResult{}, err
	}
	if !isAdditive(activeBefore, activeAfter) {
		return result, nil
	}
	beforeSet := make(map[string]struct{}, len(activeBefore))
	for _, name := range activeBefore {
		beforeSet[name] = struct{}{}
	}
	activeAdded := make([]string, 0)
	for _, name := range activeAfter {
		if _, existed := beforeSet[name]; existed {
			continue
		}
		activeAdded = append(activeAdded, name)
	}
	if len(activeAdded) == 0 {
		return result, nil
	}
	combined := append([]string(nil), activeAdded...)
	if result.AddedToolNames != nil {
		combined = append(append([]string(nil), (*result.AddedToolNames)...), activeAdded...)
	}
	seen := make(map[string]struct{}, len(combined))
	added := combined[:0]
	for _, name := range combined {
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		added = append(added, name)
	}
	result.AddedToolNames = &added
	return result, nil
}

func isAdditive(before, after []string) bool {
	afterSet := make(map[string]struct{}, len(after))
	for _, name := range after {
		afterSet[name] = struct{}{}
	}
	for _, name := range before {
		if _, exists := afterSet[name]; !exists {
			return false
		}
	}
	return true
}
