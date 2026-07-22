package host

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

func requireRuntime(t *testing.T) Runtime {
	t.Helper()
	runtime, err := DiscoverRuntime(context.Background())
	if err != nil {
		t.Skip("extension-host e2e requires Node.js >=22.6 or Bun on PATH")
	}
	return runtime
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func startFixtureManager(t *testing.T, paths ...string) (*Manager, *extensions.Registry, *extensions.Runner, LoadResult, string) {
	t.Helper()
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 30 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, paths)
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd})
	return manager, registry, runner, result, cwd
}

func TestRealHostRegistersAndExecutesToolCommandAndEvent(t *testing.T) {
	_, _, runner, result, cwd := startFixtureManager(t, fixturePath(t, "working.mjs"))
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	definition := runner.ToolDefinition("host_echo")
	if definition == nil {
		t.Fatal("host_echo was not registered")
	}
	var updates []agent.AgentToolResult
	final, err := definition.Execute(context.Background(), "call-1", map[string]any{"text": "hello"}, func(update agent.AgentToolResult) {
		updates = append(updates, update)
	}, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || toolText(updates[0]) != "partial:hello" {
		t.Fatalf("updates = %#v", updates)
	}
	if got := toolText(final); got != "final:hello" {
		t.Fatalf("final text = %q", got)
	}

	command := runner.Command("host-command")
	if command == nil {
		t.Fatal("host-command was not registered")
	}
	if err := command.Handler(context.Background(), "command-value", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(cwd, "host-command.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "command-value" {
		t.Fatalf("command output = %q", content)
	}

	eventResult := runner.EmitBeforeAgentStart(context.Background(), "prompt", nil, "system", extensions.SystemPromptOptions{})
	if eventResult == nil || eventResult.SystemPrompt == nil || *eventResult.SystemPrompt != "system host-event" {
		t.Fatalf("event result = %#v", eventResult)
	}
	if runner.ToolDefinition("host_dynamic") == nil {
		t.Fatal("tool registered after factory completion was not bound")
	}
}

func TestRealHostLoadsLocalTypeScriptEntryUnchanged(t *testing.T) {
	entry := filepath.Join(t.TempDir(), "typed.ts")
	writeFile(t, entry, `
export default function (pi: any) {
  const name: string = "typed_local";
  pi.registerTool({
    name,
    label: "Typed local",
    description: "Native TypeScript strip probe",
    parameters: { type: "object", properties: {} },
    async execute() { return { content: [{ type: "text", text: "ok" }] }; }
  });
}
`, 0o600)
	_, _, runner, result, _ := startFixtureManager(t, entry)
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	if runner.ToolDefinition("typed_local") == nil {
		t.Fatal("typed local extension was not registered")
	}
}

func TestRealHostResolvesTypeScriptPackageImports(t *testing.T) {
	root := t.TempDir()
	packageDir := filepath.Join(root, "node_modules", "typed-package")
	dependencyDir := filepath.Join(root, "node_modules", "typed-dependency")
	entry := filepath.Join(packageDir, "index.ts")
	writeFile(t, filepath.Join(packageDir, "package.json"), `{"name":"typed-package","type":"module","dependencies":{"typed-dependency":"1.0.0"}}`, 0o600)
	writeFile(t, filepath.Join(packageDir, "extensionless.ts"), `export const first: string = "typed";`, 0o600)
	writeFile(t, filepath.Join(packageDir, "js-target.ts"), `export const second: string = "package";`, 0o600)
	writeFile(t, filepath.Join(dependencyDir, "package.json"), `{"name":"typed-dependency","type":"module","exports":"./index.ts"}`, 0o600)
	writeFile(t, filepath.Join(dependencyDir, "index.ts"), `export const dependency: string = "dependency";`, 0o600)
	writeFile(t, entry, `
import { first } from "./extensionless";
import { second } from "./js-target.js";
import { dependency } from "typed-dependency";
export default function (pi: any) {
  pi.registerTool({
    name: first + "_" + second + "_" + dependency,
    label: "Typed package",
    description: import.meta.dirname,
    parameters: { type: "object", properties: {} },
    async execute() { return { content: [{ type: "text", text: "ok" }] }; }
  });
}
`, 0o600)
	_, _, runner, result, _ := startFixtureManager(t, entry)
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	if runner.ToolDefinition("typed_package_dependency") == nil {
		t.Fatal("TypeScript package imports were not resolved")
	}
	if got := runner.ToolDefinition("typed_package_dependency").Description; got != packageDir {
		t.Fatalf("tool source path = %q, want %q", got, packageDir)
	}
}

func TestRealHostIsolatesExtensionLoadErrors(t *testing.T) {
	_, _, runner, result, _ := startFixtureManager(t,
		fixturePath(t, "import-error.mjs"),
		fixturePath(t, "working.mjs"),
	)
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, "fixture import failed") {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	if runner.ToolDefinition("host_echo") == nil {
		t.Fatal("working extension did not load after import error")
	}
}

func TestRealHostRegistersAndExecutesMessageAndEntryRenderers(t *testing.T) {
	_, _, runner, result, _ := startFixtureManager(t, fixturePath(t, "renderers.mjs"))
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	shortcut := runner.Shortcuts(nil)["ctrl+alt+h"]
	if shortcut.Handler == nil {
		t.Fatal("shortcut was not registered")
	}
	if err := shortcut.Handler(context.Background(), runner.CreateContext()); err != nil {
		t.Fatal(err)
	}
	messageRenderer := runner.MessageRenderer("host-message")
	if messageRenderer == nil {
		t.Fatal("message renderer was not registered")
	}
	messageComponent := messageRenderer(extensions.CustomMessage{Content: "hello"}, extensions.MessageRenderOptions{Expanded: true}, nil)
	if messageComponent == nil {
		t.Fatal("message renderer returned no component")
	}
	if got := messageComponent.Render(72); len(got) != 1 || got[0] != "message:hello:true::72" {
		t.Fatalf("message render = %#v", got)
	}
	if disposable, ok := messageComponent.(extensions.DisposableComponent); ok {
		disposable.Dispose()
	}

	entryRenderer := runner.EntryRenderer("host-entry")
	if entryRenderer == nil {
		t.Fatal("entry renderer was not registered")
	}
	entryComponent := entryRenderer(map[string]any{"value": "world"}, extensions.EntryRenderOptions{}, nil)
	if entryComponent == nil {
		t.Fatal("entry renderer returned no component")
	}
	if got := entryComponent.Render(80); len(got) != 1 || got[0] != "entry:world:false:80" {
		t.Fatalf("entry render = %#v", got)
	}
}

func TestRealHostRestartsAfterCrashAndReregisters(t *testing.T) {
	manager, _, runner, result, _ := startFixtureManager(t, fixturePath(t, "working.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	crash := runner.ToolDefinition("host_crash")
	if crash == nil {
		t.Fatal("host_crash was not registered")
	}
	_, err := crash.Execute(context.Background(), "crash-1", map[string]any{}, nil, runner.CreateContext())
	if err == nil {
		t.Fatal("crash tool unexpectedly returned without error")
	}

	deadline := time.Now().Add(5 * time.Second)
	for manager.RestartCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if manager.RestartCount() == 0 {
		t.Fatal("manager did not restart the crashed host")
	}
	echo := runner.ToolDefinition("host_echo")
	final, err := echo.Execute(context.Background(), "call-after-restart", map[string]any{"text": "again"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if got := toolText(final); got != "final:again" {
		t.Fatalf("final text after restart = %q", got)
	}
}

func TestRealHostReloadStartsFreshProcess(t *testing.T) {
	_, registry, runner, result, cwd := startFixtureManager(t, fixturePath(t, "working.mjs"))
	if len(result.Errors) != 0 || len(result.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", result)
	}
	echo := runner.ToolDefinition("host_echo")
	first, err := echo.Execute(context.Background(), "call-1", map[string]any{"text": "one"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	second, err := echo.Execute(context.Background(), "call-2", map[string]any{"text": "two"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	firstDetails := first.Details.(map[string]any)
	secondDetails := second.Details.(map[string]any)
	if firstDetails["executions"] != float64(1) || secondDetails["executions"] != float64(2) {
		t.Fatalf("execution counts before reload = %#v, %#v", firstDetails, secondDetails)
	}
	oldPID := secondDetails["pid"]
	fresh, err := registry.Fresh(cwd)
	if err != nil {
		t.Fatal(err)
	}
	freshRunner := extensions.NewRunner(fresh, extensions.RunnerOptions{CWD: cwd})
	freshEcho := freshRunner.ToolDefinition("host_echo")
	if freshEcho == nil {
		t.Fatal("host_echo was not re-registered on fresh registry")
	}
	after, err := freshEcho.Execute(context.Background(), "call-3", map[string]any{"text": "three"}, nil, freshRunner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	afterDetails := after.Details.(map[string]any)
	if afterDetails["executions"] != float64(1) {
		t.Fatalf("execution count after reload = %#v", afterDetails)
	}
	if afterDetails["pid"] == oldPID {
		t.Fatalf("reload reused process pid %v", oldPID)
	}
}

func TestManagerWithoutRuntimeReturnsTypedDiagnostic(t *testing.T) {
	errorValue := &RuntimeUnavailableError{}
	var typed *RuntimeUnavailableError
	if !errors.As(errorValue, &typed) || typed.Diagnostic().Message != runtimeUnavailableMessage {
		t.Fatalf("runtime error = %#v", errorValue)
	}
}

func toolText(result agent.AgentToolResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	text, _ := result.Content[0].(*ai.TextContent)
	if text == nil {
		return ""
	}
	return text.Text
}
