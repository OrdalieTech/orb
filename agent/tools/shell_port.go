package tools

import (
	"context"
	"errors"

	"github.com/OrdalieTech/orb/engine/harness"
)

// ShellBashOperations adapts the Exec port (DECISIONS.md P10) to the bash
// tool. Timeouts keep the "timeout:<seconds>" message the tool renders, and
// cancellation keeps the bare "aborted" message of the local backend.
func ShellBashOperations(shell harness.Shell) BashOperations { return shellBash{shell: shell} }

type shellBash struct{ shell harness.Shell }

func (operations shellBash) Exec(ctx context.Context, command, cwd string, options BashExecOptions) (BashExecResult, error) {
	forward := func(text string) error {
		if options.OnData != nil {
			options.OnData([]byte(text))
		}
		return nil
	}
	result, err := operations.shell.Exec(ctx, command, harness.ExecOptions{
		CWD: cwd, Env: options.Env, TimeoutSeconds: options.Timeout, OnStdout: forward, OnStderr: forward,
	})
	var executionError *harness.ExecutionError
	if errors.As(err, &executionError) && executionError.Code == harness.ExecutionErrorAborted {
		return BashExecResult{}, errors.New("aborted")
	}
	if err != nil {
		return BashExecResult{}, err
	}
	exitCode := result.ExitCode
	return BashExecResult{ExitCode: &exitCode}, nil
}
