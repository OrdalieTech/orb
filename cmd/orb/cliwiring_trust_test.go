package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

// Regression for the project-trust bypass: untrusted project mcpServers must
// not spawn on `pi --help` or on unknown-flag invocations. Upstream gates
// every runtime-creation path behind resolveProjectTrusted, and the MCP
// contract keeps project entries invisible until the project-trust flow
// accepts the project (plugins/mcp/README.md).
func TestHelpAndUnknownFlagsDoNotSpawnUntrustedProjectMCPServers(t *testing.T) {
	for _, test := range []struct {
		name     string
		argv     []string
		wantCode int
	}{
		{name: "help", argv: []string{"--help"}, wantCode: 0},
		{name: "unknown flag", argv: []string{"--bogusflag"}, wantCode: 1},
		{name: "unknown flag with validation error", argv: []string{"--bogusflag", "--api-key", "k"}, wantCode: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			project := t.TempDir()
			marker := filepath.Join(t.TempDir(), "pwned")
			settings := mcpTouchSettings(t, "evil", marker)
			if err := os.MkdirAll(filepath.Join(project, ".pi"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), settings, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(config.EnvAgentDir, t.TempDir())
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Chdir(project)
			code := runCLIWithDependencies(context.Background(), test.argv, cliStreams{
				Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
			}, cliDependencies{})
			if code != test.wantCode {
				t.Fatalf("exit = %d, want %d", code, test.wantCode)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("untrusted project MCP server was spawned (marker stat err = %v)", err)
			}
		})
	}
}

// Control for the trust regression above: user-scope mcpServers still load on
// an unknown-flag invocation, which performs a full startup extension load
// (--help is metadata-only and no longer connects MCP servers, per the
// CHANGELOG). This proves the marker mechanism would catch a project-scope
// spawn.
func TestUnknownFlagStillLoadsUserScopeMCPServers(t *testing.T) {
	agentDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "spawned")
	settings := mcpTouchSettings(t, "probe", marker)
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir())
	code := runCLIWithDependencies(context.Background(), []string{"--bogusflag"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	}, cliDependencies{})
	if code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("user-scope MCP server did not spawn on the unknown-flag startup load: %v", err)
	}
}

// mcpTouchSettings is settings.json content whose MCP server named name
// creates marker as soon as it is spawned.
func mcpTouchSettings(t *testing.T, name, marker string) []byte {
	t.Helper()
	command, args := "/bin/sh", []string{"-c", "touch " + marker}
	if runtime.GOOS == "windows" {
		command, args = "cmd", []string{"/d", "/c", "type", "nul", ">", marker}
	}
	settings, err := json.Marshal(map[string]any{"mcpServers": map[string]any{
		name: map[string]any{"command": command, "args": args, "timeoutMs": 300},
	}})
	if err != nil {
		t.Fatal(err)
	}
	return settings
}
