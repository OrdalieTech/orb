//go:build windows

package harness

import (
	"os"
	"os/exec"
	"strconv"
	"syscall"
)

func configureProcessTree(*exec.Cmd) {}

func killProcessTree(process *os.Process) {
	if process == nil {
		return
	}
	command := exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(process.Pid))
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	_ = command.Run()
	_ = process.Kill()
}
