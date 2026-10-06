//go:build linux || darwin

package subagents

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/OrdalieTech/orb/internal/proctree"
	"github.com/OrdalieTech/orb/platforms/native/sandbox"
)

// runExternalCommand runs command in its own process group. The wrapper
// reports the command's status on fd 3 and then outlives it, so the group
// (every descendant included) is still addressable when it is killed.
func runExternalCommand(ctx context.Context, cwd, command string, env map[string]string, mode sandbox.Mode, stdin io.Reader, stdout, stderr io.Writer) (externalRun, error) {
	command, env = sandbox.Wrap(mode, cwd, "/bin/sh", command, env)
	process := exec.CommandContext(ctx, "/bin/sh", "-c", `/bin/sh -c "$1" 3>&-; status=$?; printf '%d\n' "$status" >&3; while :; do sleep 3600; done`, "orb-subagent", command)
	// WaitDelay only fires when an escaped descendant still holds the pipes
	// after the group kill; keep it generous so a loaded host never truncates
	// a successful child's final output burst.
	process.Dir, process.Stdin, process.WaitDelay = cwd, stdin, 5*time.Second
	for name, value := range env {
		process.Env = append(process.Env, name+"="+value)
	}
	proctree.Isolate(process)
	process.Cancel = func() error {
		proctree.Kill(process.Process.Pid)
		return nil
	}
	process.Stdout, process.Stderr = stdout, stderr
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		return externalRun{}, err
	}
	defer func() { _ = statusReader.Close() }()
	process.ExtraFiles = []*os.File{statusWriter}
	if err = process.Start(); err != nil {
		_ = statusWriter.Close()
		return externalRun{}, err
	}
	_ = statusWriter.Close()
	var run externalRun
	_, run.statusErr = fmt.Fscan(statusReader, &run.status)
	_ = process.Cancel()
	run.waitErr = process.Wait()
	return run, nil
}
