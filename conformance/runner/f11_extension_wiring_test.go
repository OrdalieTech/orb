package runner_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/permissiongate"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/pirate"
	"github.com/OrdalieTech/pi-go/codingagent/extensions/examples/statusline"
	"github.com/OrdalieTech/pi-go/conformance/runner"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
)

func TestF11ExtensionWiringMatchesUpstream(t *testing.T) {
	manifest := runner.LoadManifest(t, "F11-wire")
	if manifest.Family != "F11-wire" || manifest.Generator != "conformance/extract/f11-extension-wiring.ts" {
		t.Fatalf("unexpected F11-wire manifest: %+v", manifest)
	}
	var expected json.RawMessage
	runner.LoadJSON(t, "F11-wire", "cases.json", &expected)
	actual, err := json.Marshal(runF11WiringCases(t))
	if err != nil {
		t.Fatal(err)
	}
	want, err := runner.CanonicalJSON(expected)
	if err != nil {
		t.Fatal(err)
	}
	got, err := runner.CanonicalJSON(actual)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff(want, got); diff != "" {
		t.Fatal(diff)
	}
}

func runF11WiringCases(t *testing.T) map[string]any {
	t.Helper()
	ctx := context.Background()

	permissionRegistry := extensions.NewRegistry("/fixture")
	if err := permissionRegistry.Register("<inline:permission-gate>", permissiongate.Extension); err != nil {
		t.Fatal(err)
	}
	permission := extensions.NewRunner(permissionRegistry, extensions.RunnerOptions{Mode: extensions.ModePrint}).EmitToolCall(ctx, extensions.ToolCallEvent{
		ToolCallID: "danger", ToolName: "bash", Input: map[string]any{"command": "sudo true"},
	})

	pirateRegistry := extensions.NewRegistry("/fixture")
	if err := pirateRegistry.Register("<inline:pirate>", pirate.Extension); err != nil {
		t.Fatal(err)
	}
	pirateRunner := extensions.NewRunner(pirateRegistry, extensions.RunnerOptions{Mode: extensions.ModePrint})
	if !pirateRunner.ExecuteCommand(ctx, "pirate", "") {
		t.Fatal("pirate command was not registered")
	}
	pirateResult := pirateRunner.EmitBeforeAgentStart(ctx, "go", nil, "base", extensions.SystemPromptOptions{CWD: "/fixture"})

	statusRegistry := extensions.NewRegistry("/fixture")
	if err := statusRegistry.Register("<inline:status-line>", statusline.Extension); err != nil {
		t.Fatal(err)
	}
	statusUI := &f11StatusUI{}
	statusRunner := extensions.NewRunner(statusRegistry, extensions.RunnerOptions{Mode: extensions.ModeRPC, UI: statusUI})
	statusRunner.Emit(ctx, extensions.SessionStartEvent{Reason: extensions.SessionStartStartup})
	statusRunner.Emit(ctx, extensions.TurnStartEvent{TurnIndex: 0, Timestamp: 1})
	statusRunner.Emit(ctx, extensions.TurnEndEvent{TurnIndex: 0})

	return map[string]any{
		"permissionGate": permission,
		"pirate":         map[string]any{"systemPrompt": *pirateResult.SystemPrompt},
		"statusLine":     statusUI.calls,
		"deferredTools": map[string]any{
			"additive": f11WrappedToolResult(t, []string{"loader"}, []string{"loader", "late"}, []string{"existing", "existing"}),
			"noChange": f11WrappedToolResult(t, []string{"loader"}, []string{"loader"}, []string{"duplicate", "duplicate"}),
			"removal":  f11WrappedToolResult(t, []string{"loader", "old"}, []string{"loader", "late"}, []string{"existing"}),
		},
	}
}

type f11StatusUI struct {
	extensions.NoopUI
	calls []map[string]any
}

func (ui *f11StatusUI) SetStatus(key string, value *string) {
	var resolved any
	if value != nil {
		resolved = *value
	}
	ui.calls = append(ui.calls, map[string]any{"key": key, "value": resolved})
}

func f11WrappedToolResult(t *testing.T, before, after, added []string) map[string]any {
	t.Helper()
	registry := extensions.NewRegistry("/fixture")
	if err := registry.Register("<inline:tool>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name: "loader", Label: "loader", Description: "loader", Parameters: jsonschema.Schema(`{}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				names := append([]string(nil), added...)
				return agent.AgentToolResult{Content: ai.ToolResultContent{}, AddedToolNames: &names}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	call := 0
	runnerValue := extensions.NewRunner(registry, extensions.RunnerOptions{Actions: extensions.Actions{
		GetActiveTools: func() ([]string, error) {
			call++
			if call == 1 {
				return append([]string(nil), before...), nil
			}
			return append([]string(nil), after...), nil
		},
	}})
	result, err := extensions.WrapRegisteredTool(runnerValue.AllRegisteredTools()[0], runnerValue).Execute(context.Background(), "call", map[string]any{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"content": result.Content, "addedToolNames": *result.AddedToolNames}
}
