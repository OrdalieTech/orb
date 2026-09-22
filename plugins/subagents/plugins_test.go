package subagents

import (
	"context"
	"encoding/json"

	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/sandbox"
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

func requireError(t *testing.T, err error, want string) {
	t.Helper()
	require(t, err != nil && strings.Contains(err.Error(), want), "error = %v, want %q", err, want)
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

func (ui *widgetUI) showCount() int { ui.mu.Lock(); defer ui.mu.Unlock(); return ui.shown }

func TestSubagentCompletesInProcessWithForkedContext(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var childSawParent bool
	var returned string
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "answer", "agent": "scout", "context": "fork"}, faux.ToolCallOptions{ID: "sub-1"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			childSawParent = contextContains(request, "parent seed")
			return faux.AssistantMessage("child answer"), nil
		}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "subagent")
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	mustOK(session.PromptSync(context.Background(), "parent seed"))
	require(t, childSawParent && returned == "child answer", "childSawParent=%t tool result=%q", childSawParent, returned)
}

func TestSubagentChildOptionsUseParentRegistryForDefaultStream(t *testing.T) {
	registry := must(config.NewModelRegistry(t.TempDir()))
	options := must(childOptions(registry, nil, agent.AgentSessionOptions{}))
	require(t, options.ModelRegistry == registry && options.StreamFn == nil, "model registry=%p want=%p stream set=%t", options.ModelRegistry, registry, options.StreamFn != nil)
	_, err := childOptions(nil, nil, agent.AgentSessionOptions{})
	requireError(t, err, "parent has no model registry")
}

func TestSubagentExternalCLIConfigSchemaAndExecution(t *testing.T) {
	tool := externalSubagentTool(t, map[string]any{"zeta": "/bin/cat", "alpha": "/bin/cat"})
	wantEnum := `"enum":["scout","worker","reviewer","alpha","zeta"]`
	schema := string(tool.Spec().Parameters)
	require(t, strings.Count(schema, wantEnum) == 2, "external agent enum = %s", schema)
	result := must(tool.Execute(t.Context(), "call", map[string]any{"mode": "single", "agent": "alpha", "task": "task over stdin"}, nil))
	require(t, ai.ContentText(result.Content) == "task over stdin", "external result = %q", ai.ContentText(result.Content))

	for _, invalid := range []struct {
		name, key, want string
		value           any
	}{{"nullable enabled", "enabled", "subagents.enabled must be true or false", nil}, {"nullable external", "external", "subagents.external must map names to commands", nil}, {"built-in collision", "external", `cannot replace built-in agent "worker"`, map[string]any{"worker": "/bin/cat"}}, {"non-string command", "external", "invalid subagents settings", map[string]any{"bad": true}}, {"control in name", "external", "names must be non-empty without whitespace or control characters", map[string]any{"bad\nname": "/bin/cat"}}, {"empty command", "external", "subagents.external.bad command must not be empty", map[string]any{"bad": " "}}, {"unknown key", "externl", `unknown field "externl"`, map[string]any{"bad": "/bin/cat"}}} {
		t.Run(invalid.name, func(t *testing.T) {
			root := t.TempDir()
			settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
			settings.SetPluginSetting("subagents", invalid.key, invalid.value)
			err := extensions.NewRegistry(root).Register("<inline:subagents>", Extension(nil, nil, settings))
			require(t, err != nil && strings.Contains(err.Error(), invalid.want), "setting %s=%#v error = %v, want %q", invalid.key, invalid.value, err, invalid.want)
		})
	}
}

func TestSubagentExternalCLIStopsDescendantsAndBoundsOutput(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "cancel-marker")
	successMarker := filepath.Join(root, "success-marker")
	t.Setenv("ORB_SUBAGENT_MARKER", marker)
	t.Setenv("ORB_SUBAGENT_SUCCESS_MARKER", successMarker)
	tool := externalSubagentTool(t, map[string]any{"cancel": `(sleep 1; touch "$ORB_SUBAGENT_MARKER") & sleep 5`})
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := tool.Execute(ctx, "cancel", map[string]any{"mode": "single", "agent": "cancel", "task": "stop"}, nil)
	requireError(t, err, "deadline exceeded")
	tool = externalSubagentTool(t, map[string]any{"success": `(sleep 1; touch "$ORB_SUBAGENT_SUCCESS_MARKER") & exit 0`})
	_, err = tool.Execute(t.Context(), "success", map[string]any{"mode": "single", "agent": "success", "task": "stop"}, nil)
	require(t, err == nil, "successful background command = %v", err)
	time.Sleep(1200 * time.Millisecond)
	for _, path := range []string{marker, successMarker} {
		_, err := os.Stat(path)
		require(t, os.IsNotExist(err), "external descendant created %s: %v", path, err)
	}

	for _, stream := range []string{"stdout", "stderr"} {
		t.Run(stream, func(t *testing.T) {
			redirect := ""
			if stream == "stderr" {
				redirect = " >&2"
			}
			tool := externalSubagentTool(t, map[string]any{"noisy": `while :; do printf 0123456789abcdef0123456789abcdef` + redirect + `; done`})
			_, err := tool.Execute(t.Context(), "noisy", map[string]any{"mode": "single", "agent": "noisy", "task": "overflow"}, nil)
			requireError(t, err, stream+" exceeded the 1048576-byte limit")
		})
	}
}

func externalSubagentTool(t *testing.T, commands map[string]any) engine.AgentTool {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", commands)
	return pluginTool(t, "subagents", "subagent", Extension(nil, nil, settings), extensions.RunnerOptions{})
}

func TestSubagentParallelReturnsTwoChildResults(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var returned string
	childResponse := faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		return faux.AssistantMessage("child:" + lastUserText(request)), nil
	})
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "parallel", "tasks": []any{
			map[string]any{"task": "alpha", "agent": "worker"},
			map[string]any{"task": "beta", "agent": "reviewer"},
		}}, faux.ToolCallOptions{ID: "sub-2"})),
		childResponse,
		childResponse,
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			returned = toolResultText(request, "subagent")
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, strings.Contains(returned, "child:alpha") && strings.Contains(returned, "child:beta"), "parallel result = %q", returned)
}

func TestSubagentSurfacesChildStreamError(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	providerError := "No API key for provider: anthropic"
	var returned string
	var isError bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-error"})),
		faux.AssistantMessage(ai.AssistantContent{}, faux.AssistantMessageOptions{StopReason: ai.StopReasonError, ErrorMessage: &providerError}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			for index := len(request.Messages) - 1; index >= 0; index-- {
				if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == "subagent" {
					returned, isError = ai.ContentText(message.Content), message.IsError
					break
				}
			}
			return faux.AssistantMessage("parent done"), nil
		}),
	})
	session := newSubagentParent(t, provider)
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, isError && strings.Contains(returned, "subagent: child failed: "+providerError), "tool error=%t result=%q", isError, returned)
}

func TestSubagentInheritsPermissionsPolicy(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	childReadAbsent := false
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "inspect", "agent": "scout"}, faux.ToolCallOptions{ID: "sub-policy"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			childReadAbsent = true
			if request.Tools != nil {
				for _, tool := range *request.Tools {
					if tool.Name == "read" {
						childReadAbsent = false
					}
				}
			}
			return faux.AssistantMessage("child obeyed"), nil
		}),
		faux.AssistantMessage("parent done"),
	})
	policy := &permissions.Policy{Mode: "enforce", Rules: []permissions.Rule{{Tool: "read", Action: permissions.Deny}}}
	session := newPermissionsSession(t, provider, policy, "subagents")
	mustOK(session.PromptSync(context.Background(), "delegate"))
	require(t, childReadAbsent, "read was advertised to the child despite the inherited deny rule")
}

func TestSubagentClearsProgressWidgetAndFailsParallelRuns(t *testing.T) {
	ui := &widgetUI{}
	tool := pluginTool(t, "subagents", "subagent", Extension(nil, nil, nil), extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	// No parent model registry, so every child fails: the widget must still go.
	_, err := tool.Execute(context.Background(), "sub-1", map[string]any{"mode": "single", "task": "work"}, nil)
	require(t, err != nil, "child without a model registry succeeded")
	require(t, ui.showCount() > 0, "progress widget was never shown")
	require(t, len(ui.snapshot()) == 0, "progress widget left on screen: %v", ui.snapshot())

	_, err = tool.Execute(context.Background(), "sub-2", map[string]any{"mode": "parallel", "tasks": []any{
		map[string]any{"task": "alpha"}, map[string]any{"task": "beta"},
	}}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "2 of 2 children failed") && strings.Contains(err.Error(), "[2] worker"), "parallel failure = %v", err)
	require(t, len(ui.snapshot()) == 0, "progress widget left on screen: %v", ui.snapshot())
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

func newPermissionsSession(t *testing.T, provider *faux.Provider, policy *permissions.Policy, enabled ...string) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	manager := must(sessionstore.InMemory(root))
	registry := extensions.NewRegistry(root)
	catalog := map[string]extensions.Factory{"permissions": permissions.Extension(policy, nil, nil), "subagents": Extension(provider.StreamSimple, policy, nil)}
	for _, name := range append([]string{"permissions"}, enabled...) {
		mustOK(registry.Register("<inline:"+name+">", catalog[name]))
	}
	prompt := "permissions test"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: filepath.Join(root, "agent"), Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func newSubagentParent(t *testing.T, provider *faux.Provider) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(root+"/agent")))
	manager := must(sessionstore.InMemory(root))
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:subagents>", Extension(provider.StreamSimple, nil, nil)))
	prompt := "parent"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: root + "/agent", Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func contextContains(request ai.Context, needle string) bool {
	encoded, _ := json.Marshal(request.Messages)
	return strings.Contains(string(encoded), needle)
}

func lastUserText(request ai.Context) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.UserMessage); ok {
			return ai.ContentText(message.Content.Blocks)
		}
	}
	return ""
}

func toolResultText(request ai.Context, name string) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == name {
			return ai.ContentText(message.Content)
		}
	}
	return ""
}

// Dispose while a fan-out is in flight used to panic the host: children read the
// parent extensions.Context from their own goroutines, and every accessor panics
// once the session is torn down.
func TestSubagentSurvivesDisposeMidFanOut(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(10)})
	parent := newSubagentParent(t, provider)
	started := make(chan struct{}, 8)
	steps := []faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "parallel", "tasks": []any{
			map[string]any{"task": "a"}, map[string]any{"task": "b"},
			map[string]any{"task": "c"}, map[string]any{"task": "d"},
			map[string]any{"task": "e"}, map[string]any{"task": "f"},
		}})),
	}
	for index := 0; index < 6; index++ {
		steps = append(steps, faux.Factory(func(ctx context.Context, _ ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	}
	steps = append(steps, faux.AssistantMessage("parent done"))
	provider.SetResponses(steps)

	go func() { _ = parent.PromptSync(context.Background(), "fan out") }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("no child started")
	}
	time.Sleep(200 * time.Millisecond)
	parent.Dispose()
	// Late children keep touching the stale context after Dispose returns.
	time.Sleep(500 * time.Millisecond)
}

// tasks is model-controlled, and every entry costs a goroutine, a temp dir, a
// session, and a provider call, so the width is capped in Execute and not only
// by the schema a host may or may not enforce.
func TestSubagentParallelWidthIsCapped(t *testing.T) {
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(10)})
	root := t.TempDir()
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:subagents>", Extension(provider.StreamSimple, nil, nil)))
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{Mode: extensions.ModeTUI})
	definition := runner.ToolDefinition("subagent")
	require(t, definition != nil, "subagent tool missing")
	tasks := make([]any, maxParallelTasks+1)
	for index := range tasks {
		tasks[index] = map[string]any{"task": "t"}
	}
	_, err := definition.Execute(
		context.Background(), "call", map[string]any{"mode": "parallel", "tasks": tasks},
		nil, runner.CreateContext(),
	)
	requireError(t, err, "at most")
	require(t, provider.State().CallCount == 0, "an over-wide fan-out reached the provider %d times", provider.State().CallCount)
}

func TestSubagentExternalObjectFormTogglesWithoutLosingCommands(t *testing.T) {
	root := t.TempDir()
	settings := must(config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent"))))
	settings.SetPluginSetting("subagents", "external", map[string]any{
		"claude": map[string]any{"command": "/bin/cat", "enabled": false},
		"codex":  "/bin/cat",
	})
	entries, err := ExternalEntries(settings)
	require(t, err == nil && len(entries) == 2, "entries = %#v, %v", entries, err)
	require(t, !entries["claude"].Enabled && entries["claude"].Command == "/bin/cat", "claude = %#v", entries["claude"])
	require(t, entries["codex"].Enabled, "codex = %#v", entries["codex"])
	enabled, err := externalSubagents(settings)
	require(t, err == nil && len(enabled) == 1 && enabled["codex"] == "/bin/cat", "enabled = %#v, %v", enabled, err)
	settings.SetPluginSetting("subagents", "external", map[string]any{
		"bad": map[string]any{"command": "x", "typo": true},
	})
	_, err = ExternalEntries(settings)
	require(t, err != nil && strings.Contains(err.Error(), "must be a command or {command, enabled}"), "error = %v", err)
}

func TestSubagentInheritsFileContainment(t *testing.T) {
	root := t.TempDir()
	scratch := filepath.Join(root, "scratch")
	mustOK(os.Mkdir(scratch, 0700))
	t.Setenv("TMPDIR", scratch)
	marker := filepath.Join(root, "outside")
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var denied bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("subagent", map[string]any{"mode": "single", "task": "write", "agent": "worker"})),
		faux.AssistantMessage(faux.ToolCall("write", map[string]any{"path": marker, "content": "blocked"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			denied = strings.Contains(toolResultText(request, "write"), "sandbox:")
			return faux.AssistantMessage("child done"), nil
		}),
		faux.AssistantMessage("parent done"),
	})
	policy := &permissions.Policy{Mode: "log", Sandbox: sandbox.ModeReadOnly}
	s := newPermissionsSession(t, provider, policy, "subagents")
	mustOK(s.PromptSync(t.Context(), "delegate"))
	if !denied {
		t.Fatal("child did not inherit filesystem containment")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("child wrote outside: %v", err)
	}
}
