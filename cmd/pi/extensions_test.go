package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
)

func TestLoadCompiledExtensionsUsesSettingsAndCatalogOrder(t *testing.T) {
	cwd := t.TempDir()
	agentDir := t.TempDir()
	settingsJSON := `{"goExtensions":{"status-line":true,"permission-gate":false,"pirate":true}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settingsJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	registry, diagnostics := loadCompiledExtensions(cwd, CLIArgs{}, settings)
	if len(diagnostics) != 0 {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	if got := strings.Join(runner.ExtensionPaths(), ","); got != "<inline:pirate>,<inline:status-line>" {
		t.Fatalf("compiled extension order = %q", got)
	}
	disabled, diagnostics := loadCompiledExtensions(cwd, CLIArgs{NoExtensions: true}, settings)
	if disabled != nil || len(diagnostics) != 0 {
		t.Fatalf("disabled registry = %#v, diagnostics = %v", disabled, diagnostics)
	}
}

func TestLoadCompiledExtensionsAddsMCPOnlyForEnabledConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		settings    string
		args        CLIArgs
		wantPath    string
		wantWarning string
	}{
		{name: "absent", settings: `{}`},
		{name: "server disabled", settings: `{"mcpServers":{"local":{"command":"ignored","enabled":false}}}`},
		{name: "extension disabled", settings: `{"goExtensions":{"mcp":false},"mcpServers":{"local":{"command":"ignored"}}}`},
		{name: "all extensions disabled", settings: `{"mcpServers":[]}`, args: CLIArgs{NoExtensions: true}},
		{name: "invalid", settings: `{"mcpServers":[]}`, wantWarning: "mcpServers"},
		{name: "enabled", settings: `{"mcpServers":{"local":{"command":"pi-go-mcp-command-that-does-not-exist","timeoutMs":20}}}`, wantPath: "<inline:mcp>"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cwd := t.TempDir()
			agentDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(test.settings), 0o600); err != nil {
				t.Fatal(err)
			}
			settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
			if err != nil {
				t.Fatal(err)
			}
			registry, diagnostics := loadCompiledExtensions(cwd, test.args, settings)
			runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
			paths := strings.Join(runner.ExtensionPaths(), ",")
			if paths != test.wantPath {
				t.Fatalf("extension paths = %q, want %q", paths, test.wantPath)
			}
			warnings := strings.Join(diagnostics, "\n")
			if test.wantWarning == "" && warnings != "" || test.wantWarning != "" && !strings.Contains(warnings, test.wantWarning) {
				t.Fatalf("diagnostics = %q, want substring %q", warnings, test.wantWarning)
			}
			if test.wantPath != "" && runner.Command("mcp") == nil {
				t.Fatal("/mcp command was not registered")
			}
		})
	}
}

func TestApplyExtensionFlagsUsesUpstreamBooleanAndStringRules(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:flags>", func(api extensions.API) error {
		api.RegisterFlag("enabled", extensions.Flag{Type: extensions.FlagBoolean, Default: false, Description: "enable it"})
		api.RegisterFlag("name", extensions.Flag{Type: extensions.FlagString})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	falseText := "false"
	value := "fixture"
	diagnostics := applyExtensionFlags(registry, []CLIUnknownFlag{
		{Name: "enabled", Value: &falseText}, {Name: "name", Value: &value}, {Name: "missing"},
	})
	if len(diagnostics) != 1 || diagnostics[0] != "Unknown option: --missing" {
		t.Fatalf("diagnostics = %v", diagnostics)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
	if enabled, ok := runner.FlagValues()["enabled"].(bool); !ok || !enabled {
		t.Fatalf("boolean flag = %#v", runner.FlagValues()["enabled"])
	}
	if got := runner.FlagValues()["name"]; got != "fixture" {
		t.Fatalf("string flag = %#v", got)
	}
}

func TestApplyExtensionFlagsRequiresStringValue(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:flag>", func(api extensions.API) error {
		api.RegisterFlag("name", extensions.Flag{Type: extensions.FlagString})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := applyExtensionFlags(registry, []CLIUnknownFlag{{Name: "name"}}); len(got) != 1 || got[0] != `Extension flag "--name" requires a value` {
		t.Fatalf("diagnostics = %v", got)
	}
}

func TestExtensionHelpListsRegisteredFlags(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:flag>", func(api extensions.API) error {
		api.RegisterFlag("plan", extensions.Flag{Type: extensions.FlagBoolean, Description: "Plan first"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	help := extensionHelpText(registry)
	if !strings.Contains(help, "Extension CLI Flags:\n") || !strings.Contains(help, "--plan") || !strings.Contains(help, "Plan first") {
		t.Fatalf("help = %q", help)
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
