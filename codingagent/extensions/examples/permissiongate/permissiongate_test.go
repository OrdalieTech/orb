package permissiongate

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/providers/faux"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestPermissionGateBlocksDangerousToolCallInFauxSession(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:permission-gate>", Extension); err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		CWD: t.TempDir(), SessionManager: manager, Mode: extensions.ModePrint,
	})

	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(
			faux.ToolCall("bash", map[string]any{"command": "sudo rm -rf /tmp/example"}, faux.ToolCallOptions{ID: "call-1"}),
			faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse},
		),
		faux.AssistantMessage("blocked safely"),
	})
	var executions atomic.Int64
	bash := agent.AgentToolFunc{
		AgentToolSpec: agent.AgentToolSpec{
			Name: "bash", Label: "bash", Description: "test bash",
			Parameters: jsonschema.Schema(`{"type":"object","required":["command"],"properties":{"command":{"type":"string"}}}`),
		},
		Run: func(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
			executions.Add(1)
			return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "executed"}}}, nil
		},
	}
	agentInstance := agent.NewAgent(
		agent.WithInitialState(agent.AgentState{Model: provider.GetModel(), Tools: []agent.AgentTool{bash}}),
		agent.WithStreamFn(provider.StreamSimple),
		agent.WithBeforeToolCall(func(ctx context.Context, call agent.BeforeToolCallContext) (*agent.BeforeToolCallResult, error) {
			result := runner.EmitToolCall(ctx, extensions.ToolCallEvent{
				ToolCallID: call.ToolCall.ID, ToolName: call.ToolCall.Name, Input: call.Args.(map[string]any),
			})
			if result == nil {
				return nil, nil
			}
			return &agent.BeforeToolCallResult{Block: result.Block, Reason: result.Reason}, nil
		}),
	)
	if err := agentInstance.Prompt(context.Background(), "run it"); err != nil {
		t.Fatal(err)
	}
	if executions.Load() != 0 {
		t.Fatalf("dangerous bash executed %d times", executions.Load())
	}
	state := agentInstance.State()
	var blocked *ai.ToolResultMessage
	for _, message := range state.Messages {
		if result, ok := message.(*ai.ToolResultMessage); ok {
			blocked = result
			break
		}
	}
	if blocked == nil || !blocked.IsError || len(blocked.Content) != 1 {
		t.Fatalf("blocked result = %#v", blocked)
	}
	text, ok := blocked.Content[0].(*ai.TextContent)
	if !ok || !strings.Contains(text.Text, "Dangerous command blocked (no UI for confirmation)") {
		t.Fatalf("blocked content = %#v", blocked.Content)
	}
}
