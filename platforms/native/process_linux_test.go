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
	if os.Getenv("ORB_TEST_WRITER") != "" {
		time.Sleep(time.Minute)
		return
	}
	// The writer is this test binary run under the name orb: a copy of sleep would
	// exit at once on Ubuntu, whose uutils sleep dispatches on its binary's name.
	test, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	orb := filepath.Join(t.TempDir(), "orb")
	if err := os.Symlink(test, orb); err != nil {
		t.Fatal(err)
	}
	agentDir, otherDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	start := func(dir string) *exec.Cmd {
		process := exec.Command(orb, "-test.run=^TestMigrationGuardReadsProcfsWithoutPS$")
		process.Env = append(os.Environ(), "ORB_TEST_WRITER=1", config.EnvAgentDir+"="+dir)
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
