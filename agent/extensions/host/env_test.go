package host

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/internal/nodepath"
)

func TestPrepareHostEnvironmentMakesPiResolveConfiguredBinary(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	binary := writeFakeCommand(t, filepath.Join(root, "configured-orb"),
		"printf '%s\\n' 'orb configured-version'\n", "echo orb configured-version\n")

	environment, err := prepareHostEnvironment(Options{AgentDir: agentDir, OrbExecutable: binary}, []string{"PATH=" + shellSearchPath, "KEEP=value", "PI_CODING_AGENT=true", "PI_CODING_AGENT_DIR=/pi-only", "HERDR_AGENT=pi", "AI_AGENT=pi", "PI_SESSION_ID=pi-session", "PI_CODING_AGENT_SESSION_DIR=/pi-sessions"}, "")
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
	if got := environmentValue(environment, nodepath.AgentDirEnv); got != agentDir {
		t.Fatalf("%s = %q, want %q", nodepath.AgentDirEnv, got, agentDir)
	}
	for key, want := range map[string]string{
		"ORB_AGENT_DIR": agentDir, "PI_CODING_AGENT_DIR": agentDir,
		"AI_AGENT": "orb", "ORB_CODING_AGENT": "true",
		"PI_CODING_AGENT": "", "HERDR_AGENT": "",
		"PI_SESSION_ID": "", "PI_CODING_AGENT_SESSION_DIR": "",
	} {
		if got := environmentValue(environment, key); got != want {
			t.Fatalf("%s = %q, want %q", key, got, want)
		}
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
