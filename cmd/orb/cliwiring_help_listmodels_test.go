package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
)

// The skill is Orb's one self-description: Orb's own sessions list the very file
// `orb skill` prints, it loads without a warning, and every command in its code
// blocks is a real command's help that exits 0 and changes nothing.
func TestOrbSkillIsTheOneOrbLoadsAndNamesOnlySafeHelp(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv(config.EnvAgentDir, filepath.Join(root, "agent"))
	t.Setenv("ORB_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("ORB_BRIDGE_HOME", filepath.Join(root, "bridge"))
	t.Setenv("ORB_OFFLINE", "1")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	run := func(stdin string, args ...string) (string, int) {
		var stdout bytes.Buffer
		code := runNativeCLI(context.Background(), args, cliStreams{Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: io.Discard})
		return stdout.String(), code
	}
	tree := func() map[string]string {
		files := map[string]string{}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && !entry.IsDir() {
				data, _ := os.ReadFile(path)
				files[path] = string(data)
			}
			return nil
		})
		return files
	}

	skill, code := run("", "skill")
	if code != 0 || skill == "" {
		t.Fatalf("orb skill: exit %d, %d bytes", code, len(skill))
	}
	// The first command creates Orb's database; from then on help changes nothing.
	help, _ := run("", "--help")
	before := tree()
	var commands []string
	fenced := false
	for _, line := range strings.Split(skill, "\n") {
		if strings.HasPrefix(line, "```") {
			fenced = !fenced
		} else if fenced && strings.HasPrefix(line, "orb ") {
			commands = append(commands, line)
		}
	}
	if len(commands) == 0 {
		t.Fatal("the skill names no discovery command")
	}
	for _, command := range commands {
		out, code := run("", strings.Fields(command)[1:]...)
		if code != 0 || !strings.Contains(out, strings.TrimSuffix(command, " --help")) || command != "orb --help" && out == help {
			t.Errorf("%s: exit %d, not that command's help:\n%s", command, code, out)
		}
		if !maps.Equal(before, tree()) {
			t.Errorf("%s changed Orb's files or state", command)
			before = tree()
		}
	}

	frames, _ := run(`{"type":"get_commands"}`+"\n", "--mode", "rpc", "--no-session")
	path := ""
	for _, line := range strings.Split(frames, "\n") {
		var frame struct {
			Command string
			Data    struct {
				Commands []struct {
					Name       string
					SourceInfo struct{ Path string }
				}
			}
		}
		if json.Unmarshal([]byte(line), &frame) == nil && frame.Command == "get_commands" {
			for _, command := range frame.Data.Commands {
				if command.Name == "skill:orb" {
					path = command.SourceInfo.Path
				}
			}
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != skill {
		t.Fatalf("Orb's sessions do not list the skill orb skill prints (path %q, %v):\n%s", path, err, frames)
	}
	loaded := agent.LoadSkills(agent.LoadSkillsOptions{CWD: project, AgentDir: filepath.Join(root, "agent"), SkillPaths: []string{path}})
	if len(loaded.Diagnostics) != 0 || len(loaded.Skills) != 1 || loaded.Skills[0].Name != "orb" {
		t.Fatalf("skill frontmatter: %+v", loaded)
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

// --list-models lists providers registered by extensions, because it runs
// after full runtime creation instead of on a bare models.json registry.
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
			// The global mcp.json needs no project trust to load.
			if err := os.MkdirAll(agentDir, 0o755); err != nil {
				t.Fatal(err)
			}
			settings := `{"mcpServers":{"toy":{"command":"` + spawn + `"}}}`
			if err := os.WriteFile(filepath.Join(agentDir, "mcp.json"), []byte(settings), 0o644); err != nil {
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
	if err := os.WriteFile(filepath.Join(agentDir, "mcp.json"), []byte(settings), 0o644); err != nil {
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
