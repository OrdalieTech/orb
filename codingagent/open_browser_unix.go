//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package codingagent

import (
	"os/exec"
	"syscall"
)

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
