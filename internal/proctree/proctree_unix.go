//go:build !windows && !wasm

package proctree

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Isolate starts cmd in a new session, so it leads a process group Kill can reach.
func Isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// Kill kills the process group led by pid, which Isolate started; a group
// already gone is os.ErrProcessDone. It never signals pid alone: after the
// leader is reaped that pid may be reused.
func Kill(pid int) error {
	err := syscall.Kill(-pid, syscall.SIGKILL)
	// ESRCH: the group is gone. EPERM: on darwin, signalling a group whose
	// leader is already a zombie reports EPERM; nothing is left to kill either way.
	if errors.Is(err, syscall.ESRCH) || errors.Is(err, syscall.EPERM) {
		return os.ErrProcessDone
	}
	return err
}

func defaultShell(func(string) string) (Shell, error) {
	if _, err := os.Stat("/bin/bash"); err == nil {
		return bashShell("/bin/bash"), nil
	}
	if shell := which("bash"); shell != "" {
		return bashShell(shell), nil
	}
	return Shell{Path: "sh", Args: []string{"-c"}}, nil
}

// which trusts which(1) output without a stat so Termux and special filesystems still resolve.
func which(executable string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "which", executable).Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return strings.TrimSuffix(first, "\r")
}

// SpawnErrorCode is the code Node reports for a failed spawn ("ENOENT"), or "".
func SpawnErrorCode(err error) string {
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
