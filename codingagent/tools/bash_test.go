//go:build !windows

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/internal/truncate"
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

func TestBashToolSchemaBytesMatchUpstreamTypeBox(t *testing.T) {
	want := `{"type":"object","required":["command"],"properties":{"command":{"type":"string","description":"Bash command to execute"},"timeout":{"type":"number","description":"Timeout in seconds (optional, no default timeout)"}}}`
	if got := string(NewBashTool(t.TempDir(), nil).Spec().Parameters); got != want {
		t.Fatalf("schema = %s, want %s", got, want)
	}
}

func TestBashToolRunsLocalShellAndCommandPrefix(t *testing.T) {
	tool := NewBashTool(t.TempDir(), &BashToolOptions{CommandPrefix: "export PI_GO_BASH_TEST=prefix"})
	result, err := tool.Execute(context.Background(), "call", map[string]any{
		"command": "printf '%s' \"$PI_GO_BASH_TEST\"",
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
	var result agent.AgentToolResult
	var executeErr error
	go func() {
		defer close(done)
		result, executeErr = NewBashTool(dir, nil).Execute(
			context.Background(),
			"call",
			BashToolInput{Command: "printf x; sleep 0.4; printf y"},
			func(update agent.AgentToolResult) {
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
	var updates []agent.AgentToolResult
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(),
		"call",
		BashToolInput{Command: "chatty"},
		func(update agent.AgentToolResult) { updates = append(updates, update) },
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

func TestBashToolFormatsAbortTimeoutAndExitErrors(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		executeErr error
		exitCode   *int
		want       string
	}{
		{name: "abort", executeErr: errors.New("aborted"), want: "before\n\nCommand aborted"},
		{name: "timeout", executeErr: errors.New("timeout:1.5"), want: "before\n\nCommand timed out after 1.5 seconds"},
		{name: "timeout extra fields", executeErr: errors.New("timeout:1:extra"), want: "before\n\nCommand timed out after 1 seconds"},
		{name: "exit", exitCode: intPointer(7), want: "before\n\nCommand exited with code 7"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			operations := bashOperationsFunc(func(
				_ context.Context,
				_ string,
				_ string,
				options BashExecOptions,
			) (BashExecResult, error) {
				options.OnData([]byte("before"))
				return BashExecResult{ExitCode: testCase.exitCode}, testCase.executeErr
			})
			_, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
				context.Background(), "call", BashToolInput{Command: "failure"}, nil,
			)
			if err == nil || err.Error() != testCase.want {
				t.Fatalf("error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestBashToolReturnsNoOutputAndTreatsNullExitAsSuccess(t *testing.T) {
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		_ BashExecOptions,
	) (BashExecResult, error) {
		return BashExecResult{}, nil
	})
	result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "empty"}, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := bashResultText(t, result); got != "(no output)" {
		t.Fatalf("output = %q", got)
	}
	if result.Details != nil {
		t.Fatalf("details = %#v", result.Details)
	}
}

func TestBashToolPreservesUnknownOperationError(t *testing.T) {
	want := errors.New("remote transport failed")
	operations := bashOperationsFunc(func(
		_ context.Context,
		_ string,
		_ string,
		options BashExecOptions,
	) (BashExecResult, error) {
		options.OnData([]byte("discarded from error"))
		return BashExecResult{}, want
	})
	_, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
		context.Background(), "call", BashToolInput{Command: "failure"}, nil,
	)
	if !errors.Is(err, want) || err.Error() != want.Error() {
		t.Fatalf("error = %v, want original error", err)
	}
}

func TestBashToolFormatsByteAndPartialLineTruncation(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		output string
		check  func(testing.TB, string, BashToolDetails)
	}{
		{
			name:   "complete lines",
			output: strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 600)+"\n", 100), "\n"),
			check: func(t testing.TB, text string, details BashToolDetails) {
				t.Helper()
				truncation := details.Truncation
				start := truncation.TotalLines - truncation.OutputLines + 1
				footer := "[Showing lines " + strconv.Itoa(start) + "-100 of 100 (50.0KB limit). Full output: " + details.FullOutputPath + "]"
				if !strings.HasSuffix(text, footer) || truncation.LastLinePartial {
					t.Fatalf("text footer/details mismatch: %q, %+v", text[len(text)-min(len(text), len(footer)+10):], truncation)
				}
			},
		},
		{
			name:   "partial last line",
			output: strings.Repeat("y", 60*1024),
			check: func(t testing.TB, text string, details BashToolDetails) {
				t.Helper()
				footer := "[Showing last 50.0KB of line 1 (line is 60.0KB). Full output: " + details.FullOutputPath + "]"
				if !strings.HasSuffix(text, footer) || !details.Truncation.LastLinePartial {
					t.Fatalf("text footer/details mismatch: suffix=%q details=%+v", text[len(text)-min(len(text), len(footer)+10):], details.Truncation)
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			operations := bashOperationsFunc(func(
				_ context.Context,
				_ string,
				_ string,
				options BashExecOptions,
			) (BashExecResult, error) {
				options.OnData([]byte(testCase.output))
				return BashExecResult{ExitCode: intPointer(0)}, nil
			})
			result, err := NewBashTool(t.TempDir(), &BashToolOptions{Operations: operations}).Execute(
				context.Background(), "call", BashToolInput{Command: "large"}, nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			details, ok := result.Details.(BashToolDetails)
			if !ok || details.Truncation == nil || details.FullOutputPath == "" {
				t.Fatalf("details = %#v", result.Details)
			}
			t.Cleanup(func() { _ = os.Remove(details.FullOutputPath) })
			testCase.check(t, bashResultText(t, result), details)
		})
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

func TestBashToolPlainTextRenderHooks(t *testing.T) {
	renderer := NewBashTool(t.TempDir(), nil).(PlainTextRenderer)
	if got := renderer.RenderCall(map[string]any{"command": "echo hi", "timeout": 1.5}); got != "$ echo hi (timeout 1.5s)" {
		t.Fatalf("RenderCall() = %q", got)
	}
	if got := renderer.RenderCall(map[string]any{}); got != "$ ..." {
		t.Fatalf("RenderCall(empty) = %q", got)
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

func TestGetShellConfigOmitsNormalTransportLikeUpstream(t *testing.T) {
	config, err := GetShellConfig("/bin/bash")
	if err != nil {
		t.Fatal(err)
	}
	if config.CommandTransport != "" || len(config.Args) != 1 || config.Args[0] != "-c" {
		t.Fatalf("config = %+v", config)
	}
	stdin := bashShellConfig(`C:\Windows\System32\bash.exe`)
	if stdin.CommandTransport != ShellCommandStdin || len(stdin.Args) != 1 || stdin.Args[0] != "-s" {
		t.Fatalf("legacy WSL config = %+v", stdin)
	}
}

func TestGetShellEnvNormalizesFileURLAgentDir(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", "file://"+filepath.ToSlash(dir))
	environment := mustShellEnv(t)
	pathValue := environment["PATH"]
	if got := filepath.SplitList(pathValue)[0]; got != filepath.Join(dir, "bin") {
		t.Fatalf("managed bin PATH entry = %q, want %q", got, filepath.Join(dir, "bin"))
	}
}

func TestGetShellEnvRejectsInvalidFileURLAgentDir(t *testing.T) {
	t.Setenv("PI_CODING_AGENT_DIR", "file://remote/tmp/pi")
	_, err := GetShellEnv()
	if err == nil || !strings.Contains(err.Error(), `File URL host must be "localhost" or empty`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLocalBashOperationsMapsExecutableFormatError(t *testing.T) {
	dir := t.TempDir()
	shell := filepath.Join(dir, "invalid-shell")
	if err := os.WriteFile(shell, []byte("not an executable format"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := NewLocalBashOperations(LocalBashOperationsOptions{ShellPath: shell}).Exec(
		context.Background(), "true", dir, BashExecOptions{Env: mustShellEnv(t)},
	)
	want := "spawn " + shell + " ENOEXEC"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestLocalBashOperationsValidatesTimeoutAndCwd(t *testing.T) {
	operations := NewLocalBashOperations()
	for _, timeout := range []float64{0, -1, math.Inf(1)} {
		_, err := operations.Exec(context.Background(), "true", t.TempDir(), BashExecOptions{Timeout: &timeout})
		if err == nil || !strings.Contains(err.Error(), "Invalid timeout") {
			t.Fatalf("timeout %v error = %v", timeout, err)
		}
	}
	tooLarge := 2_147_483_647.0/1000 + 1
	_, err := operations.Exec(context.Background(), "true", t.TempDir(), BashExecOptions{Timeout: &tooLarge})
	if err == nil || err.Error() != "Invalid timeout: maximum is 2147483.647 seconds" {
		t.Fatalf("max timeout error = %v", err)
	}
	_, err = operations.Exec(context.Background(), "true", filepath.Join(t.TempDir(), "missing"), BashExecOptions{})
	if err == nil || !strings.Contains(err.Error(), "Working directory does not exist") {
		t.Fatalf("cwd error = %v", err)
	}
}

func TestLocalBashOperationsTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "child.pid")
	timeout := 0.2
	command := "sleep 60 & child=$!; printf '%s' \"$child\" > " + shellQuote(pidFile) + "; wait"
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

func TestLocalBashOperationsCapturesActiveInheritedStdioPastGrace(t *testing.T) {
	dir := t.TempDir()
	var output strings.Builder
	var outputMu sync.Mutex
	startedAt := time.Now()
	result, err := NewLocalBashOperations().Exec(
		context.Background(),
		`(for value in {1..30}; do printf 'chunk-%s\n' "$value"; sleep 0.01; done) &`,
		dir,
		BashExecOptions{
			Env: mustShellEnv(t),
			OnData: func(data []byte) {
				outputMu.Lock()
				output.Write(data)
				outputMu.Unlock()
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode != 0 {
		t.Fatalf("result = %+v", result)
	}
	elapsed := time.Since(startedAt)
	outputMu.Lock()
	text := output.String()
	outputMu.Unlock()
	if !strings.Contains(text, "chunk-1\n") || !strings.Contains(text, "chunk-30\n") {
		t.Fatalf("active inherited output = %q", text)
	}
	if elapsed < 200*time.Millisecond {
		t.Fatalf("operation returned after %s before active descendant output settled", elapsed)
	}
}

func TestLocalBashOperationsReleasesQuietInheritedStdioAfterGrace(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "quiet-child.pid")
	command := "sleep 60 & child=$!; printf '%s' \"$child\" > " + shellQuote(pidFile) + "; printf parent-exiting"
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
		"head -c 131072 /dev/zero &",
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

func intPointer(value int) *int {
	return &value
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
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

func bashResultText(t *testing.T, result agent.AgentToolResult) string {
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
