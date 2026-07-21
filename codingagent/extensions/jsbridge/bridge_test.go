package jsbridge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	conformance "github.com/OrdalieTech/pigo/conformance/runner"
)

func TestBridgeLoadsToolAndCommand(t *testing.T) {
	cwd := t.TempDir()
	extensionDir := filepath.Join(cwd, ".pi", "extensions")
	mustWrite(t, filepath.Join(extensionDir, "hello.ts"), fixtureSource(t, "hello.ts"))
	mustWrite(t, filepath.Join(extensionDir, "pirate.ts"), fixtureSource(t, "pirate.ts"))
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ProjectTrusted: true})
	t.Cleanup(loader.Close)
	loaded := loader.Load(context.Background())
	if len(loaded.Errors) != 0 {
		t.Fatalf("load errors = %#v", loaded.Errors)
	}
	runner := extensions.NewRunner(loaded.Registry, extensions.RunnerOptions{CWD: cwd, Mode: extensions.ModePrint})
	tool := runner.ToolDefinition("hello")
	if tool == nil {
		t.Fatal("hello tool was not registered")
	}
	if got, want := string(tool.Parameters), `{"type":"object","required":["name"],"properties":{"name":{"type":"string","description":"Name to greet"}}}`; got != want {
		t.Fatalf("hello schema = %s, want %s", got, want)
	}
	result, err := tool.Execute(context.Background(), "call-1", map[string]any{"name": "Pi"}, nil, runner.CreateContext())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 1 || result.Content[0].(*ai.TextContent).Text != "Hello, Pi!" {
		t.Fatalf("tool result = %#v", result)
	}
	command := runner.Command("pirate")
	if command == nil {
		t.Fatal("pirate command was not registered")
	}
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	before := runner.EmitBeforeAgentStart(context.Background(), "", nil, "base", extensions.SystemPromptOptions{})
	if before == nil || before.SystemPrompt == nil || !strings.Contains(*before.SystemPrompt, "PIRATE MODE") {
		t.Fatalf("before-agent result = %#v", before)
	}
}

func TestReloadUsesContentHashCacheAndFreshVMs(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "extension.ts")
	helper := filepath.Join(cwd, "value.ts")
	mustWrite(t, helper, `export const value = "one";`)
	mustWrite(t, entry, `
import { value } from "./value.ts";
export default async function(pi) {
  globalThis.loads = (globalThis.loads ?? 0) + 1;
  await Promise.resolve();
  pi.registerCommand("cached", {description: value + ":" + globalThis.loads, handler: async () => {}});
}
`)
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	first := loader.Load(context.Background())
	assertCommandDescription(t, first, "one:1")
	oldRunner := extensions.NewRunner(first.Registry, extensions.RunnerOptions{})
	oldCommand := oldRunner.Command("cached")
	if oldCommand == nil {
		t.Fatal("cached command was not registered")
	}
	builds := loader.cache.builds
	second := loader.Reload(context.Background())
	assertCommandDescription(t, second, "one:1")
	if err := oldCommand.Handler(context.Background(), "", oldRunner.CreateCommandContext()); err == nil || !strings.Contains(err.Error(), "VM is closed") {
		t.Fatalf("old command error = %v", err)
	}
	if loader.cache.builds != builds {
		t.Fatalf("unchanged reload rebuilt bundle: %d -> %d", builds, loader.cache.builds)
	}
	mustWrite(t, helper, `export const value = "two";`)
	third := loader.Reload(context.Background())
	assertCommandDescription(t, third, "two:1")
	if loader.cache.builds != builds+1 {
		t.Fatalf("dependency edit build count = %d, want %d", loader.cache.builds, builds+1)
	}
}

func TestSyntaxErrorReportsOriginalLine(t *testing.T) {
	var fixture struct {
		Syntax struct {
			Prefix string `json:"prefix"`
			Line   int    `json:"line"`
		} `json:"syntax"`
		InvalidFactoryPrefix string `json:"invalidFactoryPrefix"`
	}
	conformance.LoadJSON(t, "F11-jsbridge", "load-errors.json", &fixture)
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "broken.ts")
	mustWrite(t, entry, "export default function(pi) {\n  const valid = 1;\n  const broken: = 2;\n}\n")
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	result := loader.Load(context.Background())
	if len(result.Errors) != 1 {
		t.Fatalf("errors = %#v", result.Errors)
	}
	if !strings.HasPrefix(result.Errors[0].Error, fixture.Syntax.Prefix+": ") ||
		!strings.Contains(result.Errors[0].Error, "broken.ts:"+fmt.Sprint(fixture.Syntax.Line)+":") {
		t.Fatalf("syntax error = %q", result.Errors[0].Error)
	}

	noDefault := filepath.Join(cwd, "no-default.ts")
	mustWrite(t, noDefault, "export function named() {}\n")
	invalidLoader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{noDefault}})
	t.Cleanup(invalidLoader.Close)
	invalid := invalidLoader.Load(context.Background())
	if len(invalid.Errors) != 1 || !strings.HasPrefix(invalid.Errors[0].Error, fixture.InvalidFactoryPrefix+":") {
		t.Fatalf("invalid factory errors = %#v", invalid.Errors)
	}
}

func TestRuntimeErrorUsesInlineSourceMap(t *testing.T) {
	cwd := t.TempDir()
	entry := filepath.Join(cwd, "runtime-error.ts")
	mustWrite(t, entry, "export default function(pi) {\n  const message: string = 'mapped';\n  throw new Error(message);\n}\n")
	loader := NewLoader(DiscoveryOptions{CWD: cwd, AgentDir: filepath.Join(cwd, "agent"), ExplicitPaths: []string{entry}})
	t.Cleanup(loader.Close)
	result := loader.Load(context.Background())
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0].Error, "runtime-error.ts:3:") {
		t.Fatalf("runtime error = %#v", result.Errors)
	}
}

func assertCommandDescription(t *testing.T, result LoadResult, want string) {
	t.Helper()
	if len(result.Errors) != 0 {
		t.Fatalf("load errors = %#v", result.Errors)
	}
	command := extensions.NewRunner(result.Registry, extensions.RunnerOptions{}).Command("cached")
	if command == nil || command.Description != want {
		t.Fatalf("command = %#v, want description %q", command, want)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fixtureSource(t *testing.T, name string) string {
	t.Helper()
	content, err := conformance.ReadFixture("F11-jsbridge", name)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
