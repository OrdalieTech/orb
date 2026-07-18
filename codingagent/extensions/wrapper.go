package extensions

import (
	"context"

	"github.com/OrdalieTech/pi-go/agent"
)

type registeredAgentTool struct {
	registered RegisteredTool
	runner     *Runner
}

func WrapRegisteredTool(tool RegisteredTool, runner *Runner) agent.AgentTool {
	return &registeredAgentTool{registered: tool, runner: runner}
}

func WrapRegisteredTools(tools []RegisteredTool, runner *Runner) []agent.AgentTool {
	wrapped := make([]agent.AgentTool, len(tools))
	for index, tool := range tools {
		wrapped[index] = WrapRegisteredTool(tool, runner)
	}
	return wrapped
}

func (tool *registeredAgentTool) Spec() agent.AgentToolSpec {
	definition := tool.registered.Definition
	return agent.AgentToolSpec{
		Name:             definition.Name,
		Label:            definition.Label,
		Description:      definition.Description,
		Parameters:       definition.Parameters,
		PrepareArguments: definition.PrepareArguments,
		ExecutionMode:    definition.ExecutionMode,
	}
}

func (tool *registeredAgentTool) Execute(
	ctx context.Context,
	toolCallID string,
	params any,
	onUpdate agent.AgentToolUpdateCallback,
) (agent.AgentToolResult, error) {
	definition := tool.registered.Definition
	activeBefore, err := tool.runner.runtime.actionsSnapshot().GetActiveTools()
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	result, err := definition.Execute(ctx, toolCallID, params, onUpdate, tool.runner.CreateContext())
	if err != nil {
		return agent.AgentToolResult{}, err
	}
	activeAfter, err := tool.runner.runtime.actionsSnapshot().GetActiveTools()
	if err != nil {
		return agent.AgentToolResult{}, err
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
