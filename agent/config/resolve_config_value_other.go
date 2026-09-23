//go:build !windows

package config

import (
	"bytes"
	"context"
	"os/exec"
)

// shellCommandOutput is child_process.execSync's POSIX default shell.
func shellCommandOutput(ctx context.Context, command string) (string, bool) {
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	process.Stdin = nil
	var stdout bytes.Buffer
	process.Stdout = &stdout
	process.Stderr = nil
	if err := process.Run(); err != nil {
		return "", false
	}
	return stdout.String(), true
}
