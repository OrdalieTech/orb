package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareHostEnvironmentMakesPiResolveConfiguredBinary(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	binary := writeFakeCommand(t, filepath.Join(root, "configured-orb"),
		"printf '%s\\n' 'orb configured-version'\n", "echo orb configured-version\n")

	environment, err := prepareHostEnvironment(Options{AgentDir: agentDir, OrbExecutable: binary}, []string{"PATH=" + shellSearchPath, "KEEP=value"}, "")
	if err != nil {
		t.Fatal(err)
	}
	command := shellCommand("pi --version")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != "orb configured-version" {
		t.Fatalf("pi --version = %q", got)
	}
	shim := filepath.Join(agentDir, "host", "bin", commandFileName("pi"))
	if got := environmentValue(environment, piSubagentBinaryEnv); got != shim {
		t.Fatalf("%s = %q, want %q", piSubagentBinaryEnv, got, shim)
	}
	if got := environmentValue(environment, piAgentDirEnv); got != agentDir {
		t.Fatalf("%s = %q, want %q", piAgentDirEnv, got, agentDir)
	}
	if got := environmentValue(environment, piAgentMarkerEnv); got != "true" {
		t.Fatalf("%s = %q", piAgentMarkerEnv, got)
	}
	if got := environmentValue(environment, "KEEP"); got != "value" {
		t.Fatalf("preserved environment = %q", got)
	}
}

func TestPrepareHostEnvironmentAtomicallyRefreshesPiTarget(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	first := filepath.Join(root, "orb-first")
	second := filepath.Join(root, "orb-second")
	writeExecutable(t, first, "#!/bin/sh\nprintf first\n")
	writeExecutable(t, second, "#!/bin/sh\nprintf second\n")
	if _, err := prepareHostEnvironment(Options{AgentDir: agentDir, OrbExecutable: first}, nil, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareHostEnvironment(Options{AgentDir: agentDir, OrbExecutable: second}, nil, ""); err != nil {
		t.Fatal(err)
	}
	target, err := os.Readlink(filepath.Join(agentDir, "host", "bin", "pi"))
	if err != nil {
		t.Fatal(err)
	}
	if target != second {
		t.Fatalf("pi shim target = %q, want %q", target, second)
	}
}

func writeExecutable(t *testing.T, path, source string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(source), 0o755); err != nil {
		t.Fatal(err)
	}
}
