package buzz

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The team agent image installs the real buzz CLI, which runs with the Buzz
// key as the tools' user, execute-only, so the kernel runs it non-dumpable.
func TestExecuteOnlyProgramEnvironmentIsUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every process's environment")
	}
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "sleep")
	if err := os.WriteFile(program, data, 0o111); err != nil {
		t.Fatal(err)
	}
	running := exec.Command(program, "5")
	running.Env = []string{"BUZZ_PRIVATE_KEY=nostr-secret"}
	if err := running.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = running.Process.Kill(); _ = running.Wait() }()
	if environ, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", running.Process.Pid)); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read the program's environment (%v): %q", err, environ)
	}
}
