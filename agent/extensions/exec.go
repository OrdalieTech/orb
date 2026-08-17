package extensions

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// execKillGrace mirrors upstream execCommand (core/exec.ts): SIGTERM first,
// SIGKILL only if the process survives 5 seconds.
const execKillGrace = 5 * time.Second

func Exec(ctx context.Context, command string, args []string, options *ExecOptions) (ExecResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if options == nil {
		options = &ExecOptions{}
	}
	if options.Context != nil {
		ctx = options.Context
	}
	if options.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(options.Timeout)*time.Millisecond)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Cancel = func() error {
		return cmd.Process.Signal(syscall.SIGTERM)
	}
	cmd.WaitDelay = execKillGrace
	cmd.Dir = options.CWD
	if len(options.Env) > 0 {
		cmd.Env = options.Env
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	result := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		result.Code = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		result.Killed = true
		// Upstream reports the child's own exit code when it exits during the
		// SIGTERM grace, and 0 when the signal (or forced SIGKILL) ends it.
		result.Code = 0
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			result.Code = cmd.ProcessState.ExitCode()
		}
		return result, nil
	}
	if err == nil {
		return result, nil
	}
	// The child exited but a detached grandchild still holds the stdio pipes;
	// upstream's waitForChildProcess resolves with the child's own code here.
	if errors.Is(err, exec.ErrWaitDelay) {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.Code = exitError.ExitCode()
		return result, nil
	}
	result.Code = 1
	return result, nil
}
