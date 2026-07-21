package codingagent

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const detachedBrowserHelperEnv = "PIGO_TEST_DETACHED_BROWSER_HELPER"

// LOG-M1: the launcher returns while its child remains alive in a separate OS
// session, the observable POSIX effect of spawn({ detached: true }).unref().
func TestLOGM1BrowserLauncherDetachesChildSession(t *testing.T) {
	if os.Getenv(detachedBrowserHelperEnv) != "" {
		path := os.Getenv(detachedBrowserHelperEnv)
		if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Second)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidPath := t.TempDir() + "/pid"
	t.Setenv(detachedBrowserHelperEnv, pidPath)
	started := time.Now()
	launchDetached(executable, []string{"-test.run=^TestLOGM1BrowserLauncherDetachesChildSession$"})
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("detached launcher blocked for %v", elapsed)
	}

	var pid int
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(pidPath)
		if readErr == nil {
			pid, err = strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if pid == 0 {
		t.Fatal("detached child did not remain alive long enough to publish its pid")
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = process.Kill() })

	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	closeParen := strings.LastIndexByte(string(stat), ')')
	if closeParen < 0 {
		t.Fatalf("invalid child stat: %q", stat)
	}
	fields := strings.Fields(string(stat[closeParen+1:]))
	if len(fields) < 4 {
		t.Fatalf("invalid child stat fields: %q", stat)
	}
	sessionID, err := strconv.Atoi(fields[3])
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != pid {
		t.Fatalf("detached child session = %d, want child pid %d", sessionID, pid)
	}
	for descriptor := 0; descriptor <= 2; descriptor++ {
		target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/fd/" + strconv.Itoa(descriptor))
		if err != nil {
			t.Fatal(err)
		}
		if target != "/dev/null" {
			t.Fatalf("detached child fd %d = %q, want ignored stdio via /dev/null", descriptor, target)
		}
	}
}
