//go:build !windows

package tools

import (
	"context"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const exitStdioGrace = 100 * time.Millisecond

type ShellCommandTransport string

const (
	ShellCommandArgv  ShellCommandTransport = "argv"
	ShellCommandStdin ShellCommandTransport = "stdin"
)

type ShellConfig struct {
	Shell            string
	Args             []string
	CommandTransport ShellCommandTransport
}

func GetShellConfig(customShellPath string) (ShellConfig, error) {
	if customShellPath != "" {
		if _, err := os.Stat(customShellPath); err == nil {
			return bashShellConfig(customShellPath), nil
		}
		return ShellConfig{}, upstreamToolErrorf("Custom shell path not found: %s", customShellPath)
	}
	if _, err := os.Stat("/bin/bash"); err == nil {
		return bashShellConfig("/bin/bash"), nil
	}
	if shell := findBashOnPath(); shell != "" {
		return bashShellConfig(shell), nil
	}
	return ShellConfig{Shell: "sh", Args: []string{"-c"}}, nil
}

func bashShellConfig(shell string) ShellConfig {
	if isLegacyWSLBashPath(shell) {
		return ShellConfig{Shell: shell, Args: []string{"-s"}, CommandTransport: ShellCommandStdin}
	}
	return ShellConfig{Shell: shell, Args: []string{"-c"}}
}

func isLegacyWSLBashPath(shell string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(shell, "/", `\`))
	if len(normalized) < 3 || normalized[0] < 'a' || normalized[0] > 'z' || normalized[1] != ':' {
		return false
	}
	rest := normalized[2:]
	return rest == `\windows\system32\bash.exe` || rest == `\windows\sysnative\bash.exe`
}

func findBashOnPath() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "which", "bash").Output()
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return strings.TrimSuffix(first, "\r")
}

func GetShellEnv() (map[string]string, error) {
	environment := make(map[string]string)
	pathKey := "PATH"
	foundPathKey := false
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		environment[key] = value
		if !foundPathKey && strings.EqualFold(key, "PATH") {
			pathKey = key
			foundPathKey = true
		}
	}

	binDir, err := agentBinDir()
	if err != nil {
		return nil, err
	}
	currentPath := environment[pathKey]
	hasBinDir := false
	for _, entry := range filepath.SplitList(currentPath) {
		if entry == binDir {
			hasBinDir = true
			break
		}
	}
	if !hasBinDir {
		if currentPath == "" {
			environment[pathKey] = binDir
		} else {
			environment[pathKey] = binDir + string(os.PathListSeparator) + currentPath
		}
	}
	return environment, nil
}

func agentBinDir() (string, error) {
	return managedBinDir()
}

type localBashOperations struct {
	shellPath    string
	resolveShell func(string) (ShellConfig, error)
}

func NewLocalBashOperations(options ...LocalBashOperationsOptions) BashOperations {
	shellPath := ""
	if len(options) > 0 {
		shellPath = options[0].ShellPath
	}
	return &localBashOperations{shellPath: shellPath, resolveShell: GetShellConfig}
}

func (operations *localBashOperations) Exec(
	ctx context.Context,
	command string,
	cwd string,
	options BashExecOptions,
) (BashExecResult, error) {
	timeout, err := resolveBashTimeout(options.Timeout)
	if err != nil {
		return BashExecResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return BashExecResult{}, errors.New("aborted")
	}
	resolver := operations.resolveShell
	if resolver == nil {
		resolver = GetShellConfig
	}
	shell, err := resolver(operations.shellPath)
	if err != nil {
		return BashExecResult{}, err
	}
	if _, err := os.Stat(cwd); err != nil {
		return BashExecResult{}, upstreamToolErrorf("Working directory does not exist: %s\nCannot execute bash commands.", cwd)
	}

	args := append([]string(nil), shell.Args...)
	if shell.CommandTransport != ShellCommandStdin {
		args = append(args, command)
	}
	child := exec.Command(shell.Shell, args...)
	child.Dir = cwd
	environment := options.Env
	if environment == nil {
		environment, err = GetShellEnv()
		if err != nil {
			return BashExecResult{}, err
		}
	}
	child.Env = environmentList(environment)
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if shell.CommandTransport == ShellCommandStdin {
		child.Stdin = strings.NewReader(command)
	}

	stdoutRead, stdoutWrite, err := os.Pipe()
	if err != nil {
		return BashExecResult{}, err
	}
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		return BashExecResult{}, err
	}
	child.Stdout = stdoutWrite
	child.Stderr = stderrWrite
	if err := child.Start(); err != nil {
		_ = stdoutRead.Close()
		_ = stdoutWrite.Close()
		_ = stderrRead.Close()
		_ = stderrWrite.Close()
		return BashExecResult{}, formatSpawnError(shell.Shell, err)
	}
	_ = stdoutWrite.Close()
	_ = stderrWrite.Close()

	pid := child.Process.Pid
	TrackDetachedChildPID(pid)
	defer UntrackDetachedChildPID(pid)

	activity := make(chan struct{}, 64)
	readerDone := make(chan struct{}, 2)
	var onDataMu sync.Mutex
	callbackState := processPipeCallbackState{}
	readPipe := func(pipe *os.File) {
		defer func() { readerDone <- struct{}{} }()
		buffer := make([]byte, 32*1024)
		for {
			count, readErr := pipe.Read(buffer)
			if count > 0 {
				chunk := append([]byte(nil), buffer[:count]...)
				callbackState.begin()
				onDataMu.Lock()
				if options.OnData != nil {
					options.OnData(chunk)
				}
				onDataMu.Unlock()
				callbackState.end()
				select {
				case activity <- struct{}{}:
				default:
				}
			}
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) && !errors.Is(readErr, os.ErrClosed) {
					select {
					case activity <- struct{}{}:
					default:
					}
				}
				return
			}
		}
	}
	go readPipe(stdoutRead)
	go readPipe(stderrRead)

	const (
		stopNone int32 = iota
		stopAborted
		stopTimedOut
	)
	var stopReason atomic.Int32
	executionDone := make(chan struct{})
	var timeoutTimer *time.Timer
	if timeout != nil {
		timeoutTimer = time.NewTimer(*timeout)
	}
	go func() {
		var timeoutChannel <-chan time.Time
		if timeoutTimer != nil {
			timeoutChannel = timeoutTimer.C
		}
		select {
		case <-ctx.Done():
			if stopReason.CompareAndSwap(stopNone, stopAborted) {
				KillProcessTree(pid)
			}
		case <-timeoutChannel:
			if stopReason.CompareAndSwap(stopNone, stopTimedOut) {
				KillProcessTree(pid)
			}
		case <-executionDone:
		}
	}()
	if ctx.Err() != nil && stopReason.CompareAndSwap(stopNone, stopAborted) {
		KillProcessTree(pid)
	}

	waitErr := child.Wait()
	waitForProcessPipes(stdoutRead, stderrRead, activity, readerDone, &callbackState)
	close(executionDone)
	if timeoutTimer != nil && !timeoutTimer.Stop() {
		select {
		case <-timeoutTimer.C:
		default:
		}
	}

	if ctx.Err() != nil || stopReason.Load() == stopAborted {
		return BashExecResult{}, errors.New("aborted")
	}
	if stopReason.Load() == stopTimedOut {
		return BashExecResult{}, upstreamToolErrorf("timeout:%s", formatJSNumber(*options.Timeout))
	}
	if waitErr == nil {
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	}
	var exitError *exec.ExitError
	if errors.As(waitErr, &exitError) {
		code := exitError.ExitCode()
		if code < 0 {
			return BashExecResult{}, nil
		}
		return BashExecResult{ExitCode: &code}, nil
	}
	return BashExecResult{}, waitErr
}

func resolveBashTimeout(timeout *float64) (*time.Duration, error) {
	if timeout == nil {
		return nil, nil
	}
	if *timeout <= 0 || math.IsNaN(*timeout) || math.IsInf(*timeout, 0) {
		return nil, upstreamToolError("Invalid timeout: must be a finite number of seconds")
	}
	const maxTimeoutSeconds = 2_147_483_647.0 / 1000
	if *timeout > maxTimeoutSeconds {
		return nil, upstreamToolErrorf("Invalid timeout: maximum is %s seconds", formatJSNumber(maxTimeoutSeconds))
	}
	milliseconds := int64(*timeout * 1000)
	if milliseconds < 1 {
		milliseconds = 1
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	return &duration, nil
}

func environmentList(environment map[string]string) []string {
	if environment == nil {
		return nil
	}
	entries := make([]string, 0, len(environment))
	for key, value := range environment {
		entries = append(entries, key+"="+value)
	}
	return entries
}

func formatSpawnError(shell string, err error) error {
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
			return upstreamToolErrorf("spawn %s %s", shell, candidate.code)
		}
	}
	return err
}

func waitForProcessPipes(
	stdout *os.File,
	stderr *os.File,
	activity <-chan struct{},
	readerDone <-chan struct{},
	callbackState *processPipeCallbackState,
) {
	timer := time.NewTimer(exitStdioGrace)
	defer timer.Stop()
	readersFinished := 0
	for readersFinished < 2 {
		select {
		case <-readerDone:
			readersFinished++
		case <-activity:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(exitStdioGrace)
		case <-timer.C:
			if remaining := callbackState.remainingGrace(exitStdioGrace); remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			_ = stdout.Close()
			_ = stderr.Close()
			for readersFinished < 2 {
				<-readerDone
				readersFinished++
			}
			return
		}
	}
	_ = stdout.Close()
	_ = stderr.Close()
}

type processPipeCallbackState struct {
	sync.Mutex
	inFlight     int
	lastComplete time.Time
}

func (state *processPipeCallbackState) begin() {
	state.Lock()
	state.inFlight++
	state.Unlock()
}

func (state *processPipeCallbackState) end() {
	state.Lock()
	state.inFlight--
	state.lastComplete = time.Now()
	state.Unlock()
}

func (state *processPipeCallbackState) remainingGrace(grace time.Duration) time.Duration {
	state.Lock()
	defer state.Unlock()
	if state.inFlight > 0 {
		return grace
	}
	if state.lastComplete.IsZero() {
		return 0
	}
	remaining := grace - time.Since(state.lastComplete)
	if remaining < 0 {
		return 0
	}
	return remaining
}

var trackedDetachedChildren = struct {
	sync.Mutex
	pids map[int]struct{}
}{pids: make(map[int]struct{})}

func TrackDetachedChildPID(pid int) {
	trackedDetachedChildren.Lock()
	trackedDetachedChildren.pids[pid] = struct{}{}
	trackedDetachedChildren.Unlock()
}

func UntrackDetachedChildPID(pid int) {
	trackedDetachedChildren.Lock()
	delete(trackedDetachedChildren.pids, pid)
	trackedDetachedChildren.Unlock()
}

func KillTrackedDetachedChildren() {
	trackedDetachedChildren.Lock()
	pids := make([]int, 0, len(trackedDetachedChildren.pids))
	for pid := range trackedDetachedChildren.pids {
		pids = append(pids, pid)
	}
	clear(trackedDetachedChildren.pids)
	trackedDetachedChildren.Unlock()
	for _, pid := range pids {
		KillProcessTree(pid)
	}
}

func KillProcessTree(pid int) {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err == nil {
		return
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
}

func trackedDetachedChildCount() int {
	trackedDetachedChildren.Lock()
	defer trackedDetachedChildren.Unlock()
	return len(trackedDetachedChildren.pids)
}
