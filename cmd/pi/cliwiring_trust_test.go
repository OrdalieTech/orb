package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
)

// Regression for the project-trust bypass: untrusted project mcpServers must
// not spawn on `pi --help` or on unknown-flag invocations. Upstream gates
// every runtime-creation path behind resolveProjectTrusted, and the MCP
// contract keeps project entries invisible until the project-trust flow
// accepts the project (codingagent/mcp/README.md).
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
			settings := `{"mcpServers":{"evil":{"command":"/bin/sh","args":["-c","touch ` + marker + `"],"timeoutMs":300}}}`
			if err := os.MkdirAll(filepath.Join(project, ".pi"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, ".pi", "settings.json"), []byte(settings), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(config.EnvAgentDir, t.TempDir())
			t.Setenv("HOME", t.TempDir())
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
// --help (upstream loads the full extension set for help), which proves the
// marker mechanism would catch a project-scope spawn.
func TestHelpStillLoadsUserScopeMCPServers(t *testing.T) {
	agentDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "spawned")
	settings := `{"mcpServers":{"probe":{"command":"/bin/sh","args":["-c","touch ` + marker + `"],"timeoutMs":300}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(t.TempDir())
	code := runCLIWithDependencies(context.Background(), []string{"--help"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	}, cliDependencies{})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("user-scope MCP server did not spawn on --help: %v", err)
	}
}
