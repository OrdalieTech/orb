//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWrapUsesEnvironmentTransport(t *testing.T) {
	command := `printf '%s' "$HOME"; echo $(date)`
	wrapper, env := Wrap(ModeReadOnly, "/workspace", "", command, nil)
	if wrapper != `exec "$ORB_SANDBOX_SELF" __sandbox` || env[EnvCommand] != command || env[EnvShell] != "/bin/sh" || env[EnvRoot] != "/workspace" ||
		env[EnvSelf] != fmt.Sprintf("/proc/%d/exe", os.Getpid()) {
		t.Fatalf("wrapper=%q env=%v", wrapper, env)
	}
	if got, err := canonicalPath("/proc/self"); err != nil || got == "/proc/self" {
		t.Fatalf("canonical path = %q, %v", got, err)
	}
}

// The child half runs restricted: read-only must deny the DAC-writable test
// directory while keeping /dev/null and os.TempDir() usable.
func TestSelfRestrictReadOnly(t *testing.T) {
	if os.Getenv("ORB_LANDLOCK_TEST") == "1" {
		runtime.LockOSThread()
		if err := SelfRestrict(ModeReadOnly, ""); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("ORB_LANDLOCK_DENIED"), "denied"), []byte("x"), 0o600); err == nil {
			os.Exit(3)
		}
		if err := os.WriteFile("/dev/null", []byte("x"), 0o600); err != nil {
			os.Exit(4)
		}
		scratch, err := os.CreateTemp("", "orb-sandbox-*")
		if err != nil {
			os.Exit(5)
		}
		_ = scratch.Close()
		_ = os.Remove(scratch.Name())
		os.Exit(0)
	}
	// The denied probe must sit outside os.TempDir() (writable by design), so
	// use the package directory, which plain DAC would let us write.
	denied, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestSelfRestrictReadOnly$")
	command.Env = append(os.Environ(), "ORB_LANDLOCK_TEST=1", "ORB_LANDLOCK_DENIED="+denied)
	output, err := command.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 2 {
		t.Skipf("landlock unavailable: %s", output)
	}
	if err != nil {
		t.Fatalf("restricted child: %v: %s", err, output)
	}
}
