//go:build !windows && !wasm

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/internal/proctree"
)

func TestGetShellEnvPrependsManagedBinOnce(t *testing.T) {
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	t.Setenv("ORB_AGENT_DIR", agentDir)
	t.Setenv("PATH", "/first"+string(os.PathListSeparator)+"/second")

	environment, err := GetShellEnv()
	if err != nil {
		t.Fatal(err)
	}
	want := binDir + string(os.PathListSeparator) + "/first" + string(os.PathListSeparator) + "/second"
	if environment["PATH"] != want {
		t.Fatalf("PATH = %q, want %q", environment["PATH"], want)
	}
	t.Setenv("PATH", want)
	environment, err = GetShellEnv()
	if err != nil {
		t.Fatal(err)
	}
	if got := environment["PATH"]; got != want {
		t.Fatalf("already-prefixed PATH = %q, want %q", got, want)
	}
}

func TestLocalBashOperationsUsesShellEnvWhenUnset(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("ORB_AGENT_DIR", agentDir)
	operations := localBashOperationsForTest("/bin/sh")
	var output strings.Builder
	result, err := operations.Exec(context.Background(), `printf '%s' "$PATH"`, t.TempDir(), BashExecOptions{
		OnData: func(data []byte) { output.Write(data) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	wantPrefix := filepath.Join(agentDir, "bin") + string(os.PathListSeparator)
	if !strings.HasPrefix(output.String(), wantPrefix) {
		t.Fatalf("PATH = %q, want prefix %q", output.String(), wantPrefix)
	}
}

func TestLocalBashOperationsAbortKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	readyFile := filepath.Join(dir, "ready")
	survivalFile := filepath.Join(dir, "survived")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	operations := localBashOperationsForTest("/bin/sh")
	done := make(chan error, 1)
	go func() {
		_, err := operations.Exec(
			ctx,
			"(sleep 0.5; printf survived > "+shellSingleQuote(survivalFile)+") & printf ready > "+shellSingleQuote(readyFile)+"; wait",
			dir,
			BashExecOptions{},
		)
		done <- err
	}()
	waitForFile(t, readyFile)
	cancel()
	select {
	case err := <-done:
		if err == nil || err.Error() != "aborted" {
			t.Fatalf("abort error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("aborted command did not return")
	}
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(survivalFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("background descendant survived process-group kill: %v", err)
	}
}

func TestLocalBashOperationsSerializesStdoutAndStderrCallbacks(t *testing.T) {
	operations := localBashOperationsForTest("/bin/sh")
	var inCallback atomic.Bool
	var overlap atomic.Bool
	_, err := operations.Exec(
		context.Background(),
		`(for i in 1 2 3 4 5; do printf o; sleep 0.01; done) & (for i in 1 2 3 4 5; do printf e >&2; sleep 0.01; done) & wait`,
		t.TempDir(),
		BashExecOptions{OnData: func([]byte) {
			if !inCallback.CompareAndSwap(false, true) {
				overlap.Store(true)
				return
			}
			time.Sleep(2 * time.Millisecond)
			inCallback.Store(false)
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if overlap.Load() {
		t.Fatal("OnData callbacks overlapped")
	}
}

// A timeout that hits after the shell exited, while a background process it started keeps
// writing, also reports the shell's exit (#11613).
func TestBashTimeoutReportsAnEarlierShellExit(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // the flood below spills to a full-output file
	tool := NewBashTool(t.TempDir(), &BashToolOptions{Operations: localBashOperationsForTest("/bin/sh")})
	for command, want := range map[string]*regexp.Regexp{
		// The writer forks nothing: a pause between two processes on a loaded machine would
		// outlast the grace that follows the shell's exit.
		"(while :; do echo tick; done) & sleep 0.1; exit 3": regexp.MustCompile(
			`Command timed out after 0\.6 seconds\. The shell exited with code 3 after 0\.\d seconds, but background processes it started kept writing output until the timeout\.$`),
		"echo started; sleep 30": regexp.MustCompile(`started\s+Command timed out after 0\.6 seconds$`),
	} {
		_, err := tool.Execute(context.Background(), "call", map[string]any{"command": command, "timeout": 0.6}, nil)
		if err == nil || !want.MatchString(err.Error()) {
			t.Fatalf("%s: error = %v", command, err)
		}
	}
}

func localBashOperationsForTest(shell string) *localBashOperations {
	return &localBashOperations{shell: func() (proctree.Shell, error) {
		return proctree.Shell{Path: shell, Args: []string{"-c"}}, nil
	}}
}

func waitForFile(t testing.TB, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
