package teamenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A team agent's tools run as its user, yet cannot read the credentials in
// the agent's environment through /proc.
func TestHiddenAgentEnvironmentIsUnreadableToItsTools(t *testing.T) {
	if os.Getenv("ORB_HIDE_HELPER") == "1" {
		Hide()
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

// Credentials handed over on descriptors are usable by the agent, yet not by
// path: its read tool, which runs in the agent, can open /proc/self/environ and
// /proc/self/fd/N, and finds neither.
func TestCredentialsOnDescriptorsAreUnreadableByPath(t *testing.T) {
	if os.Getenv("ORB_DESCRIPTORS_HELPER") == "1" {
		if err := LoadSecrets(); err != nil {
			os.Exit(2)
		}
		// A first start opens the state twice; the descriptor must survive the
		// collection of whatever the first open left behind.
		_ = AuthDocument()
		runtime.GC()
		runtime.GC()
		auth, _ := AuthDocument().Read(context.Background())
		environ, _ := os.ReadFile("/proc/self/environ")
		_, reopen := os.ReadFile("/proc/self/fd/4")
		fmt.Printf("key=%s auth=%s environ=%t reopen=%t\n", os.Getenv("OPENROUTER_API_KEY"), auth,
			strings.Contains(string(environ), "sk-or-secret"), errors.Is(reopen, os.ErrPermission))
		os.Exit(0)
	}
	if os.Geteuid() == 0 {
		t.Skip("root opens any file")
	}
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(`{"openai-codex":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = auth.Close() }()
	// As the root-owned file the entrypoint opens: unreadable by the agent's user.
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	secrets, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_, _ = writer.WriteString("OPENROUTER_API_KEY=sk-or-secret\n")
	_ = writer.Close()
	agent := exec.Command(os.Args[0], "-test.run=^TestCredentialsOnDescriptorsAreUnreadableByPath$")
	agent.ExtraFiles = []*os.File{secrets, auth}
	agent.Env = append(os.Environ(), "ORB_DESCRIPTORS_HELPER=1", "ORB_SECRETS_FD=3", "ORB_AUTH_FD=4")
	out, err := agent.CombinedOutput()
	if want := "key=sk-or-secret auth={\"openai-codex\":{}} environ=false reopen=true\n"; err != nil || string(out) != want {
		t.Fatalf("agent saw %q (%v), want %q", out, err, want)
	}
}
