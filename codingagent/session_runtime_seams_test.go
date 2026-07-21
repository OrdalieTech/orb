package codingagent

import (
	"context"
	"reflect"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/OrdalieTech/pigo/internal/jsonschema"
)

type staticComponent []string

func (component staticComponent) Render(int) []string { return component }

type seamStubTool struct{ name string }

func (tool seamStubTool) Spec() agent.AgentToolSpec {
	return agent.AgentToolSpec{Name: tool.name, Description: "stub", Parameters: jsonschema.Schema(`{"type":"object"}`)}
}

func (seamStubTool) Execute(context.Context, string, any, agent.AgentToolUpdateCallback) (agent.AgentToolResult, error) {
	return agent.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "ok"}}}, nil
}

func (seamStubTool) RenderCall(any) string { return "stub call" }
func (seamStubTool) RenderResult(agent.AgentToolResult) string {
	return "stub result"
}

func TestGetToolDefinitionKeepsCustomRenderersReachable(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:renderers>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name: "widget", Description: "renders widgets",
			Parameters: jsonschema.Schema(`{"type":"object"}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{}, nil
			},
			RenderCall: func(any, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
				return staticComponent{"custom call"}
			},
			RenderResult: func(agent.AgentToolResult, extensions.ToolRenderResultOptions, extensions.Theme, extensions.ToolRenderContext) extensions.Component {
				return staticComponent{"custom result"}
			},
		})
		api.RegisterTool(extensions.ToolDefinition{
			Name: "blocked", Description: "excluded",
			Parameters: jsonschema.Schema(`{"type":"object"}`),
			Execute: func(context.Context, string, any, agent.AgentToolUpdateCallback, extensions.Context) (agent.AgentToolResult, error) {
				return agent.AgentToolResult{}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	base := seamStubTool{name: "stub"}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, BaseTools: []agent.AgentTool{base},
		ExcludedToolNames: []string{"blocked"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	definition := runtime.GetToolDefinition("widget")
	if definition == nil || definition.RenderCall == nil || definition.RenderResult == nil {
		t.Fatalf("tool definition = %#v, want renderers reachable", definition)
	}
	call := definition.RenderCall(nil, nil, extensions.ToolRenderContext{})
	if call == nil || call.Render(80)[0] != "custom call" {
		t.Fatalf("renderCall output = %#v", call)
	}
	result := definition.RenderResult(agent.AgentToolResult{}, extensions.ToolRenderResultOptions{}, nil, extensions.ToolRenderContext{})
	if result == nil || result.Render(80)[0] != "custom result" {
		t.Fatalf("renderResult output = %#v", result)
	}
	if runtime.GetToolDefinition("blocked") != nil {
		t.Fatal("excluded tool definition must not be exposed")
	}
	if runtime.GetToolDefinition("stub") != nil {
		t.Fatal("built-in tools have no extension definition")
	}

	registered := runtime.RegisteredTool("stub")
	if registered == nil {
		t.Fatal("built-in tool not found in registry")
	}
	renderer, ok := registered.(tools.PlainTextRenderer)
	if !ok || renderer.RenderCall(nil) != "stub call" {
		t.Fatalf("built-in renderer seam = %#v", registered)
	}
	if runtime.RegisteredTool("widget") == nil {
		t.Fatal("extension tool not found in registry")
	}
	if runtime.RegisteredTool("blocked") != nil {
		t.Fatal("excluded tool must not be registered")
	}
}

func TestRegisteredToolWithoutExtensionsScansAgentTools(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	base := seamStubTool{name: "stub"}
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{
		Messages: agent.AgentMessages{}, Tools: []agent.AgentTool{base},
	}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()
	if tool := runtime.RegisteredTool("stub"); tool == nil {
		t.Fatal("base tool not found without extension state")
	}
	if tool := runtime.RegisteredTool("missing"); tool != nil {
		t.Fatalf("unknown tool = %#v, want nil", tool)
	}
}

func TestPendingMessagesSnapshotsQueueOrderAndCopies(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	var updates []QueueUpdateEvent
	runtime.Subscribe(func(event any) {
		if update, ok := event.(QueueUpdateEvent); ok {
			updates = append(updates, update)
		}
	})
	if err := runtime.Steer("first steer"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Steer("second steer"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.FollowUp("later"); err != nil {
		t.Fatal(err)
	}

	snapshot := runtime.PendingMessages()
	want := QueueUpdateEvent{Steering: []string{"first steer", "second steer"}, FollowUp: []string{"later"}}
	if !reflect.DeepEqual(snapshot, want) {
		t.Fatalf("snapshot = %#v, want %#v", snapshot, want)
	}
	if len(updates) != 3 || !reflect.DeepEqual(updates[2], want) {
		t.Fatalf("queue updates = %#v", updates)
	}
	if !reflect.DeepEqual(updates[0], QueueUpdateEvent{Steering: []string{"first steer"}, FollowUp: []string{}}) {
		t.Fatalf("first queue update = %#v", updates[0])
	}

	// Copied slices: mutating a snapshot must not leak into runtime state.
	snapshot.Steering[0] = "mutated"
	if runtime.PendingMessages().Steering[0] != "first steer" {
		t.Fatal("snapshot mutation leaked into runtime queue")
	}

	// Delivery removes the queued text and re-emits, matching upstream
	// message_start handling.
	if err := runtime.handleAgentEvent(context.Background(), agent.MessageStartEvent{Message: userMessage("first steer")}); err != nil {
		t.Fatal(err)
	}
	snapshot = runtime.PendingMessages()
	if !reflect.DeepEqual(snapshot.Steering, []string{"second steer"}) || !reflect.DeepEqual(snapshot.FollowUp, []string{"later"}) {
		t.Fatalf("post-delivery snapshot = %#v", snapshot)
	}
	if len(updates) != 4 || !reflect.DeepEqual(updates[3], snapshot) {
		t.Fatalf("post-delivery updates = %#v", updates)
	}
}

func TestInteractiveSettingsSnapshotUsesDocumentedDefaults(t *testing.T) {
	cwd := t.TempDir()
	manager, settings := extensionRuntimeDependencies(t, cwd)
	created := agent.NewAgent(agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := NewSessionRuntime(SessionRuntimeConfig{Agent: created, SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	snapshot := runtime.InteractiveSettings()
	want := InteractiveSettings{
		DoubleEscapeAction: "tree", ShowImages: true, ImageWidthCells: 60,
		EditorPaddingX: 0, AutocompleteMaxVisible: 5,
		SteeringMode: agent.QueueOneAtATime, FollowUpMode: agent.QueueOneAtATime,
	}
	if snapshot != want {
		t.Fatalf("interactive settings = %#v, want %#v", snapshot, want)
	}
}
