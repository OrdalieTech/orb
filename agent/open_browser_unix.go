//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package agent

import (
	"os/exec"
	"syscall"
)

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
