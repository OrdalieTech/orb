//go:build linux || darwin

package plugins

import (
	"os"
	"os/exec"
	"syscall"
)

func isolateExternalProcess(process *exec.Cmd) (func() error, error) {
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	process.Cancel = func() error {
		if process.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-process.Process.Pid, syscall.SIGKILL)
		// ESRCH: the group is gone. EPERM: on darwin, signalling a group whose
		// leader is already a zombie (the exec watchdog killed it first)
		// reports EPERM — there is nothing left to clean up either way.
		if err == syscall.ESRCH || err == syscall.EPERM {
			return os.ErrProcessDone
		}
		return err
	}
	return process.Cancel, nil
}
