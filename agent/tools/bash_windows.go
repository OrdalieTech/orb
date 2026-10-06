package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/OrdalieTech/orb/internal/proctree"
)

// ShellCommandOutput ports upstream resolve-config-value's win32 executor: the
// configured bash when one resolves and spawns, otherwise child_process.execSync's
// default shell, cmd.exe /d /s /c "command" with the command line passed verbatim.
// ComSpec is not consulted; its default is the System32 cmd.exe used here.
func ShellCommandOutput(ctx context.Context, command string) (string, bool) {
	if shell, err := GetShellConfig(""); err == nil {
		child := exec.CommandContext(ctx, shell.Path, shell.Args...)
		if shell.Stdin {
			child.Stdin = strings.NewReader(command)
		} else {
			child.Args = append(child.Args, command)
		}
		proctree.Isolate(child)
		var stdout strings.Builder
		child.Stdout = &stdout
		err := child.Start()
		if err == nil {
			// stdout is complete only once Wait has drained the pipe.
			waitErr := child.Wait()
			return stdout.String(), waitErr == nil
		}
		if proctree.SpawnErrorCode(err) != "ENOENT" {
			return "", false
		}
	}
	root, ok := os.LookupEnv("SystemRoot")
	if !ok {
		root = `C:\Windows`
	}
	shell := filepath.Join(root, "System32", "cmd.exe")
	child := exec.CommandContext(ctx, shell)
	child.SysProcAttr = &syscall.SysProcAttr{CmdLine: shell + ` /d /s /c "` + command + `"`}
	var stdout strings.Builder
	child.Stdout = &stdout
	if err := child.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}
