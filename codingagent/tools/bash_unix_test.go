//go:build !windows

package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestBashShellConfigOmitsTransportExceptLegacyWSL(t *testing.T) {
	customShell := filepath.Join(t.TempDir(), "bash")
	if err := os.WriteFile(customShell, nil, 0o755); err != nil {
		t.Fatal(err)
	}
	config, err := GetShellConfig(customShell)
	if err != nil {
		t.Fatal(err)
	}
	if config.Shell != customShell || config.CommandTransport != "" || len(config.Args) != 1 || config.Args[0] != "-c" {
		t.Fatalf("custom shell config = %+v", config)
	}

	legacy := bashShellConfig(`C:\Windows\Sysnative\bash.exe`)
	if legacy.CommandTransport != ShellCommandStdin || len(legacy.Args) != 1 || legacy.Args[0] != "-s" {
		t.Fatalf("legacy WSL shell config = %+v", legacy)
	}
}

func TestGetShellEnvPrependsManagedBinOnce(t *testing.T) {
	agentDir := t.TempDir()
	binDir := filepath.Join(agentDir, "bin")
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
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

func TestResolveBashTimeoutUsesNodeMillisecondFloor(t *testing.T) {
	timeout := 0.0001
	duration, err := resolveBashTimeout(&timeout)
	if err != nil {
		t.Fatal(err)
	}
	if duration == nil || *duration != time.Millisecond {
		t.Fatalf("duration = %v, want 1ms", duration)
	}
}

func TestLocalBashOperationsUsesShellEnvWhenUnset(t *testing.T) {
	agentDir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", agentDir)
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
			"(sleep 0.5; printf survived > "+shellQuote(survivalFile)+") & printf ready > "+shellQuote(readyFile)+"; wait",
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

func localBashOperationsForTest(shell string) *localBashOperations {
	return &localBashOperations{resolveShell: func(string) (ShellConfig, error) {
		return ShellConfig{Shell: shell, Args: []string{"-c"}, CommandTransport: ShellCommandArgv}, nil
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
