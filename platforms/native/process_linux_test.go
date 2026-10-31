package native

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
)

func TestMigrationGuardReadsProcfsWithoutPS(t *testing.T) {
	sleep, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep is unavailable")
	}
	data, err := os.ReadFile(sleep)
	if err != nil {
		t.Fatal(err)
	}
	orb := filepath.Join(t.TempDir(), "orb")
	if err := os.WriteFile(orb, data, 0o700); err != nil {
		t.Fatal(err)
	}
	agentDir, otherDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	start := func(dir string) *exec.Cmd {
		process := exec.Command(orb, "30")
		process.Env = append(os.Environ(), config.EnvAgentDir+"="+dir)
		if err := process.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = process.Process.Kill(); _ = process.Wait() })
		// Start returns once the child has passed exec's point of no return, before
		// the kernel fills in its environment; wait for it so the process is a writer.
		for environ := filepath.Join("/proc", strconv.Itoa(process.Process.Pid), "environ"); ; time.Sleep(time.Millisecond) {
			if data, err := os.ReadFile(environ); err != nil || len(data) > 0 {
				break
			}
		}
		return process
	}
	t.Setenv("PATH", t.TempDir())

	writer := start(agentDir)
	err = RequireOfflineMigration(context.Background(), agentDir)
	if err == nil || !strings.Contains(err.Error(), "close other Orb processes") {
		t.Fatalf("writer on the same state root: %v", err)
	}
	_ = writer.Process.Kill()
	_ = writer.Wait()

	start(otherDir)
	if err := RequireOfflineMigration(context.Background(), agentDir); err != nil {
		t.Fatalf("writer on another state root blocked migration: %v", err)
	}
}
