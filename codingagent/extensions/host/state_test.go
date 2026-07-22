package host

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/tools"
)

func TestStateSnapshotActionsEventBusAndToolCallVeto(t *testing.T) {
	pigoExecutable := filepath.Join(t.TempDir(), "pigo")
	writeExecutable(t, pigoExecutable, "#!/bin/sh\nprintf '%s\\n' 'pigo fixture-version'\n")
	_, registry, result, cwd := startStateFixtureManager(t, pigoExecutable)
	if len(result.Diagnostics) != 0 || len(result.Errors) != 0 {
		t.Fatalf("load result = %#v", result)
	}

	var sessionMu sync.RWMutex
	sessionName := "runtime-initial"
	messages := make(chan string, 12)
	setNames := make(chan string, 2)
	aborted := make(chan struct{}, 1)
	errorsSeen := make(chan extensions.ExtensionError, 2)
	callbackSignalContext, cancelCallbackSignal := context.WithCancel(context.Background())
	defer cancelCallbackSignal()
	actions := extensions.Actions{
		SendUserMessage: func(_ context.Context, content ai.UserContent, _ *extensions.SendUserMessageOptions) error {
			if content.Text == nil {
				messages <- "non-text-user-message"
				return nil
			}
			messages <- *content.Text
			return nil
		},
		SetSessionName: func(_ context.Context, name string) error {
			sessionMu.Lock()
			sessionName = name
			sessionMu.Unlock()
			setNames <- name
			return nil
		},
		GetSessionName: func(context.Context) (*string, error) {
			sessionMu.RLock()
			value := sessionName
			sessionMu.RUnlock()
			return &value, nil
		},
		GetActiveTools: func() ([]string, error) { return []string{"runtime-read"}, nil },
		GetAllTools: func() ([]extensions.ToolInfo, error) {
			return []extensions.ToolInfo{{Name: "runtime-read", Description: "read"}}, nil
		},
		GetCommands: func() ([]extensions.SlashCommandInfo, error) {
			return []extensions.SlashCommandInfo{{Name: "runtime-command", Source: extensions.SlashCommandExtension}}, nil
		},
		GetThinkingLevel: func() (agent.ThinkingLevel, error) { return agent.ThinkingLow, nil },
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		CWD: cwd, Actions: actions,
		ContextActions: extensions.ContextActions{
			GetSignal: func() context.Context { return callbackSignalContext },
			Abort:     func() { aborted <- struct{}{} },
		},
		ErrorHandler: func(value extensions.ExtensionError) { errorsSeen <- value },
	})

	sessionMu.Lock()
	sessionName = "delta-name"
	sessionMu.Unlock()
	runner.Emit(context.Background(), extensions.AgentStartEvent{})
	if got := waitStateMessage(t, messages, "delta:"); got != "delta:delta-name:runtime-read:runtime-command:low" {
		t.Fatalf("state delta message = %q", got)
	}

	command := runner.Command("state-probe")
	if command == nil {
		t.Fatal("state-probe command was not registered")
	}
	if err := command.Handler(context.Background(), "local-name", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := waitStateMessage(t, messages, "probe:"); got != "probe:delta-name:local-name:exec-ok:0" {
		t.Fatalf("state action message = %q", got)
	}
	select {
	case got := <-setNames:
		if got != "local-name" {
			t.Fatalf("set session name = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("setSessionName did not reach the Go runtime")
	}

	bus := runner.Command("state-bus")
	if bus == nil {
		t.Fatal("state-bus command was not registered")
	}
	if err := bus.Handler(context.Background(), "roundtrip", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := waitStateMessage(t, messages, "bus:"); got != "bus:roundtrip" {
		t.Fatalf("event bus message = %q", got)
	}
	abortCommand := runner.Command("state-abort")
	if abortCommand == nil {
		t.Fatal("state-abort command was not registered")
	}
	if err := abortCommand.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-aborted:
	case <-time.After(5 * time.Second):
		t.Fatal("ctx.abort did not reach the live Go context")
	}
	if got := waitStateMessage(t, messages, "abort:"); got != "abort:queued" {
		t.Fatalf("abort message = %q", got)
	}
	execAbort := runner.Command("state-exec-abort")
	if execAbort == nil {
		t.Fatal("state-exec-abort command was not registered")
	}
	if err := execAbort.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := waitStateMessage(t, messages, "exec-abort:"); got != "exec-abort:true:0" {
		t.Fatalf("aborted exec message = %q", got)
	}
	piVersion := runner.Command("state-pi-version")
	if piVersion == nil {
		t.Fatal("state-pi-version command was not registered")
	}
	if err := piVersion.Handler(context.Background(), "", runner.CreateCommandContext()); err != nil {
		t.Fatal(err)
	}
	if got := waitStateMessage(t, messages, "pi-version:"); got != "pi-version:pigo fixture-version" {
		t.Fatalf("pi shim version message = %q", got)
	}

	input := map[string]any{"original": true}
	veto := runner.EmitToolCall(context.Background(), extensions.ToolCallEvent{
		ToolCallID: "call-state", ToolName: "blocked", Input: input,
	})
	if veto == nil || !veto.Block || veto.Reason != "blocked by host fixture" {
		t.Fatalf("tool call veto = %#v", veto)
	}
	if input["hostMutated"] != true || input["original"] != true {
		t.Fatalf("mutated tool input = %#v", input)
	}
	requestPayload, ok := runner.EmitBeforeProviderRequest(context.Background(), map[string]any{"original": true}).(map[string]any)
	if !ok || requestPayload["original"] != true || requestPayload["hostMutated"] != true {
		t.Fatalf("provider request replacement = %#v", requestPayload)
	}
	if replacedWithNull := runner.EmitBeforeProviderRequest(context.Background(), map[string]any{"replaceWithNull": true}); replacedWithNull != nil {
		t.Fatalf("provider request explicit null replacement = %#v", replacedWithNull)
	}

	headers := ai.ProviderHeaders{"existing": stringPointer("kept")}
	mutatedHeaders := runner.EmitBeforeProviderHeaders(context.Background(), headers)
	if mutatedHeaders["existing"] == nil || *mutatedHeaders["existing"] != "kept" || mutatedHeaders["x-state-host"] == nil || *mutatedHeaders["x-state-host"] != "mutated" {
		t.Fatalf("mutated provider headers = %#v", mutatedHeaders)
	}

	contextMessages := runner.EmitContext(context.Background(), agent.AgentMessages{
		&ai.UserMessage{Content: ai.NewUserText("original"), Timestamp: 1},
	})
	if len(contextMessages) != 1 || userText(contextMessages[0]) != "context-host" {
		t.Fatalf("context result = %#v", contextMessages)
	}
	replaced := runner.EmitMessageEnd(context.Background(), extensions.MessageEndEvent{
		Message: &ai.UserMessage{Content: ai.NewUserText("original"), Timestamp: 1},
	})
	if userText(replaced) != "message-host" {
		t.Fatalf("message_end result = %#v", replaced)
	}
	toolResult := runner.EmitToolResult(context.Background(), extensions.ToolResultEvent{
		ToolCallID: "result-state", ToolName: "read", Content: ai.ToolResultContent{}, IsError: true,
	})
	if toolResult == nil || toolResult.Content == nil || len(*toolResult.Content) != 1 || toolResult.IsError == nil || *toolResult.IsError {
		t.Fatalf("tool_result result = %#v", toolResult)
	}
	if text, ok := (*toolResult.Content)[0].(*ai.TextContent); !ok || text.Text != "tool-result-host" {
		t.Fatalf("tool_result content = %#v", *toolResult.Content)
	}

	bashResult := runner.EmitUserBash(context.Background(), extensions.UserBashEvent{Command: "delegate", CWD: cwd})
	if bashResult == nil || bashResult.Operations == nil {
		t.Fatalf("user_bash result = %#v", bashResult)
	}
	var streamed strings.Builder
	execResult, err := bashResult.Operations.Exec(context.Background(), "printf delegated", cwd, tools.BashExecOptions{
		OnData: func(data []byte) { streamed.Write(data) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if execResult.ExitCode == nil || *execResult.ExitCode != 7 || streamed.String() != "printf delegated@"+cwd {
		t.Fatalf("delegated bash = %#v, stream %q", execResult, streamed.String())
	}

	runner.Emit(context.Background(), extensions.SessionBeforeTreeEvent{Signal: context.Background()})
	if got := waitStateMessage(t, messages, "event-signal:"); got != "event-signal:session_before_tree:true:false" {
		t.Fatalf("event signal message = %q", got)
	}

	signalCommand := runner.Command("state-signal")
	if signalCommand == nil {
		t.Fatal("state-signal command was not registered")
	}
	signalCommandDone := make(chan error, 1)
	go func() {
		signalCommandDone <- signalCommand.Handler(context.Background(), "", runner.CreateCommandContext())
	}()
	if got := waitStateMessage(t, messages, "signal:ready"); got != "signal:ready" {
		t.Fatalf("signal ready message = %q", got)
	}
	cancelCallbackSignal()
	if got := waitStateMessage(t, messages, "signal:aborted:"); got != "signal:aborted:true:context canceled" {
		t.Fatalf("signal abort message = %q", got)
	}
	select {
	case err := <-signalCommandDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("state-signal command did not finish after cancellation")
	}

	select {
	case value := <-errorsSeen:
		t.Fatalf("extension error = %#v", value)
	default:
	}
}

func startStateFixtureManager(t *testing.T, pigoExecutable string) (*Manager, *extensions.Registry, LoadResult, string) {
	t.Helper()
	runtime := requireRuntime(t)
	cwd := t.TempDir()
	manager := NewManager(Options{
		AgentDir: t.TempDir(), CWD: cwd, Version: "test", Runtime: &runtime, PigoExecutable: pigoExecutable,
		RequestTimeout: 30 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})
	registry := extensions.NewRegistry(cwd)
	result := manager.RegisterInto(context.Background(), registry, []string{fixturePath(t, "state.mjs")})
	return manager, registry, result, cwd
}

func userText(message agent.AgentMessage) string {
	user, ok := message.(*ai.UserMessage)
	if !ok || user.Content.Text == nil {
		return ""
	}
	return *user.Content.Text
}

func stringPointer(value string) *string { return &value }

func waitStateMessage(t *testing.T, messages <-chan string, prefix string) string {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case message := <-messages:
			if strings.HasPrefix(message, prefix) {
				return message
			}
		case <-timer.C:
			t.Fatalf("timed out waiting for message prefix %q", prefix)
		}
	}
}
