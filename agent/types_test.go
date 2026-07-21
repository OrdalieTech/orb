package agent

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

func TestAgentToolFuncExposesSpecAndExecutes(t *testing.T) {
	wantResult := AgentToolResult{Content: ai.ToolResultContent{}, Details: map[string]any{"ok": true}}
	tool := AgentToolFunc{
		AgentToolSpec: AgentToolSpec{
			Name:          "echo",
			Label:         "Echo",
			Description:   "echoes input",
			Parameters:    jsonschema.Schema(`{"type":"object"}`),
			ExecutionMode: ToolExecutionSequential,
		},
		Run: func(_ context.Context, id string, params any, _ AgentToolUpdateCallback) (AgentToolResult, error) {
			if id != "call-1" {
				t.Fatalf("tool call id = %q", id)
			}
			if params.(map[string]any)["value"] != "hello" {
				t.Fatalf("params = %#v", params)
			}
			return wantResult, nil
		},
	}

	spec := tool.Spec()
	if spec.Name != "echo" || spec.ExecutionMode != ToolExecutionSequential {
		t.Fatalf("spec = %#v", spec)
	}
	got, err := tool.Execute(context.Background(), "call-1", map[string]any{"value": "hello"}, nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got.Details.(map[string]any)["ok"] != true {
		t.Fatalf("result = %#v", got)
	}
}

func TestAgentToolFuncRejectsMissingRun(t *testing.T) {
	if _, err := (AgentToolFunc{}).Execute(context.Background(), "call-1", nil, nil); err == nil {
		t.Fatal("expected missing execute function error")
	}
}
