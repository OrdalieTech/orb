package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A team agent's tools run as its user, yet cannot read the credentials in
// the agent's environment through /proc.
func TestHiddenAgentEnvironmentIsUnreadableToItsTools(t *testing.T) {
	if os.Getenv("ORB_HIDE_HELPER") == "1" {
		hideProcess()
		out, _ := exec.Command("sh", "-c", "cat /proc/$PPID/environ").CombinedOutput()
		_, _ = os.Stdout.Write(out)
		os.Exit(0)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads every process's environment")
	}
	agent := exec.Command(os.Args[0], "-test.run=^TestHiddenAgentEnvironmentIsUnreadableToItsTools$")
	agent.Env = append(os.Environ(), "ORB_HIDE_HELPER=1", "OPENROUTER_API_KEY=sk-or-secret")
	out, err := agent.CombinedOutput()
	if err != nil || strings.Contains(string(out), "sk-or-secret") {
		t.Fatalf("a tool read the agent's environment (%v): %q", err, out)
	}
}

// The real buzz CLI, which runs with the Buzz key as the tools' user, is
// installed execute-only so the kernel runs it non-dumpable.
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
