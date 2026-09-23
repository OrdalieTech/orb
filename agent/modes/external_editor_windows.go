package modes

import (
	"os"
	"os/exec"
	"syscall"
)

// externalEditorProcess runs the editor through the command interpreter the
// way upstream's spawn(..., {shell: true}) does on win32: Node passes
// `/d /s /c "<command>"` to %ComSpec% verbatim, so cmd.exe, not the Windows
// argv quoting rules, parses the editor command line. The prompt path is
// quoted because a profile directory may contain spaces.
func externalEditorProcess(command, filePath string) *exec.Cmd {
	shell := os.Getenv("ComSpec")
	if shell == "" {
		shell = "cmd.exe"
	}
	process := exec.Command(shell)
	process.SysProcAttr = &syscall.SysProcAttr{CmdLine: shell + ` /d /s /c "` + command + ` "` + filePath + `""`}
	return process
}
