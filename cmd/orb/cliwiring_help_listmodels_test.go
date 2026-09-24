package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
)

// Finding 8: pi --help must document the --extension/-e flag and the package
// subcommands, mirroring upstream cli/args.ts.
func TestHelpTextDocumentsExtensionFlagAndCommands(t *testing.T) {
	for _, want := range []string{
		"--extension, -e <path>",
		"--no-extensions, -ne           Disable extension discovery (explicit -e paths still work)",
		"--theme <path>",
		"--no-themes",
		"orb install <source>",
		"orb remove <source>",
		"orb uninstall <source>",
		"orb update [target]         Update orb itself, installed packages, or model catalogs",
		"orb list",
		"orb config",
		"--offline",
	} {
		if !strings.Contains(helpText, want) {
			t.Fatalf("help text missing %q", want)
		}
	}
}

func TestRunCLIClosesExtensionHostBeforeReturning(t *testing.T) {
	requireExtensionHostRuntime(t)
	cwd := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	closeExtensionHostOnCleanup(t)
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(cwd)
	extension := writeJSExtension(t, filepath.Join(cwd, "ext"), `export default function () {
		setInterval(() => {}, 1000);
	}`)

	code := runCLI(context.Background(), []string{"--help", "--no-extensions", "-e", extension}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	extensionHostMu.Lock()
	active := activeExtensionHost
	extensionHostMu.Unlock()
	if active != nil {
		t.Fatal("runCLI returned with a live extension host")
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
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("FAKE_KEY", "dummy")
	t.Chdir(cwd)
	// The extension host runs in cwd; win32 cannot remove a directory in use.
	t.Cleanup(func() { replaceActiveExtensionHost(nil) })

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
// and --help renders extension flags from registration metadata only; MCP
// servers contribute tools, not models or flags, so neither metadata command may
// spawn and connect them (a cost/side-effect the pre-fix paths lacked).
func TestMetadataCommandsDoNotSpawnMCPServers(t *testing.T) {
	for _, test := range []struct {
		name, flag, wantStdout string
	}{
		{name: "ListModelsDoesNotSpawnMCPServers", flag: "--list-models"},
		{name: "HelpDoesNotSpawnMCPServers", flag: "--help", wantStdout: "Usage: orb"},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			code := runCLIWithDependencies(context.Background(), []string{test.flag}, cliStreams{
				Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true,
			}, cliDependencies{})
			if code != 0 {
				t.Fatalf("exit=%d stdout=%q", code, stdout.String())
			}
			if !strings.Contains(stdout.String(), test.wantStdout) {
				t.Fatalf("%s output missing %q:\n%s", test.flag, test.wantStdout, stdout.String())
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatalf("%s spawned the configured MCP server (marker created)", test.flag)
			}
		})
	}
}

// Guard: --version answers before any extension infrastructure runs — neither
// a configured MCP server nor the JS extension host runtime may be spawned.
func TestVersionDoesNotSpawnExtensionInfrastructure(t *testing.T) {
	cwd := t.TempDir()
	agentDir := filepath.Join(t.TempDir(), "agent")
	t.Setenv(config.EnvAgentDir, agentDir)
	t.Setenv("HOME", t.TempDir())
	t.Chdir(cwd)

	mcpMarker := filepath.Join(cwd, "MCP_SPAWNED")
	spawn := filepath.Join(cwd, "spawn.sh")
	if err := os.WriteFile(spawn, []byte("#!/bin/sh\ntouch \""+mcpMarker+"\"\ncat\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"mcpServers":{"toy":{"command":"` + spawn + `"}}}`
	if err := os.WriteFile(filepath.Join(agentDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSExtension(t, filepath.Join(agentDir, "extensions", "guard"), listModelsProviderExtension)
	nodeMarker := filepath.Join(cwd, "NODE_SPAWNED")
	fakeNode := filepath.Join(cwd, "node.sh")
	if err := os.WriteFile(fakeNode, []byte("#!/bin/sh\ntouch \""+nodeMarker+"\"\necho v22.13.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ORB_NODE", fakeNode)

	var stdout bytes.Buffer
	code := runCLIWithDependencies(context.Background(), []string{"--version"}, cliStreams{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &bytes.Buffer{}, StdinTTY: true, StdoutTTY: true,
	}, cliDependencies{})
	if code != 0 {
		t.Fatalf("exit=%d stdout=%q", code, stdout.String())
	}
	if !strings.Contains(stdout.String(), "orb ") {
		t.Fatalf("version output missing:\n%s", stdout.String())
	}
	if _, err := os.Stat(mcpMarker); err == nil {
		t.Fatal("--version spawned the configured MCP server (marker created)")
	}
	if _, err := os.Stat(nodeMarker); err == nil {
		t.Fatal("--version spawned the extension host runtime (marker created)")
	}
}
