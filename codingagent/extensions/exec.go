package extensions

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

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
		result.Code = 0
		return result, nil
	}
	if err == nil {
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
