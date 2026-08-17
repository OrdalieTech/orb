//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestWrapUsesEnvironmentTransport(t *testing.T) {
	command := `printf '%s' "$HOME"; echo $(date)`
	wrapper, env, enforcement := Wrap(ModeReadOnly, "/workspace", "/bin/orb", "", command, nil)
	if wrapper != `exec "$ORB_SANDBOX_SELF" __sandbox` || env[EnvCommand] != command || env[EnvShell] != "/bin/sh" || env[EnvRoot] != "/workspace" {
		t.Fatalf("wrapper=%q env=%v", wrapper, env)
	}
	_, want, err := Probe()
	if err != nil {
		want = EnforcementNone
	}
	if enforcement != want {
		t.Fatalf("enforcement = %q, want %q", enforcement, want)
	}
}

func TestSelfRestrictReadOnly(t *testing.T) {
	if os.Getenv("ORB_LANDLOCK_TEST") == "1" {
		enforcement, err := SelfRestrict(ModeReadOnly, os.Getenv("ORB_LANDLOCK_ROOT"))
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(2)
		}
		if err := os.WriteFile(filepath.Join(os.Getenv("ORB_LANDLOCK_ROOT"), "denied"), []byte("x"), 0o600); err == nil {
			os.Exit(3)
		}
		fmt.Print(enforcement)
		os.Exit(0)
	}
	if _, _, err := Probe(); err != nil {
		t.Skip(err)
	}
	root := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestSelfRestrictReadOnly$")
	command.Env = append(os.Environ(), "ORB_LANDLOCK_TEST=1", "ORB_LANDLOCK_ROOT="+root)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("restricted child: %v: %s", err, output)
	}
	if string(output) != string(EnforcementFull) && string(output) != string(EnforcementPartial) {
		t.Fatalf("enforcement = %q", output)
	}
}
