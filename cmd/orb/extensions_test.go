package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	extensionhost "github.com/OrdalieTech/orb/agent/extensions/host"
	"github.com/OrdalieTech/orb/ai"
)

func TestAutoLoadsPolicyWithoutPersistingOverride(t *testing.T) {
	for _, settingsJSON := range []string{
		`{}`,
		`{"plugins":{"permissions":{"enabled":false,"mode":"log","rules":[{"tool":"blocked","action":"deny"},{"tool":"write","action":"ask"}]}}}`,
		`{"plugins":{"permissions":{"mode":"invalid"}}}`,
	} {
		cwd, agentDir := t.TempDir(), t.TempDir()
		settingsPath := filepath.Join(agentDir, "settings.json")
		if err := os.WriteFile(settingsPath, []byte(settingsJSON), 0600); err != nil {
			t.Fatal(err)
		}
		settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
		if err != nil {
			t.Fatal(err)
		}
		registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{Auto: true}, settings, nil)
		if len(diagnostics) != 0 {
			t.Fatal(diagnostics)
		}
		runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd})
		if runner.Command("permissions") == nil {
			t.Fatal("--auto did not enable the permission plugin")
		}
		for _, tool := range []string{"write", "blocked"} {
			got := runner.EmitToolCall(t.Context(), extensions.ToolCallEvent{ToolName: tool})
			wantDeny := strings.Contains(settingsJSON, "invalid") || tool == "blocked" && strings.Contains(settingsJSON, "rules")
			if got == nil || got.Block != wantDeny || got.Approved == wantDeny {
				t.Fatalf("%s, %s: %#v", settingsJSON, tool, got)
			}
		}
		data, err := os.ReadFile(settingsPath)
		if err != nil || string(data) != settingsJSON {
			t.Fatalf("--auto persisted settings: %s, %v", data, err)
		}
	}
}

func TestFirstPartyPluginsAreDormantUntilEnabled(t *testing.T) {
	tests := []struct {
		name, settings string
		want           []string
	}{
		{name: "default off", settings: `{}`, want: []string{"tool_search"}},
		{name: "enabled", settings: `{"plugins":{"tasks":true,"websearch":true,"subagents":true,"permissions":{"mode":"log"},"memory":true}}`, want: []string{"fetch_content", "forget", "recall", "remember", "replace", "subagent", "todo", "tool_search", "web_search"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd, agentDir := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(test.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
			if err != nil {
				t.Fatal(err)
			}
			registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{}, settings, nil)
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %v", diagnostics)
			}
			runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
			var tools []string
			for _, registered := range runner.AllRegisteredTools() {
				tools = append(tools, registered.Definition.Name)
			}
			sort.Strings(tools)
			if got := strings.Join(tools, ","); got != strings.Join(test.want, ",") {
				t.Fatalf("tools = %q, want %q", got, strings.Join(test.want, ","))
			}
			if test.name == "default off" {
				if _, err := os.Stat(filepath.Join(agentDir, "memory")); !os.IsNotExist(err) {
					t.Fatalf("default-off memory plugin touched storage: %v", err)
				}
			}
			if runner.Command("plugins") == nil {
				t.Fatal("/plugins control command missing")
			}
			if got := runner.Command("permissions") != nil; got != (test.name == "enabled") {
				t.Fatalf("/permissions present = %t", got)
			}
		})
	}
}

func TestRegisteredCommandExecAndEventBusUseBoundRuntime(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	var command extensions.Command
	var busValue string
	if err := registry.Register("<inline:command>", func(api extensions.API) error {
		api.Events().On("fixture", func(_ context.Context, value any) error {
			busValue, _ = value.(string)
			return nil
		})
		api.RegisterCommand("probe", extensions.Command{Handler: func(ctx context.Context, _ string, _ extensions.CommandContext) error {
			result, err := api.Exec(ctx, "sh", []string{"-c", "printf exec"}, nil)
			if err != nil {
				return err
			}
			api.Events().Emit(ctx, "fixture", result.Stdout)
			return nil
		}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	resolved := runner.Command("probe")
	if resolved == nil {
		t.Fatal("command was not registered")
	}
	command = resolved.Command
	if err := command.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if busValue != "exec" {
		t.Fatalf("event bus value = %q", busValue)
	}
}

func TestHerdrHostOnlyReceivesInteractiveHint(t *testing.T) {
	if _, err := extensionhost.DiscoverRuntime(t.Context()); err != nil {
		t.Skip("extension-host e2e requires Node.js >=22.6 or Bun on PATH")
	}
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_PANE_ID", "w1:p1")
	t.Setenv("HERDR_SOCKET_PATH", filepath.Join(t.TempDir(), "socket"))
	t.Setenv("HERDR_AGENT", "")
	for _, interactive := range []bool{true, false} {
		t.Run(map[bool]string{true: "interactive", false: "headless"}[interactive], func(t *testing.T) {
			cwd, agentDir := t.TempDir(), t.TempDir()
			// Stopped before the temp dirs go: Windows can't remove a directory the host still holds.
			t.Cleanup(func() { replaceActiveExtensionHost(nil) })
			path := filepath.Join(cwd, "herdr.mjs")
			source := `// installed by herdr
// HERDR_INTEGRATION_ID=pi
export default function(pi) {
 pi.registerTool({name: "herdr_hint", label: "hint", description: "hint", parameters: {type: "object", properties: {}},
 execute: async () => ({content: [{type: "text", text: process.env.HERDR_AGENT || "none"}], details: {}})});
}`
			if err := os.WriteFile(path, []byte(source), 0600); err != nil {
				t.Fatal(err)
			}
			settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
			if err != nil {
				t.Fatal(err)
			}
			registry, diagnostics := loadCompiledExtensions(cwd, agentDir, CLIArgs{NoExtensions: true, Extensions: []string{path}, allowNoModel: interactive}, settings, nil)
			if len(diagnostics) != 0 {
				t.Fatal(diagnostics)
			}
			runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd})
			tool := runner.ToolDefinition("herdr_hint")
			if tool == nil {
				t.Fatal("hint tool not registered")
			}
			result, err := tool.Execute(t.Context(), "probe", map[string]any{}, nil, runner.CreateContext())
			if err != nil || len(result.Content) != 1 {
				t.Fatalf("probe: %#v, %v", result, err)
			}
			want := "none"
			if interactive {
				want = "pi"
			}
			text, ok := result.Content[0].(*ai.TextContent)
			if !ok || text.Text != want {
				t.Fatalf("child hint: %#v, want %q", result.Content, want)
			}
			if os.Getenv("HERDR_AGENT") != "" {
				t.Fatal("changed parent environment")
			}
		})
	}
}
