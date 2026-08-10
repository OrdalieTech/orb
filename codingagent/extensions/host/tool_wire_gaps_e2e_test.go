package host

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent/extensions"
	"github.com/OrdalieTech/orb/internal/jsonschema"
)

// TestRealHostToolWireGapClosures covers the register_tool wire gaps from the
// dossier risk list: a lazy JS `get promptGuidelines()` is re-read live
// instead of frozen at registration, renderCall/renderResult pass through as
// live components, the JSON-Schema parameters cross intact for the
// validate-before-execute gate, and `terminate: true` survives the result
// decode.
func TestRealHostToolWireGapClosures(t *testing.T) {
	_, _, runner, result, _ := startFixtureManager(t, fixturePath(t, "tool_gaps.mjs"))
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("gap_tool")
	if definition == nil {
		t.Fatal("gap_tool was not registered")
	}

	// Registration captured a frozen snapshot of the getter.
	if len(definition.PromptGuidelines) != 1 || !strings.HasPrefix(definition.PromptGuidelines[0], "guideline-read-") {
		t.Fatalf("snapshot guidelines = %#v", definition.PromptGuidelines)
	}
	if definition.PromptGuidelinesFunc == nil {
		t.Fatal("lazy promptGuidelines getter did not produce a PromptGuidelinesFunc")
	}
	first, err := definition.PromptGuidelinesFunc(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := definition.PromptGuidelinesFunc(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || len(second) != 1 || first[0] == second[0] || !strings.HasPrefix(second[0], "guideline-read-") {
		t.Fatalf("lazy guidelines not re-read live: first %#v second %#v", first, second)
	}
	if first[0] == definition.PromptGuidelines[0] {
		t.Fatalf("lazy read returned the frozen snapshot: %#v", first)
	}

	// renderCall passthrough: component created host-side, rendered over the
	// renderer-component RPC.
	if definition.RenderCall == nil {
		t.Fatal("renderCall did not pass through the wire")
	}
	callComponent := definition.RenderCall(map[string]any{"key": "k"}, nil, extensions.ToolRenderContext{ToolCallID: "call-9"})
	if callComponent == nil {
		t.Fatal("renderCall returned no component")
	}
	if lines := callComponent.Render(40); !reflect.DeepEqual(lines, []string{"call:k:40:call-9"}) {
		t.Fatalf("renderCall lines = %#v", lines)
	}

	// renderResult passthrough with options.
	if definition.RenderResult == nil {
		t.Fatal("renderResult did not pass through the wire")
	}
	toolResult := agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "out"}}}
	resultComponent := definition.RenderResult(toolResult, extensions.ToolRenderResultOptions{Expanded: true}, nil, extensions.ToolRenderContext{})
	if resultComponent == nil {
		t.Fatal("renderResult returned no component")
	}
	if lines := resultComponent.Render(20); !reflect.DeepEqual(lines, []string{"result:out:true:20"}) {
		t.Fatalf("renderResult lines = %#v", lines)
	}

	// The JSON-Schema parameters crossed intact: the agent loop's
	// validate-before-execute gate refuses schema-invalid arguments.
	if _, err := jsonschema.ValidateToolArgumentsJSON("gap_tool", jsonschema.Schema(definition.Parameters), []byte(`{}`)); err == nil {
		t.Fatal("wire-crossed schema accepted arguments missing a required property")
	}

	// terminate: true survives the execute_tool result decode.
	final, err := definition.Execute(context.Background(), "call-1", map[string]any{"key": "k"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if final.Terminate == nil || !*final.Terminate {
		t.Fatalf("terminate lost on the wire: %#v", final)
	}
}
