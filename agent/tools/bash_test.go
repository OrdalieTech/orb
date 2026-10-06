//go:build !windows && !wasm

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/truncate"
)

type bashOperationsFunc func(context.Context, string, string, BashExecOptions) (BashExecResult, error)

func trackedDetachedChildCount() int {
	trackedDetachedChildren.Lock()
	defer trackedDetachedChildren.Unlock()
	return len(trackedDetachedChildren.pids)
}

func (function bashOperationsFunc) Exec(
	ctx context.Context,
	command string,
	cwd string,
	options BashExecOptions,
) (BashExecResult, error) {
	return function(ctx, command, cwd, options)
}

func TestBashToolRunsLocalShellAndCommandPrefix(t *testing.T) {
	tool := NewBashTool(t.TempDir(), &BashToolOptions{CommandPrefix: "export ORB_BASH_TEST=prefix"})
	result, err := tool.Execute(context.Background(), "call", map[string]any{
		"command": "printf '%s' \"$ORB_BASH_TEST\"",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := bashResultText(t, result); got != "prefix" {
		t.Fatalf("output = %q", got)
	}
	if result.Details != nil {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestBashToolStreamsLocalOutputBeforeCommandCompletes(t *testing.T) {
	dir := t.TempDir()
	updates := make(chan string, 8)
	done := make(chan struct{})
	var result engine.AgentToolResult
	var executeErr error
	go func() {
		defer close(done)
		result, executeErr = NewBashTool(dir, nil).Execute(
			context.Background(),
			"call",
			BashToolInput{Command: "printf x; sleep 0.4; printf y"},
			func(update engine.AgentToolResult) {
				updates <- bashResultText(t, update)
			},
		)
	}()

	deadline := time.After(300 * time.Millisecond)
	sawFirstByte := false
	for !sawFirstByte {
		select {
		case update := <-updates:
			sawFirstByte = update == "x"
		case <-done:
			t.Fatal("command completed before its first output update was observed")
		case <-deadline:
			t.Fatal("first output was not streamed while the command was running")
		}
	}
	select {
	case <-done:
		t.Fatal("command completed before the streaming assertion")
	default:
	}
	<-done
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	if got := bashResultText(t, result); got != "xy" {
		t.Fatalf("final output = %q", got)
	}
}

func TestBashToolSpawnHookMutatesCommandCwdAndEnv(t *testing.T) {
	firstDir := t.TempDir()
	secondDir := t.TempDir()
	var captured BashSpawnContext
	var hookInput BashSpawnContext
	operations := bashOperationsFunc(func(
		_ context.Context,
		command string,
		cwd string,
		options BashExecOptions,
	) (BashExecResult, error) {
		captured = BashSpawnContext{Command: command, Cwd: cwd, Env: options.Env}
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	})
	tool := NewBashTool(firstDir, &BashToolOptions{
		Operations:    operations,
		CommandPrefix: "prefix",
		ShellPath:     "/unused/custom/shell",
		SpawnHook: func(context BashSpawnContext) BashSpawnContext {
			hookInput = context
			context.Command = "rewritten"
			context.Cwd = secondDir
			context.Env = map[string]string{"ONLY": "hook"}
			return context
		},
	})
	if _, err := tool.Execute(context.Background(), "call", BashToolInput{Command: "original"}, nil); err != nil {
		t.Fatal(err)
	}
	if captured.Command != "rewritten" || captured.Cwd != secondDir || captured.Env["ONLY"] != "hook" {
		t.Fatalf("spawn context = %+v", captured)
	}
	if hookInput.Command != "prefix\noriginal" || hookInput.Cwd != firstDir || hookInput.Env["PATH"] == "" {
		t.Fatalf("spawn hook input = %+v", hookInput)
	}
}

func TestBashToolSessionEnvironmentExposureAndOptOut(t *testing.T) {
	t.Setenv("PI_SESSION_ID", "stale-session")
	t.Setenv("PI_REASONING_LEVEL", "stale-level")
	captureEnv := func(target *map[string]string) BashOperations {
		return bashOperationsFunc(func(
			_ context.Context,
			_ string,
			_ string,
			options BashExecOptions,
		) (BashExecResult, error) {
			*target = options.Env
			code := 0
			return BashExecResult{ExitCode: &code}, nil
		})
	}
	source := func() *BashSessionEnvironment {
		return &BashSessionEnvironment{
			SessionID:      "session-1",
			SessionFile:    "/sessions/session-1.jsonl",
			Provider:       "anthropic",
			Model:          "claude-sonnet-4-5",
			ReasoningLevel: "high",
		}
	}
	sessionKeys := []string{"PI_SESSION_ID", "PI_SESSION_FILE", "PI_PROVIDER", "PI_MODEL", "PI_REASONING_LEVEL"}

	var sessionEnv map[string]string
	tool := NewBashTool(t.TempDir(), &BashToolOptions{Operations: captureEnv(&sessionEnv)})
	tool.(BashSessionEnvironmentBinder).BindSessionEnvironment(source)
	if _, err := tool.Execute(context.Background(), "call", BashToolInput{Command: "printf ok"}, nil); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"PI_SESSION_ID":      "session-1",
		"PI_SESSION_FILE":    "/sessions/session-1.jsonl",
		"PI_PROVIDER":        "anthropic",
		"PI_MODEL":           "claude-sonnet-4-5",
		"PI_REASONING_LEVEL": "high",
	} {
		if sessionEnv[key] != want {
			t.Fatalf("%s = %q, want %q", key, sessionEnv[key], want)
		}
	}

	optOut := false
	var optedOutEnv map[string]string
	optedOutTool := NewBashTool(t.TempDir(), &BashToolOptions{
		Operations: captureEnv(&optedOutEnv), ExposeSessionEnvironment: &optOut,
	})
	optedOutTool.(BashSessionEnvironmentBinder).BindSessionEnvironment(source)
	if _, err := optedOutTool.Execute(context.Background(), "call", BashToolInput{Command: "printf ok"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range sessionKeys {
		if value, exists := optedOutEnv[key]; exists {
			t.Fatalf("opted-out env still has %s=%q", key, value)
		}
	}

	// A tool without a bound session still scrubs ambient PI_* variables.
	var unboundEnv map[string]string
	unboundTool := NewBashTool(t.TempDir(), &BashToolOptions{Operations: captureEnv(&unboundEnv)})
	if _, err := unboundTool.Execute(context.Background(), "call", BashToolInput{Command: "printf ok"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, key := range sessionKeys {
		if value, exists := unboundEnv[key]; exists {
			t.Fatalf("unbound env still has %s=%q", key, value)
		}
	}
}

func TestBashToolEmitsInitialAndIncrementalUpdates(t *testing.T) {
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		options.OnData([]byte("first\n"))
		for range 5000 {
			options.OnData([]byte("chatty\n"))
		}
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	})
	var updates []engine.AgentToolResult
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(),
		"call",
		BashToolInput{Command: "chatty"},
		func(update engine.AgentToolResult) { updates = append(updates, update) },
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) < 2 || len(updates[0].Content) != 0 || updates[0].Details != nil {
		t.Fatalf("updates = %#v", updates)
	}
	initialJSON, err := json.Marshal(updates[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(initialJSON) != `{"content":[]}` {
		t.Fatalf("initial update JSON = %s", initialJSON)
	}
	if len(updates) >= 25 {
		t.Fatalf("received %d updates, want fewer than 25", len(updates))
	}
	if !strings.Contains(bashResultText(t, result), "chatty") {
		t.Fatalf("result = %q", bashResultText(t, result))
	}
	details, ok := result.Details.(BashToolDetails)
	if !ok || details.Truncation == nil || !details.Truncation.Truncated || details.FullOutputPath == "" {
		t.Fatalf("details = %#v", result.Details)
	}
	t.Cleanup(func() { _ = os.Remove(details.FullOutputPath) })
}

func TestBashToolIgnoresLateOutputCallbacks(t *testing.T) {
	lateDone := make(chan struct{})
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		options.OnData([]byte("before\n"))
		go func() {
			time.Sleep(time.Millisecond)
			options.OnData([]byte("late\n"))
			close(lateDone)
		}()
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	})
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "late"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	<-lateDone
	if got := strings.TrimSpace(bashResultText(t, result)); got != "before" {
		t.Fatalf("output = %q", got)
	}
}

func TestBashToolTruncatedAbortIncludesUsableFullOutputPath(t *testing.T) {
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		for line := 1; line <= 3000; line++ {
			options.OnData([]byte(strconv.Itoa(line) + "\n"))
		}
		return BashExecResult{}, errors.New("aborted")
	})
	_, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "chatty"}, nil,
	)
	if err == nil || !strings.HasSuffix(err.Error(), "Command aborted") {
		t.Fatalf("error = %v", err)
	}
	marker := "Full output: "
	start := strings.LastIndex(err.Error(), marker)
	if start == -1 {
		t.Fatalf("missing full-output footer: %v", err)
	}
	path := err.Error()[start+len(marker):]
	path = strings.TrimSuffix(strings.SplitN(path, "]", 2)[0], " ")
	t.Cleanup(func() { _ = os.Remove(path) })
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read full output: %v", readErr)
	}
	if !strings.HasPrefix(string(data), "1\n2\n3\n") || !strings.HasSuffix(string(data), "2998\n2999\n3000\n") {
		t.Fatalf("full output boundaries missing: %d bytes", len(data))
	}
}

func TestLocalBashOperationsSupportsArgvAndStdinTransport(t *testing.T) {
	for _, transport := range []ShellCommandTransport{ShellCommandArgv, ShellCommandStdin} {
		t.Run(string(transport), func(t *testing.T) {
			args := []string{"-c"}
			if transport == ShellCommandStdin {
				args = []string{"-s"}
			}
			operations := &localBashOperations{resolveShell: func(string) (ShellConfig, error) {
				return ShellConfig{Shell: "/bin/bash", Args: args, CommandTransport: transport}, nil
			}}
			var output strings.Builder
			result, err := operations.Exec(context.Background(), "printf transport", t.TempDir(), BashExecOptions{
				OnData: func(data []byte) { output.Write(data) },
				Env:    mustShellEnv(t),
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode == nil || *result.ExitCode != 0 || output.String() != "transport" {
				t.Fatalf("result = %+v, output = %q", result, output.String())
			}
		})
	}
}

func TestLocalBashOperationsTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	timeout := 0.2
	command := "sleep 60 & child=$!; printf '%s' \"$child\" > " + shellSingleQuote(pidFile) + "; wait"
	_, executeErr := NewLocalBashOperations().Exec(context.Background(), command, dir, BashExecOptions{
		Timeout: &timeout,
		Env:     mustShellEnv(t),
	})
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if executeErr == nil || executeErr.Error() != "timeout:0.2" {
		t.Fatalf("error = %v", executeErr)
	}
	deadline := time.Now().Add(2 * time.Second)
	for processExists(pid) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("child process %d survived process-group kill", pid)
	}
	if count := trackedDetachedChildCount(); count != 0 {
		t.Fatalf("tracked detached children = %d", count)
	}
}

func TestLocalBashOperationsTracksDetachedProcessUntilItSettles(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	dir := t.TempDir()
	ready := make(chan struct{})
	done := make(chan error, 1)
	var once sync.Once
	go func() {
		_, err := NewLocalBashOperations().Exec(ctx, "printf ready; sleep 60", dir, BashExecOptions{
			Env: mustShellEnv(t),
			OnData: func([]byte) {
				once.Do(func() { close(ready) })
			},
		})
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("command did not start")
	}
	if count := trackedDetachedChildCount(); count != 1 {
		cancel()
		t.Fatalf("tracked detached children while running = %d", count)
	}
	cancel()
	if err := <-done; err == nil || err.Error() != "aborted" {
		t.Fatalf("error = %v", err)
	}
	if count := trackedDetachedChildCount(); count != 0 {
		t.Fatalf("tracked detached children after completion = %d", count)
	}
}

func TestWaitForProcessPipesRearmsActiveGrace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stdout, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		stderr, err := os.Open(os.DevNull)
		if err != nil {
			t.Fatal(err)
		}
		activity := make(chan struct{})
		readerDone := make(chan struct{})
		done := make(chan struct{})
		go func() {
			waitForProcessPipes(stdout, stderr, activity, readerDone, &processPipeCallbackState{})
			close(done)
		}()
		synctest.Wait()
		for range 3 {
			time.Sleep(exitStdioGrace - time.Nanosecond)
			activity <- struct{}{}
		}
		readerDone <- struct{}{}
		readerDone <- struct{}{}
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("pipe wait did not finish")
		}
	})
}

func TestLocalBashOperationsReleasesQuietInheritedStdioAfterGrace(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "quiet-child.pid")
	command := "sleep 60 & child=$!; printf '%s' \"$child\" > " + shellSingleQuote(pidFile) + "; printf parent-exiting"
	var output strings.Builder
	startedAt := time.Now()
	result, err := NewLocalBashOperations().Exec(context.Background(), command, dir, BashExecOptions{
		Env:    mustShellEnv(t),
		OnData: func(data []byte) { output.Write(data) },
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(startedAt)
	pidBytes, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	if result.ExitCode == nil || *result.ExitCode != 0 || output.String() != "parent-exiting" {
		t.Fatalf("result = %+v, output = %q", result, output.String())
	}
	if elapsed < exitStdioGrace || elapsed > time.Second {
		t.Fatalf("quiet inherited stdio released after %s", elapsed)
	}
}

func TestLocalBashOperationsDoesNotExpireGraceDuringSlowOutputCallback(t *testing.T) {
	dir := t.TempDir()
	var first sync.Once
	var outputBytes atomic.Int64
	result, err := NewLocalBashOperations().Exec(
		context.Background(),
		// The shell exits once the background writer runs (the fifo), and the
		// writer is a builtin: its output starts at once instead of after a
		// program starts, which a loaded machine can delay past the grace.
		`mkfifo started; { printf x >started; printf '%0131072d' 0; } & cat started >/dev/null`,
		dir,
		BashExecOptions{
			Env: mustShellEnv(t),
			OnData: func(data []byte) {
				first.Do(func() { time.Sleep(150 * time.Millisecond) })
				outputBytes.Add(int64(len(data)))
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got := outputBytes.Load(); got != 131072 {
		t.Fatalf("captured %d bytes after slow callback, want 131072", got)
	}
}

func TestBashToolConcurrentOutputCallbacksAreRaceSafe(t *testing.T) {
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		var group sync.WaitGroup
		for range 100 {
			group.Add(1)
			go func() {
				defer group.Done()
				options.OnData([]byte("line\n"))
			}()
		}
		group.Wait()
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	})
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "parallel"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(bashResultText(t, result), "line\n"); got != 100 {
		t.Fatalf("line count = %d", got)
	}
}

func processExists(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func TestBashToolTruncationLineCountsExcludeTrailingNewline(t *testing.T) {
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		for line := 1; line <= 4000; line++ {
			options.OnData([]byte("line-" + strconv.Itoa(line) + "\n"))
		}
		code := 0
		return BashExecResult{ExitCode: &code}, nil
	})
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "many-lines"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	details := result.Details.(BashToolDetails)
	t.Cleanup(func() { _ = os.Remove(details.FullOutputPath) })
	if details.Truncation.TotalLines != 4000 || details.Truncation.OutputLines != truncate.DefaultMaxLines {
		t.Fatalf("truncation = %+v", details.Truncation)
	}
	if !strings.Contains(bashResultText(t, result), "[Showing lines 2001-4000 of 4000. Full output:") {
		t.Fatalf("output footer = %q", bashResultText(t, result))
	}
	fullOutput, err := os.ReadFile(details.FullOutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(fullOutput), "line-1\nline-2\n") || !strings.HasSuffix(string(fullOutput), "line-3999\nline-4000\n") {
		t.Fatalf("full output boundaries are missing: %d bytes", len(fullOutput))
	}
}

func bashResultText(t *testing.T, result engine.AgentToolResult) string {
	t.Helper()
	parts := make([]string, 0, len(result.Content))
	for _, block := range result.Content {
		text, ok := block.(*ai.TextContent)
		if !ok {
			t.Fatalf("content block = %T, want *ai.TextContent", block)
		}
		parts = append(parts, text.Text)
	}
	return strings.Join(parts, "\n")
}

func mustShellEnv(t *testing.T) map[string]string {
	t.Helper()
	environment, err := GetShellEnv()
	if err != nil {
		t.Fatal(err)
	}
	return environment
}
