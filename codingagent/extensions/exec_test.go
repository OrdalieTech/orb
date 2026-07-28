package extensions

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Regression: Exec used to SIGKILL immediately on timeout/abort; upstream
// execCommand sends SIGTERM first and SIGKILLs only after a 5s grace, so
// children can trap TERM and clean up.
func TestExecTimeoutSendsSIGTERMBeforeKill(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGTERM trap test requires a POSIX shell")
	}
	marker := filepath.Join(t.TempDir(), "term-marker")
	script := `trap 'kill "$child" 2>/dev/null; echo trapped > "$1"; exit 7' TERM; sleep 30 & child=$!; wait "$child"`
	start := time.Now()
	result, err := Exec(t.Context(), "sh", []string{"-c", script, "sh", marker}, &ExecOptions{Timeout: 300})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Killed {
		t.Fatalf("result = %#v, want Killed", result)
	}
	if result.Code != 7 {
		t.Fatalf("exit code = %d, want the trap's own exit code 7", result.Code)
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil {
		t.Fatalf("SIGTERM trap never ran (immediate SIGKILL?): %v", readErr)
	}
	if string(content) != "trapped\n" {
		t.Fatalf("marker content = %q", content)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("Exec took %v; graceful TERM exit should not wait for the kill grace", elapsed)
	}
}

func TestExecBackgroundGrandchildKeepsChildExitCode(t *testing.T) {
	start := time.Now()
	result, err := Exec(context.Background(), "sh", []string{"-c", "sleep 30 & echo started; exit 0"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Upstream waitForChildProcess reports the child's own code even when a
	// detached grandchild still holds the stdio pipes.
	if result.Code != 0 {
		t.Fatalf("exit code = %d, want the child's own 0 despite the pipe-holding grandchild", result.Code)
	}
	if result.Killed {
		t.Fatalf("result = %#v, want not Killed", result)
	}
	if result.Stdout != "started\n" {
		t.Fatalf("stdout = %q", result.Stdout)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("Exec took %v; the bounded WaitDelay is 5s", elapsed)
	}
}
