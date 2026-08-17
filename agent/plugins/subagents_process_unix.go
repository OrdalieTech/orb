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
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return process.Cancel, nil
}
