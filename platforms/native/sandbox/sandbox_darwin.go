//go:build darwin

package sandbox

import (
	"fmt"
	"os"
	"strconv"
)

const sandboxExec = "/usr/bin/sandbox-exec"

func wrap(mode Mode, root, shell, command string, env map[string]string) (string, map[string]string) {
	if _, err := os.Stat(sandboxExec); err != nil {
		return refuse("sandbox-exec is missing"), env
	}
	writable := []string{os.TempDir(), "/dev"}
	if mode == ModeWorkspaceWrite {
		writable = append(writable, root)
	}
	profile := "(version 1)\n(allow default)\n(deny file-write*)"
	for _, allowed := range writable {
		allowed, err := canonicalPath(allowed)
		if err != nil {
			return refuse("cannot resolve a writable path"), env
		}
		profile += "\n(allow file-write* (subpath " + strconv.Quote(allowed) + "))"
	}
	env[EnvShell], env[EnvCommand], env["ORB_SANDBOX_PROFILE"] = shell, command, profile
	return `exec /usr/bin/sandbox-exec -p "$ORB_SANDBOX_PROFILE" "$ORB_SANDBOX_SHELL" -c "$ORB_SANDBOX_CMD"`, env
}

func SelfRestrict(Mode, string) error {
	return fmt.Errorf("sandbox: SelfRestrict requires Linux")
}
