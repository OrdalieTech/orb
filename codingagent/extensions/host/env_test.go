package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareHostEnvironmentMakesPiResolveConfiguredBinary(t *testing.T) {
	root := t.TempDir()
	agentDir := filepath.Join(root, "agent")
	binary := filepath.Join(root, "configured-pigo")
	writeExecutable(t, binary, "#!/bin/sh\nprintf '%s\\n' 'pigo configured-version'\n")

	environment, err := prepareHostEnvironment(agentDir, []string{"PATH=/usr/bin:/bin", "KEEP=value"}, binary)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("/bin/sh", "-c", "pi --version")
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(output)); got != "pigo configured-version" {
		t.Fatalf("pi --version = %q", got)
	}
	shim := filepath.Join(agentDir, "host", "bin", "pi")
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
	first := filepath.Join(root, "pigo-first")
	second := filepath.Join(root, "pigo-second")
	writeExecutable(t, first, "#!/bin/sh\nprintf first\n")
	writeExecutable(t, second, "#!/bin/sh\nprintf second\n")
	if _, err := prepareHostEnvironment(agentDir, nil, first); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareHostEnvironment(agentDir, nil, second); err != nil {
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
