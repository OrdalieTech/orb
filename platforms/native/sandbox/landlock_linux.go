//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const readAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

func wrap(mode Mode, root, shell, command string, env map[string]string) (string, map[string]string) {
	launcher := fmt.Sprintf("/proc/%d/exe", os.Getpid())
	if _, err := os.Stat(launcher); err != nil {
		return refuse("/proc is unavailable"), env
	}
	env[EnvMode], env[EnvRoot], env[EnvSelf] = string(mode), root, launcher
	env[EnvShell], env[EnvCommand] = shell, command
	return `exec "$ORB_SANDBOX_SELF" __sandbox`, env
}

// SelfRestrict installs a Landlock ruleset on the calling OS thread and its
// children. The caller must hold runtime.LockOSThread and exec immediately
// after: Landlock and no_new_privs bind to the thread, not the process.
func SelfRestrict(mode Mode, root string) error {
	if mode != ModeReadOnly && mode != ModeWorkspaceWrite {
		return fmt.Errorf("sandbox: invalid mode %q", mode)
	}
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return fmt.Errorf("sandbox: Landlock is unavailable on this kernel (%v); set plugins.permissions.sandbox to %q to run unsandboxed", errno, ModeDangerFullAccess)
	}
	writeAccess := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	deviceAccess := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
	if abi >= 5 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
		deviceAccess |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	attr := unix.LandlockRulesetAttr{Access_fs: uint64(readAccess) | writeAccess}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("sandbox: create Landlock ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()
	type grant struct {
		path    string
		allowed uint64
	}
	grants := []grant{{"/", uint64(readAccess)}, {os.TempDir(), uint64(readAccess) | writeAccess}, {"/dev", uint64(readAccess) | deviceAccess}}
	if mode == ModeWorkspaceWrite {
		grants = append(grants, grant{root, uint64(readAccess) | writeAccess})
	}
	for _, grant := range grants {
		parent, err := unix.Open(grant.path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("sandbox: open %q: %w", grant.path, err)
		}
		pathAttr := unix.LandlockPathBeneathAttr{Allowed_access: grant.allowed, Parent_fd: int32(parent)}
		_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&pathAttr)), 0, 0, 0)
		_ = unix.Close(parent)
		if errno != 0 {
			return fmt.Errorf("sandbox: allow %q: %w", grant.path, errno)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("sandbox: no_new_privs: %w", err)
	}
	if _, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return fmt.Errorf("sandbox: restrict self: %w", errno)
	}
	return nil
}
