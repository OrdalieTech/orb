package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/platforms/native"
)

func TestNativeRootPrefixes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"ORB_AGENT_DIR", "ORB_STATE_HOME", "ORB_BRIDGE_HOME"} {
		t.Setenv(key, "")
	}
	pi := filepath.Join(home, ".pi", "agent")
	t.Setenv("PI_CODING_AGENT_DIR", pi)
	if err := os.MkdirAll(pi, 0700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(pi, "settings.json")
	if err := os.WriteFile(sentinel, []byte("not Orb configuration"), 0600); err != nil {
		t.Fatal(err)
	}
	agentDir, stateDir := filepath.Join(home, "own-agent"), filepath.Join(home, "own-state")
	var out, errs bytes.Buffer
	code := runNativeCLI(t.Context(), []string{"--agent-dir", agentDir, "--state-home", stateDir, "storage", "config", "export", "settings.json", filepath.Join(home, "export.json")}, cliStreams{Stdout: &out, Stderr: &errs})
	if code != 0 {
		t.Fatalf("code=%d: %s", code, errs.String())
	}
	if _, err := os.Stat(filepath.Join(stateDir, "orb.db")); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(sentinel); string(got) != "not Orb configuration" {
		t.Fatal("Pi sentinel changed")
	}
	if _, err := os.Stat(filepath.Join(home, ".orb", "state", "orb.db")); !os.IsNotExist(err) {
		t.Fatal("opened default database", err)
	}
}

func TestHerdrRootsPreserveDefaultDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"ORB_AGENT_DIR", "ORB_STATE_HOME", "ORB_BRIDGE_HOME"} {
		t.Setenv(key, "")
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		t.Fatal(err)
	}
	before, err := native.Path(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	roots, err := herdrResumeRoots(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	argv, files, err := nativeRootOptions(append(roots, "--version"))
	if err != nil {
		t.Fatal(err)
	}
	if files || !slices.Equal(argv, []string{"--version"}) {
		t.Fatal(argv, files)
	}
	after, err := native.Path(agentDir)
	if err != nil || before != after {
		t.Fatalf("database moved: %s -> %s (%v)", before, after, err)
	}
}

func TestNativeIDSurvivesAgentDirectorySeparation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"ORB_AGENT_DIR", "ORB_STATE_HOME", "ORB_BRIDGE_HOME"} {
		t.Setenv(key, "")
	}
	oldDir := filepath.Join(home, ".pi", "agent")
	state, err := openNativeState(t.Context(), oldDir, false)
	if err != nil {
		t.Fatal(err)
	}
	manager, _, err := createCLISession(home, CLIArgs{native: state}, cliStreams{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AppendMessage(map[string]any{"role": "user", "content": "retained conversation"}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.AppendMessage(map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "retained answer"}}, "stopReason": "stop"}); err != nil {
		t.Fatal(err)
	}
	id := manager.GetSessionID()
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", oldDir)
	output := filepath.Join(home, "export.md")
	var stdout, stderr bytes.Buffer
	code := runNativeCLI(t.Context(), []string{"--export", id, output}, cliStreams{Stdout: &stdout, Stderr: &stderr})
	if code != 0 {
		t.Fatalf("native resume/export: %d %s", code, stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil || !bytes.Contains(data, []byte("retained conversation")) {
		t.Fatalf("lost session %s: %s %v", id, data, err)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatal("Pi directory was touched", err)
	}
}

func TestFileRestorePrefixesAvoidDefaultMigratedRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"ORB_AGENT_DIR", "ORB_STATE_HOME", "ORB_BRIDGE_HOME"} {
		t.Setenv(key, "")
	}
	defaultDir, err := config.GetAgentDir()
	if err != nil {
		t.Fatal(err)
	}
	state, err := openNativeState(t.Context(), defaultDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = state.Close(); err != nil {
		t.Fatal(err)
	}
	filesDir := filepath.Join(home, "file-agent")
	manager := createCLIStoredSession(t, home, filepath.Join(filesDir, "sessions"), "file-session")
	for index, prefix := range [][]string{
		{"--pi-files", "--agent-dir", filesDir, "--state-home", filepath.Join(filesDir, "state")},
		{"--agent-dir", filesDir, "--pi-files", "--state-home", filepath.Join(filesDir, "state")},
	} {
		output := filepath.Join(home, fmt.Sprintf("file-%d.md", index))
		var stdout, stderr bytes.Buffer
		argv := append(prefix, "--session", manager.GetSessionFile(), "--export", manager.GetSessionFile(), output)
		if code := runNativeCLI(t.Context(), argv, cliStreams{Stdout: &stdout, Stderr: &stderr}); code != 0 {
			t.Fatalf("file restore: %d %s", code, stderr.String())
		}
		if data, err := os.ReadFile(output); err != nil || !bytes.Contains(data, []byte("answer")) {
			t.Fatalf("file not restored: %s %v", data, err)
		}
	}
}

func TestNativeRootValidation(t *testing.T) {
	for _, args := range [][]string{{"--agent-dir"}, {"--state-home", ""}, {"--bridge-home", "--version"}} {
		if _, _, err := nativeRootOptions(args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	args := []string{"--system-prompt", "--agent-dir", "not-a-root"}
	rest, _, err := nativeRootOptions(args)
	if err != nil || !slices.Equal(rest, args) {
		t.Fatalf("consumed runtime args: %v %v", rest, err)
	}
}
