// Package sandbox limits filesystem effects of child commands.
package sandbox

type Mode string

const (
	ModeReadOnly         Mode = "read-only"
	ModeWorkspaceWrite   Mode = "workspace-write"
	ModeDangerFullAccess Mode = "danger-full-access"
)

type Enforcement string

const (
	EnforcementNone    Enforcement = "none"
	EnforcementPartial Enforcement = "partial"
	EnforcementFull    Enforcement = "full"
)

const (
	EnvMode    = "ORB_SANDBOX_MODE"
	EnvRoot    = "ORB_SANDBOX_ROOT"
	EnvCommand = "ORB_SANDBOX_CMD"
	EnvSelf    = "ORB_SANDBOX_SELF"
	EnvShell   = "ORB_SANDBOX_SHELL"
)

// Wrap replaces command with the platform sandbox launcher. The original
// command travels through env so shell quoting cannot change it.
func Wrap(mode Mode, root, self, shell, command string, env map[string]string) (string, map[string]string, Enforcement) {
	if env == nil {
		env = make(map[string]string)
	}
	if mode == ModeDangerFullAccess {
		return command, env, EnforcementNone
	}
	if mode != ModeReadOnly && mode != ModeWorkspaceWrite {
		mode = ModeReadOnly
	}
	if shell == "" {
		shell = "/bin/sh"
	}
	return wrap(mode, root, self, shell, command, env)
}
