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
	"github.com/OrdalieTech/orb/tui"
)

type widgetUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	lines   []string
	factory extensions.ComponentFactory
	shown   int
}

type taskWidgetHost struct{ invalidations int }

type dimTaskTheme struct{}

type countingTaskTheme struct{ calls int }

type blockingTaskTheme struct {
	calls   int
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (theme *countingTaskTheme) FG(_ string, text string) string { theme.calls++; return text }

func (theme *blockingTaskTheme) FG(_ string, text string) string {
	theme.calls++
	theme.once.Do(func() {
		close(theme.started)
		<-theme.release
	})
	return text
}

func (dimTaskTheme) FG(color, text string) string {
	if color == "dim" {
		return "\x1b[2m" + text + "\x1b[22m"
	}
	return text
}

func (*taskWidgetHost) Width() int { return 80 }

func (*taskWidgetHost) Height() int { return 24 }

func (host *taskWidgetHost) Invalidate() { host.invalidations++ }

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
	tool := pluginTool(t, "tasks", "todo", Extension(), extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
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
	require(t, factory != nil, "TUI task widget has no click renderer")
	host := &taskWidgetHost{}
	component := factory(host, nil)
	mouse, ok := component.(tui.MouseHandler)
	require(t, ok && mouse.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1}), "task widget did not accept a left click")
	expanded := strings.Join(component.Render(80), "\n")
	require(t, strings.Contains(expanded, "[x] inspect") && strings.Contains(expanded, "→ [ ] implement") && host.invalidations == 1, "expanded task widget = %q, invalidations = %d", expanded, host.invalidations)
	for width := 1; width <= 4; width++ {
		for _, line := range component.Render(width) {
			if got := tui.VisibleWidth(line); got > width {
				t.Fatalf("task widget width %d rendered %d cells: %q", width, got, line)
			}
		}
	}
	styled := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}, {Text: "implement", Status: "in_progress"}}, host, dimTaskTheme{})
	styled.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	styledLines := styled.Render(80)
	require(t, len(styledLines) >= 3 && strings.Contains(strings.Join(styledLines, "\n"), "\x1b[2m") && strings.HasPrefix(styledLines[1], "    "), "styled expanded task widget = %#v", styledLines)

	result = must(tool.Execute(context.Background(), "todo-2", map[string]any{"items": []any{
		map[string]any{"text": "ship", "status": "pending"},
	}}, nil))
	got := ai.ContentText(result.Content)
	require(t, got == "[ ] ship" && strings.Join(ui.snapshot(), "\n") == "✓ 0/1  ·  +1 queued", "replacement result = %q widget = %q", got, strings.Join(ui.snapshot(), "\n"))
	details, ok := result.Details.(todoInput)
	require(t, ok && len(details.Items) == 1 && details.Items[0].Text == "ship", "result details = %#v", result.Details)
}

func TestTaskWidgetCachesStableRenders(t *testing.T) {
	theme := &countingTaskTheme{}
	widget := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}, {Text: "implement", Status: "in_progress"}}, nil, theme)
	widget.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	first := widget.Render(80)
	calls := theme.calls
	second := widget.Render(80)
	require(t, strings.Join(first, "\n") == strings.Join(second, "\n"), "cached render changed: %#v != %#v", first, second)
	require(t, theme.calls == calls, "stable render restyled tasks: calls %d -> %d", calls, theme.calls)
	widget.Render(40)
	require(t, theme.calls != calls, "width change reused a stale task render")
	calls = theme.calls
	widget.Invalidate()
	widget.Render(40)
	require(t, theme.calls != calls, "invalidation reused stale themed task lines")
}

func TestTaskWidgetInvalidationWinsConcurrentRender(t *testing.T) {
	theme := &blockingTaskTheme{started: make(chan struct{}), release: make(chan struct{})}
	widget := newTaskWidget([]todoItem{{Text: "inspect", Status: "done"}}, nil, theme)
	widget.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Button: 0, Clicks: 1})
	done := make(chan struct{})
	go func() {
		widget.Render(80)
		close(done)
	}()
	<-theme.started
	widget.Invalidate()
	close(theme.release)
	<-done
	calls := theme.calls
	widget.Render(80)
	require(t, theme.calls != calls, "concurrent invalidation allowed stale task lines back into the cache")
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
