//go:build linux

package sandbox

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

const readAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR

func Probe() (int, Enforcement, error) {
	abi, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, EnforcementNone, fmt.Errorf("sandbox: probe Landlock: %w", errno)
	}
	enforcement := EnforcementPartial
	if abi >= 3 {
		enforcement = EnforcementFull
	}
	return int(abi), enforcement, nil
}

func wrap(mode Mode, root, self, shell, command string, env map[string]string) (string, map[string]string, Enforcement) {
	env[EnvMode], env[EnvRoot], env[EnvSelf] = string(mode), root, self
	env[EnvShell], env[EnvCommand] = shell, command
	_, enforcement, _ := Probe()
	return `exec "$ORB_SANDBOX_SELF" __sandbox`, env, enforcement
}

// SelfRestrict installs a Landlock ruleset on the calling thread and its children.
func SelfRestrict(mode Mode, root string) (Enforcement, error) {
	abi, enforcement, err := Probe()
	if err != nil {
		return EnforcementNone, err
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
	if abi >= 5 {
		writeAccess |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	attr := unix.LandlockRulesetAttr{Access_fs: uint64(readAccess) | writeAccess}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return EnforcementNone, fmt.Errorf("sandbox: create Landlock ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(ruleset)) }()
	paths := []string{"/"}
	if mode == ModeWorkspaceWrite {
		paths = append(paths, root, os.TempDir(), "/dev")
	} else if mode != ModeReadOnly {
		return EnforcementNone, fmt.Errorf("sandbox: invalid mode %q", mode)
	}
	for index, path := range paths {
		parent, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			return EnforcementNone, fmt.Errorf("sandbox: open %q: %w", path, err)
		}
		allowed := uint64(readAccess)
		if index > 0 {
			allowed |= writeAccess
		}
		pathAttr := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(parent)}
		_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&pathAttr)), 0, 0, 0)
		_ = unix.Close(parent)
		if errno != 0 {
			return EnforcementNone, fmt.Errorf("sandbox: allow %q: %w", path, errno)
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return EnforcementNone, fmt.Errorf("sandbox: no_new_privs: %w", err)
	}
	if _, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0); errno != 0 {
		return EnforcementNone, fmt.Errorf("sandbox: restrict self: %w", errno)
	}
	return enforcement, nil
}
