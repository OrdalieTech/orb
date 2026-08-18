// Package sandbox limits filesystem effects of child commands.
//
// Coverage is honest but partial by nature: Landlock does not mediate chmod,
// chown, xattr, utime, or fcntl metadata mutations, and old ABIs miss
// truncate. Enforcement is fail-closed: when the platform cannot sandbox,
// the wrapped command refuses to run (exit 126) with an actionable message
// instead of running unrestricted.
package sandbox

import "path/filepath"

type Mode string

const (
	ModeReadOnly         Mode = "read-only"
	ModeWorkspaceWrite   Mode = "workspace-write"
	ModeDangerFullAccess Mode = "danger-full-access"
)

const (
	EnvMode    = "ORB_SANDBOX_MODE"
	EnvRoot    = "ORB_SANDBOX_ROOT"
	EnvCommand = "ORB_SANDBOX_CMD"
	EnvSelf    = "ORB_SANDBOX_SELF"
	EnvShell   = "ORB_SANDBOX_SHELL"
)

// Wrap replaces command without requoting it through the platform launcher.
// Both restrictive modes keep /dev and os.TempDir() writable so ordinary
// shell idioms (2>/dev/null, mktemp) work; workspace-write additionally
// opens root.
func Wrap(mode Mode, root, shell, command string, env map[string]string) (string, map[string]string) {
	if env == nil {
		env = make(map[string]string)
	}
	if mode == ModeDangerFullAccess {
		return command, env
	}
	if mode != ModeReadOnly && mode != ModeWorkspaceWrite {
		mode = ModeReadOnly
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return wrap(mode, root, shell, command, env)
}

// refuse is the fail-closed wrapper: explain, then exit 126.
func refuse(reason string) string {
	return "echo 'orb: sandbox: " + reason + "; set plugins.permissions.sandbox to danger-full-access to run unsandboxed' >&2; exit 126"
}

func canonicalPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
