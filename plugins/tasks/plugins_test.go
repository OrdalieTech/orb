package tasks

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

type widgetUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	lines   []string
	factory extensions.ComponentFactory
	shown   int
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func (ui *widgetUI) SetWidget(_ string, widget *extensions.Widget, _ *extensions.WidgetOptions) {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	ui.lines, ui.factory = nil, nil
	if widget != nil {
		ui.lines = append([]string(nil), widget.Lines...)
		ui.factory = widget.Factory
		ui.shown++
	}
}

func (ui *widgetUI) snapshot() []string {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return append([]string(nil), ui.lines...)
}

func (ui *widgetUI) widgetFactory() extensions.ComponentFactory {
	ui.mu.Lock()
	defer ui.mu.Unlock()
	return ui.factory
}

func TestTasksToolReplacesTheLiveWidget(t *testing.T) {
	ui := &widgetUI{}
	var drawn []string
	draw := Draw{Widget: func(summary, list string, _ extensions.UIHost, _ extensions.Theme) extensions.Component {
		drawn = []string{summary, list}
		return nil
	}}
	tool := pluginTool(t, "tasks", "todo", Extension(draw), extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	result := must(tool.Execute(context.Background(), "todo-1", map[string]any{"items": []any{
		map[string]any{"text": "inspect", "status": "done"},
		map[string]any{"text": "implement", "status": "in_progress"},
	}}, nil))
	text := ai.ContentText(result.Content)
	require(t, text == "[x] inspect\n→ [ ] implement", "tool result = %q", text)
	if got, want := strings.Join(ui.snapshot(), "\n"), "✓ 1/2  → implement"; got != want {
		t.Fatalf("widget = %q, want %q", got, want)
	}
	factory := ui.widgetFactory()
	require(t, factory != nil, "a terminal's task widget is not drawn")
	factory(nil, nil)
	require(t, len(drawn) == 2 && drawn[0] == "✓ 1/2  → implement" && drawn[1] == text, "the terminal drew %q", drawn)

	result = must(tool.Execute(context.Background(), "todo-2", map[string]any{"items": []any{
		map[string]any{"text": "ship", "status": "pending"},
	}}, nil))
	got := ai.ContentText(result.Content)
	require(t, got == "[ ] ship" && strings.Join(ui.snapshot(), "\n") == "✓ 0/1  ·  +1 queued", "replacement result = %q widget = %q", got, strings.Join(ui.snapshot(), "\n"))
	details, ok := result.Details.(todoInput)
	require(t, ok && len(details.Items) == 1 && details.Items[0].Text == "ship", "result details = %#v", result.Details)
}

func TestTasksRebuildFromBranchDetails(t *testing.T) {
	manager := must(sessionstore.InMemory(t.TempDir()))
	for _, items := range []string{`[{"text":"first","status":"done"}]`, `[{"text":"second","status":"pending"}]`} {
		_ = must(manager.AppendMessage(&ai.ToolResultMessage{
			ToolName: "todo", Content: ai.ToolResultContent{&ai.TextContent{Text: "ok"}},
			Details: json.RawMessage(`{"items":` + items + `}`),
		}))
	}
	restored := todosFromBranch(manager)
	require(t, len(restored) == 1 && restored[0].Text == "second" && restored[0].Status == "pending", "restored = %#v", restored)
	require(t, todosFromBranch(nil) == nil, "nil manager returned %#v", todosFromBranch(nil))
}

func pluginTool(t *testing.T, plugin, tool string, factory extensions.Factory, runnerOptions extensions.RunnerOptions) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	if factory == nil {
		t.Fatalf("plugin %q missing", plugin)
	}
	mustOK(registry.Register("<inline:"+plugin+">", factory))
	manager := must(sessionstore.InMemory(t.TempDir()))
	runnerOptions.SessionManager = manager
	runnerOptions.Actions.GetActiveTools = func() ([]string, error) { return []string{tool}, nil }
	runner := extensions.NewRunner(registry, runnerOptions)
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == tool {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("tool %q missing", tool)
	return nil
}
