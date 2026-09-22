//go:build !windows && !wasm

package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func defaultShellConfig() (ShellConfig, error) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return bashShellConfig("/bin/bash"), nil
	}
	if shell := findExecutableOnPath("bash"); shell != "" {
		return bashShellConfig(shell), nil
	}
	return ShellConfig{Shell: "sh", Args: []string{"-c"}}, nil
}

// Unix trusts which(1) output without a stat so Termux and special filesystems still resolve.
func findExecutableOnPath(executable string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "which", executable).Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return strings.TrimSuffix(first, "\r")
}

func configureShellProcess(child *exec.Cmd) {
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func spawnErrorCode(err error) string {
	for _, candidate := range []struct {
		err  error
		code string
	}{
		{syscall.E2BIG, "E2BIG"},
		{syscall.EACCES, "EACCES"},
		{syscall.ELOOP, "ELOOP"},
		{syscall.ENAMETOOLONG, "ENAMETOOLONG"},
		{syscall.ENOENT, "ENOENT"},
		{syscall.ENOEXEC, "ENOEXEC"},
		{syscall.ENOTDIR, "ENOTDIR"},
	} {
		if errors.Is(err, candidate.err) {
			return candidate.code
		}
	}
	return ""
}

func KillProcessTree(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}
