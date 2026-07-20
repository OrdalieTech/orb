package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/codingagent/config"
)

// Finding 8: pi --help must document the --extension/-e flag and the package
// subcommands, mirroring upstream cli/args.ts.
func TestHelpTextDocumentsExtensionFlagAndCommands(t *testing.T) {
	for _, want := range []string{
		"--extension, -e <path>",
		"pi install <source>",
		"pi remove <source>",
		"pi uninstall <source>",
		"pi update",
		"pi list",
		"pi config",
	} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help text missing %q", want)
		}
	}
}

const listModelsProviderExtension = `export default function (pi) {
  pi.registerProvider("fakeprov", {
    name: "Fake Provider",
    baseUrl: "https://fake.invalid",
    api: "openai-responses",
    apiKey: process.env.FAKE_KEY,
    models: [{ id: "fake-1", name: "Fake One", reasoning: false, input: ["text"],
      cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, contextWindow: 1000, maxTokens: 100 }],
    streamSimple: () => { throw new Error("unused"); },
  });
}
`

// Finding 7: --list-models lists providers registered by extensions, because it
// now runs after full runtime creation (upstream main.ts:747-764) instead of
// short-circuiting on a bare models.json registry.
func TestListModelsIncludesExtensionRegisteredProviders(t *testing.T) {
	cwd := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("FAKE_KEY", "dummy")
	t.Chdir(cwd)

	extDir := filepath.Join(cwd, "ext")
	if err := os.MkdirAll(extDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extDir, "index.ts"), []byte(listModelsProviderExtension), 0o644); err != nil {
		t.Fatal(err)
	}

	search := "fake"
	var stdout bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--list-models", search, "-e", filepath.Join(extDir, "index.ts")}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "fakeprov") || !strings.Contains(stdout.String(), "fake-1") {
		t.Fatalf("extension-registered provider missing from --list-models output:\n%s", stdout.String())
	}
}

// Regression: --list-models builds the runtime to enumerate extension providers,
// but MCP servers contribute tools, not models — model enumeration must not spawn
// and connect them (a cost/side-effect the pre-fix bare-registry listing lacked).
func TestListModelsDoesNotSpawnMCPServers(t *testing.T) {
	cwd := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(cwd)

	marker := filepath.Join(cwd, "SPAWNED")
	spawn := filepath.Join(cwd, "spawn.sh")
	if err := os.WriteFile(spawn, []byte("#!/bin/sh\ntouch \""+marker+"\"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Global (user-scope) settings need no project trust to load.
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"mcpServers":{"toy":{"command":"` + spawn + `"}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--list-models"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("--list-models spawned the configured MCP server (marker created)")
	}
}
