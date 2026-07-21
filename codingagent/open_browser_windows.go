package codingagent

import (
	"os/exec"
	"syscall"
)

const detachedProcess = 0x00000008

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | detachedProcess,
	}
}
