package harness

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func requireExecutionErrorCode(t testing.TB, err error, code ExecutionErrorCode) *ExecutionError {
	t.Helper()
	var typed *ExecutionError
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatalf("error = %v, want ExecutionError(%s)", err, code)
	}
	return typed
}

func TestNodeExecutionEnvShellParityAndFailures(t *testing.T) {
	RequireProcesses(t)
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: root, ShellEnv: map[string]string{"BASE": "base"}}
	ctx := context.Background()
	var callbackMu strings.Builder
	result, err := env.Exec(ctx, `printf "$BASE:$EXTRA"; printf err >&2; exit 7`, ExecOptions{
		Env: map[string]string{"EXTRA": "extra"},
		OnStdout: func(chunk string) error {
			_, _ = callbackMu.WriteString("stdout:" + chunk)
			return nil
		},
		OnStderr: func(chunk string) error {
			_, _ = callbackMu.WriteString("stderr:" + chunk)
			return nil
		},
	})
	if err != nil || result.Stdout != "base:extra" || result.Stderr != "err" || result.ExitCode != 7 {
		t.Fatalf("Exec = %#v, %v", result, err)
	}
	if callbacks := callbackMu.String(); !strings.Contains(callbacks, "stdout:base:extra") || !strings.Contains(callbacks, "stderr:err") {
		t.Fatalf("callbacks = %q", callbacks)
	}

	for _, timeout := range []float64{0, -1, math.NaN(), math.Inf(1), maxExecutionTimeoutSeconds + 1} {
		if _, err := env.Exec(ctx, "true", ExecOptions{TimeoutSeconds: &timeout}); err == nil {
			t.Fatalf("timeout %v succeeded", timeout)
		} else {
			_ = requireExecutionErrorCode(t, err, ExecutionErrorTimeout)
		}
	}
	tiny := 0.01
	if _, err := env.Exec(ctx, "sleep 5", ExecOptions{TimeoutSeconds: &tiny}); err == nil {
		t.Fatal("timed execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorTimeout)
	}
	if _, err := env.Exec(ctx, "printf boom", ExecOptions{OnStdout: func(string) error { return errors.New("callback boom") }}); err == nil {
		t.Fatal("callback failure succeeded")
	} else if typed := requireExecutionErrorCode(t, err, ExecutionErrorCallback); typed.Error() != "callback boom" {
		t.Fatalf("callback error = %q", typed.Error())
	}
	aborted, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := env.Exec(aborted, "printf never", ExecOptions{}); err == nil {
		t.Fatal("pre-aborted execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorAborted)
	}
	live, cancelLive := context.WithCancel(ctx)
	if _, err := env.Exec(live, "printf ready; sleep 5", ExecOptions{OnStdout: func(string) error {
		cancelLive()
		return nil
	}}); err == nil {
		t.Fatal("cancelled live execution succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorAborted)
	}

	missing := NodeExecutionEnv{CWD: root, ShellPath: "missing-shell"}
	if _, err := missing.Exec(ctx, "true", ExecOptions{}); err == nil {
		t.Fatal("missing shell succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorShellUnavailable)
	}
	notExecutable := filepath.Join(root, "not-executable")
	if err := os.WriteFile(notExecutable, []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	spawnFailure := NodeExecutionEnv{CWD: root, ShellPath: notExecutable}
	if _, err := spawnFailure.Exec(ctx, "true", ExecOptions{}); err == nil {
		t.Fatal("non-executable shell succeeded")
	} else {
		_ = requireExecutionErrorCode(t, err, ExecutionErrorSpawn)
	}
}

func TestNodeExecutionEnvReportsMissingWorkingDirectoryBeforeSpawn(t *testing.T) {
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: filepath.Join(root, "missing")}
	_, err := env.Exec(context.Background(), "printf ok", ExecOptions{})
	if err == nil {
		t.Fatal("missing working directory succeeded")
	}
	typed := requireExecutionErrorCode(t, err, ExecutionErrorSpawn)
	if !strings.Contains(typed.Error(), "Working directory does not exist") {
		t.Fatalf("missing working directory error = %q", typed.Error())
	}
}

func TestNodeExecutionEnvSettlesAfterExitWhenDescendantRetainsStdio(t *testing.T) {
	RequireProcesses(t)
	if runtime.GOOS == "windows" {
		t.Skip("the drain-grace command is a POSIX script")
	}
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: root}
	started := time.Now()
	type execOutcome struct {
		result ExecResult
		err    error
	}
	outcome := make(chan execOutcome, 1)
	go func() {
		result, err := env.Exec(context.Background(), "sleep 5 & echo child-exiting", ExecOptions{})
		outcome <- execOutcome{result: result, err: err}
	}()
	select {
	case settled := <-outcome:
		if settled.err != nil || !strings.Contains(settled.result.Stdout, "child-exiting") {
			t.Fatalf("Exec = %#v, %v", settled.result, settled.err)
		}
		if elapsed := time.Since(started); elapsed >= 4*time.Second {
			t.Fatalf("exec settled after %s, want the post-exit stdio grace", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not settle after the shell exited")
	}
}

func TestNodeExecutionEnvCleanupTerminatesActiveShellProcesses(t *testing.T) {
	RequireProcesses(t)
	if runtime.GOOS == "windows" {
		t.Skip("the cleanup command is a POSIX script")
	}
	root := t.TempDir()
	env := NodeExecutionEnv{CWD: root}
	ctx := context.Background()
	type execOutcome struct {
		result ExecResult
		err    error
	}
	outcome := make(chan execOutcome, 1)
	go func() {
		result, err := env.Exec(ctx, "touch started; sleep 60", ExecOptions{})
		outcome <- execOutcome{result: result, err: err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		exists, existsErr := env.Exists(ctx, "started")
		if existsErr != nil {
			t.Fatal(existsErr)
		}
		if exists {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("shell process never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := env.Cleanup(); err != nil {
		t.Fatal(err)
	}
	select {
	case settled := <-outcome:
		if settled.err != nil {
			t.Fatalf("Exec after cleanup = %v", settled.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not terminate the active shell process")
	}
}
