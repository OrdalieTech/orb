// Package proctree runs a shell command as the root of its own process tree,
// streams its output, and kills the whole tree on cancellation, timeout or a
// failed output callback. It is the one shell executor behind the bash tool
// and the harness Exec port.
package proctree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// exitStdioGrace bounds how long a run waits for stdio to drain after the
// shell exits while a detached descendant still holds the inherited pipes.
const exitStdioGrace = 100 * time.Millisecond

// Shell is a resolved shell: Path run with Args and then the command, or with
// the command on stdin when Stdin is set (the legacy WSL bash).
type Shell struct {
	Path  string
	Args  []string
	Stdin bool
}

// FindShell resolves upstream's bash: custom when it is set, else the
// platform default, reading ProgramFiles through getenv on win32.
func FindShell(custom string, getenv func(string) string) (Shell, error) {
	if custom == "" {
		return defaultShell(getenv)
	}
	if _, err := os.Stat(custom); err != nil {
		return Shell{}, fmt.Errorf("Custom shell path not found: %s", custom) //nolint:staticcheck // Upstream error text is observable.
	}
	return bashShell(custom, getenv), nil
}

func bashShell(path string, getenv func(string) string) Shell {
	windowsPath := func(value string) string {
		return strings.TrimRight(strings.ToLower(strings.ReplaceAll(value, "/", `\`)), `\`)
	}
	normalized := windowsPath(path)
	// The legacy WSL launcher, in %SystemRoot% wherever Windows is installed; C:\Windows only when unknown.
	root := ""
	if getenv != nil {
		root = windowsPath(getenv("SystemRoot"))
	}
	if root == "" && len(normalized) > 2 && normalized[0] >= 'a' && normalized[0] <= 'z' {
		root = normalized[:2] + `\windows`
	}
	if len(root) > 2 && root[1] == ':' && (normalized == root+`\system32\bash.exe` || normalized == root+`\sysnative\bash.exe`) {
		return Shell{Path: path, Args: []string{"-s"}, Stdin: true}
	}
	return Shell{Path: path, Args: []string{"-c"}}
}

// Kind classifies a failed run.
type Kind int

const (
	Spawn Kind = iota
	Aborted
	Timeout
)

// Error is a failed run; its text is upstream's.
type Error struct {
	Kind Kind
	Err  error
	// ShellExit is set on a timeout that hit after the shell itself exited, while background
	// processes it started kept writing to its output.
	ShellExit *ShellExit
}

// ShellExit is the shell's own exit: its status and when it exited after the start.
type ShellExit struct {
	Code  int
	After time.Duration
}

func (err *Error) Error() string { return err.Err.Error() }
func (err *Error) Unwrap() error { return err.Err }

func failure(kind Kind, format string, args ...any) error {
	return &Error{Kind: kind, Err: fmt.Errorf(format, args...)}
}

// Command is one run of a shell script.
type Command struct {
	Script string
	Dir    string
	Env    []string
	// Timeout is in seconds; nil means none.
	Timeout *float64
	// Shell resolves the shell once the timeout and context are checked.
	Shell func() (Shell, error)
	// Started receives the pid of the shell, which leads the tree.
	Started func(pid int)
	// OnData receives each output chunk, one call at a time; an error kills
	// the tree and becomes the run's error.
	OnData func(stderr bool, chunk []byte) error
}

// Run runs command to completion and returns the shell's exit status, 128
// plus the signal number when a signal ended it.
func Run(ctx context.Context, command Command) (int, error) {
	timeout, err := timeoutDuration(command.Timeout)
	if err != nil {
		return 0, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return 0, failure(Aborted, "aborted")
	}
	shell, err := command.Shell()
	if err != nil {
		return 0, err
	}
	if _, err := os.Stat(command.Dir); err != nil {
		return 0, failure(Spawn, "Working directory does not exist: %s\nCannot execute bash commands.", command.Dir)
	}
	args := slices.Clone(shell.Args)
	if !shell.Stdin {
		args = append(args, command.Script)
	}
	child := exec.Command(shell.Path, args...)
	child.Dir, child.Env = command.Dir, command.Env
	Isolate(child)
	if shell.Stdin {
		child.Stdin = strings.NewReader(command.Script)
	}
	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return 0, &Error{Kind: Spawn, Err: err}
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_, _ = stdoutRead.Close(), stdoutWrite.Close()
		return 0, &Error{Kind: Spawn, Err: err}
	}
	child.Stdout, child.Stderr = stdoutWrite, stderrWrite
	err = child.Start()
	_, _ = stdoutWrite.Close(), stderrWrite.Close()
	if err != nil {
		_, _ = stdoutRead.Close(), stderrRead.Close()
		if code := SpawnErrorCode(err); code != "" {
			return 0, failure(Spawn, "spawn %s %s", shell.Path, code)
		}
		return 0, &Error{Kind: Spawn, Err: err}
	}
	pid, startedAt := child.Process.Pid, time.Now()
	// Set before the shell has had time to start the command's own processes,
	// which inherit it.
	expendable(pid)
	if command.Started != nil {
		command.Started(pid)
	}

	const (
		running int32 = iota
		aborted
		timedOut
		callbackFailed
	)
	var stop atomic.Int32
	kill := func(reason int32) {
		if stop.CompareAndSwap(running, reason) {
			_ = Kill(pid)
		}
	}
	var callbackErr error
	pipes := &pipeReaders{activity: make(chan struct{}, 64), done: make(chan struct{}, 2)}
	onData := func(stderr bool, chunk []byte) {
		if command.OnData != nil {
			if err := command.OnData(stderr, chunk); err != nil && callbackErr == nil {
				callbackErr = err
				kill(callbackFailed)
			}
		}
	}
	go pipes.read(stdoutRead, false, onData)
	go pipes.read(stderrRead, true, onData)

	finished := make(chan struct{})
	var expired <-chan time.Time
	if timeout != nil {
		timer := time.NewTimer(*timeout)
		defer timer.Stop()
		expired = timer.C
	}
	var shellExited, exitedBeforeDeadline atomic.Bool
	go func() {
		select {
		case <-ctx.Done():
			kill(aborted)
		case <-expired:
			exitedBeforeDeadline.Store(shellExited.Load())
			// Also after the shell exited: the group kill still reaches the background processes
			// holding its pipes.
			kill(timedOut)
		case <-finished:
		}
	}()
	waitErr := child.Wait()
	shellExited.Store(true)
	exitedAfter := time.Since(startedAt)
	pipes.wait(stdoutRead, stderrRead)
	close(finished)

	code, err := exitStatus(waitErr)
	switch {
	case stop.Load() == callbackFailed:
		return 0, callbackErr
	case ctx.Err() != nil || stop.Load() == aborted:
		return 0, failure(Aborted, "aborted")
	case stop.Load() == timedOut:
		seconds, _ := json.Marshal(*command.Timeout)
		timeoutErr := &Error{Kind: Timeout, Err: fmt.Errorf("timeout:%s", seconds)}
		if exitedBeforeDeadline.Load() && err == nil {
			timeoutErr.ShellExit = &ShellExit{Code: code, After: exitedAfter}
		}
		return 0, timeoutErr
	}
	return code, err
}

// exitStatus is the shell's exit status, 128 plus the signal number when a signal ended it.
func exitStatus(waitErr error) (int, error) {
	var exitErr *exec.ExitError
	if waitErr == nil {
		return 0, nil
	}
	if !errors.As(waitErr, &exitErr) {
		return 0, &Error{Kind: Spawn, Err: waitErr}
	}
	code := exitErr.ExitCode()
	if code < 0 {
		code = 1
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			code = 128 + int(status.Signal())
		}
	}
	return code, nil
}

func timeoutDuration(seconds *float64) (*time.Duration, error) {
	if seconds == nil {
		return nil, nil
	}
	if *seconds <= 0 || math.IsNaN(*seconds) || math.IsInf(*seconds, 0) {
		return nil, failure(Timeout, "Invalid timeout: must be a finite number of seconds")
	}
	if *seconds > 2_147_483.647 {
		return nil, failure(Timeout, "Invalid timeout: maximum is 2147483.647 seconds")
	}
	duration := max(time.Duration(*seconds*1000)*time.Millisecond, time.Millisecond)
	return &duration, nil
}

// pipeReaders drains the shell's stdout and stderr, delivering chunks one at
// a time, and tracks callbacks still running so the exit grace never expires
// under a slow consumer.
type pipeReaders struct {
	activity chan struct{}
	done     chan struct{}
	deliver  sync.Mutex

	mu           sync.Mutex
	inFlight     int
	lastComplete time.Time
}

func (pipes *pipeReaders) read(pipe *os.File, stderr bool, onData func(bool, []byte)) {
	defer func() { pipes.done <- struct{}{} }()
	buffer := make([]byte, 32*1024)
	for {
		count, err := pipe.Read(buffer)
		if count > 0 {
			pipes.mu.Lock()
			pipes.inFlight++
			pipes.mu.Unlock()
			pipes.deliver.Lock()
			onData(stderr, append([]byte(nil), buffer[:count]...))
			pipes.deliver.Unlock()
			pipes.mu.Lock()
			pipes.inFlight--
			pipes.lastComplete = time.Now()
			pipes.mu.Unlock()
			pipes.signal()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				pipes.signal()
			}
			return
		}
	}
}

func (pipes *pipeReaders) signal() {
	select {
	case pipes.activity <- struct{}{}:
	default:
	}
}

// wait returns once both readers finish: at end of stream, or after the exit
// grace passes quietly, when it closes the pipes a detached descendant holds.
// Bytes still unread then mean a starved reader, not a quiet pipe: closing
// would drop the shell's last output.
func (pipes *pipeReaders) wait(stdout, stderr *os.File) {
	timer := time.NewTimer(exitStdioGrace)
	defer timer.Stop()
	finished := 0
	for finished < 2 {
		select {
		case <-pipes.done:
			finished++
		case <-pipes.activity:
			timer.Reset(exitStdioGrace)
		case <-timer.C:
			if remaining := pipes.remainingGrace(); remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			if unread(stdout) || unread(stderr) {
				timer.Reset(exitStdioGrace)
				continue
			}
			_, _ = stdout.Close(), stderr.Close()
			for ; finished < 2; finished++ {
				<-pipes.done
			}
		}
	}
	_, _ = stdout.Close(), stderr.Close()
}

func (pipes *pipeReaders) remainingGrace() time.Duration {
	pipes.mu.Lock()
	defer pipes.mu.Unlock()
	if pipes.inFlight > 0 {
		return exitStdioGrace
	}
	if pipes.lastComplete.IsZero() {
		return 0
	}
	return max(exitStdioGrace-time.Since(pipes.lastComplete), 0)
}
