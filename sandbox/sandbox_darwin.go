//go:build darwin

package sandbox

import (
	"fmt"
	"os"
	"strconv"
)

const sandboxExec = "/usr/bin/sandbox-exec"

func Probe() (int, Enforcement, error) {
	if _, err := os.Stat(sandboxExec); err != nil {
		return 0, EnforcementNone, fmt.Errorf("sandbox: sandbox-exec: %w", err)
	}
	return 1, EnforcementFull, nil
}

func wrap(mode Mode, root, _ string, shell, command string, env map[string]string) (string, map[string]string, Enforcement) {
	profile := "(version 1)\n(allow default)\n(deny file-write*)"
	if mode == ModeWorkspaceWrite {
		for _, allowed := range []string{root, os.TempDir(), "/dev"} {
			profile += "\n(allow file-write* (subpath " + strconv.Quote(allowed) + "))"
		}
	}
	env[EnvShell], env[EnvCommand], env["ORB_SANDBOX_PROFILE"] = shell, command, profile
	_, enforcement, _ := Probe()
	return `exec /usr/bin/sandbox-exec -p "$ORB_SANDBOX_PROFILE" "$ORB_SANDBOX_SHELL" -c "$ORB_SANDBOX_CMD"`, env, enforcement
}

func SelfRestrict(Mode, string) (Enforcement, error) {
	return EnforcementNone, fmt.Errorf("sandbox: SelfRestrict is only available on Linux")
}
