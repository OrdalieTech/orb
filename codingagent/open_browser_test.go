package codingagent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/session"
)

// LOG-M1: the browser launcher mirrors upstream utils/open-browser.ts — the
// platform opener without a shell (no `cmd /c start` on Windows).
func TestLOGM1OpenBrowserCommandPerPlatform(t *testing.T) {
	tests := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{goos: "darwin", wantName: "open", wantArgs: []string{"https://example.test/a?b=c&d=e"}},
		{goos: "windows", wantName: "rundll32", wantArgs: []string{"url.dll,FileProtocolHandler", "https://example.test/a?b=c&d=e"}},
		{goos: "linux", wantName: "xdg-open", wantArgs: []string{"https://example.test/a?b=c&d=e"}},
		{goos: "freebsd", wantName: "xdg-open", wantArgs: []string{"https://example.test/a?b=c&d=e"}},
	}
	for _, test := range tests {
		name, args := openBrowserCommand(test.goos, "https://example.test/a?b=c&d=e")
		if name != test.wantName || len(args) != len(test.wantArgs) {
			t.Fatalf("%s launcher = %s %v", test.goos, name, args)
		}
		for index := range args {
			if args[index] != test.wantArgs[index] {
				t.Fatalf("%s launcher args = %v", test.goos, args)
			}
		}
	}
}

// LOG-M1: launcher failures stay best-effort (upstream swallows the spawn
// error event), so a missing opener binary must not error or panic.
func TestLOGM1OpenBrowserToleratesMissingLauncher(t *testing.T) {
	launchDetached("pigo-test-missing-launcher-binary", []string{"https://example.test"})
}

func newOpenBrowserSeamRuntime(t *testing.T, agentSettings string, runtimeConfig SessionRuntimeConfig) *SessionRuntime {
	t.Helper()
	cwd := t.TempDir()
	agentDir := t.TempDir()
	if agentSettings != "" {
		if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(agentSettings), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	runtimeConfig.Agent = agent.NewAgent(nil)
	runtimeConfig.SessionManager = manager
	runtimeConfig.Settings = settings
	runtime, err := NewSessionRuntime(runtimeConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	return runtime
}

// LOG-M4: ProviderAPIKey resolves the key the runtime would send, the seam the
// TUI uses for the sk-ant-oat subscription-auth check.
func TestLOGM4ProviderAPIKeySeamResolvesRequestAuth(t *testing.T) {
	key := "sk-ant-oat-seam"
	runtime := newOpenBrowserSeamRuntime(t, "", SessionRuntimeConfig{
		GetRequestAuth: func(context.Context, ai.ProviderID) (*agent.RequestAuth, error) {
			return &agent.RequestAuth{APIKey: &key}, nil
		},
	})
	resolved, err := runtime.ProviderAPIKey(context.Background(), "anthropic")
	if err != nil || resolved != key {
		t.Fatalf("ProviderAPIKey = %q, %v", resolved, err)
	}

	fallback := newOpenBrowserSeamRuntime(t, "", SessionRuntimeConfig{
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) {
			value := "plain-key"
			return &value, nil
		},
	})
	resolved, err = fallback.ProviderAPIKey(context.Background(), "anthropic")
	if err != nil || resolved != "plain-key" {
		t.Fatalf("fallback ProviderAPIKey = %q, %v", resolved, err)
	}
}

// LOG-M4: the warnings.anthropicExtraUsage gate flows through the runtime
// seam consumed by the TUI warning.
func TestLOGM4WarnAnthropicExtraUsageSeam(t *testing.T) {
	enabled := newOpenBrowserSeamRuntime(t, "", SessionRuntimeConfig{})
	if !enabled.WarnAnthropicExtraUsage() {
		t.Fatal("warning gate must default to enabled")
	}
	disabled := newOpenBrowserSeamRuntime(t, `{"warnings":{"anthropicExtraUsage":false}}`, SessionRuntimeConfig{})
	if disabled.WarnAnthropicExtraUsage() {
		t.Fatal("warnings.anthropicExtraUsage=false must disable the warning")
	}
}
