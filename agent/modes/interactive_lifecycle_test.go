package modes

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/internal/jsonschema"
	"github.com/OrdalieTech/orb/internal/termcaps"
	"github.com/OrdalieTech/orb/plugins/permissions"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

func TestRunInteractiveModeAttachesUIBeforeSessionStartAndRendersUnderMutation(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	uiReady := make(chan extensions.UI, 1)
	promptStarted := make(chan struct{}, 1)
	if err := registry.Register("<lifecycle-test>", func(api extensions.API) error {
		api.On(extensions.EventUIPromptStart, func(context.Context, extensions.Event, extensions.Context) (any, error) {
			promptStarted <- struct{}{}
			return nil, nil
		})
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			ui := ctx.UI()
			ui.SetHeader(func(extensions.UIHost, extensions.Theme) extensions.Component { return lifecycleText("startup header") })
			ui.SetWidget("startup", &extensions.Widget{Lines: []string{"startup widget"}}, nil)
			status := "startup status"
			ui.SetStatus("startup", &status)
			uiReady <- ui
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeTUI, DeferExtensionStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := newLifecycleTerminal(72, 18)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- RunInteractiveMode(ctx, runtime, InteractiveModeOptions{Terminal: terminal}) }()

	var extensionUI extensions.UI
	select {
	case extensionUI = <-uiReady:
	case <-time.After(2 * time.Second):
		t.Fatal("session_start did not run")
	}
	if !terminal.waitFor("startup header", 2*time.Second) || !terminal.waitFor("startup widget", 2*time.Second) || !terminal.waitFor("startup status", 2*time.Second) {
		t.Fatalf("startup UI did not survive initialization: %q", terminal.output())
	}

	inputResult := make(chan string, 1)
	go func() {
		value, err := runtime.RequestInput(ctx, "Runtime question", []string{"Keep", "Change"})
		if err != nil {
			value = err.Error()
		}
		inputResult <- value
	}()
	if !terminal.waitFor("Runtime question", 2*time.Second) {
		t.Fatal("runtime input did not reach the visible UI")
	}
	select {
	case <-promptStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime input did not emit ui_prompt_start")
	}
	terminal.mu.Lock()
	send := terminal.onInput
	terminal.mu.Unlock()
	send("\r")
	select {
	case answer := <-inputResult:
		if answer != "Keep" {
			t.Fatalf("runtime reply: %q", answer)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime input never completed")
	}

	mutationsDone := make(chan struct{})
	go func() {
		defer close(mutationsDone)
		for index := 0; index < 100; index++ {
			status := "tick"
			extensionUI.SetStatus("race", &status)
			extensionUI.SetWidget("race", &extensions.Widget{Lines: []string{"race widget"}}, nil)
			terminal.resize(70+index%5, 18+index%3)
			extensionUI.SetWidget("race", nil, nil)
		}
	}()
	<-mutationsDone
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive mode did not stop")
	}
}

// Quitting while a session opens (a plugin's session_start still running, as
// on a very large conversation) ends Orb there: the plugins left to start do
// not run against the closed session, nothing reports their stale ctx, and the
// terminal is asked nothing more, so no reply lands in the shell.
func TestQuittingWhileASessionOpensEndsThere(t *testing.T) {
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	stale := func(ctx extensions.Context) (stale bool) {
		defer func() { stale = recover() != nil }()
		ctx.CWD()
		return false
	}
	registry := extensions.NewRegistry(cwd)
	starting := make(chan struct{})
	var laterStarted atomic.Bool
	for _, handler := range []extensions.Handler{
		func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			close(starting)
			for !stale(ctx) {
				time.Sleep(time.Millisecond)
			}
			return ctx.CWD(), nil
		},
		func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			laterStarted.Store(true)
			return ctx.CWD(), nil
		},
	} {
		if err := registry.Register(fmt.Sprintf("<start-%p>", handler), func(api extensions.API) error {
			api.On(extensions.EventSessionStart, handler)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var reported []string
	var reportedMu sync.Mutex
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeTUI, DeferExtensionStart: true,
		ExtensionErrorHandler: func(failure extensions.ExtensionError) {
			reportedMu.Lock()
			reported = append(reported, failure.ExtensionPath+": "+failure.Error)
			reportedMu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := newLifecycleTerminal(72, 18)
	done := make(chan int, 1)
	go func() {
		done <- RunInteractiveMode(context.Background(), runtime, InteractiveModeOptions{Terminal: terminal})
	}()
	select {
	case <-starting:
	case <-time.After(2 * time.Second):
		t.Fatal("session_start did not run")
	}
	terminal.mu.Lock()
	send := terminal.onInput
	terminal.mu.Unlock()
	send("\x03")
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Orb kept opening the session after the quit")
	}
	reportedMu.Lock()
	defer reportedMu.Unlock()
	if laterStarted.Load() || len(reported) > 0 {
		t.Fatalf("the closed session went on starting: later plugin ran %t, reported %q", laterStarted.Load(), reported)
	}
	if output := terminal.output(); strings.Contains(output, "\x1b[?996n") || strings.Contains(output, "\x1b]11;?") {
		t.Fatalf("the terminal was asked for its colours after the quit: %q", output)
	}
}

func TestStartupVersionCheckNotifyIsRaceSafeAndStopsWithMode(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}

	terminal := newLifecycleTerminal(72, 18)
	ctx, cancel := context.WithCancel(context.Background())
	started, stopped := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	go func() {
		done <- RunInteractiveMode(ctx, runtime, InteractiveModeOptions{
			Terminal: terminal,
			StartupVersionCheck: func(ctx context.Context, ui extensions.UI) {
				close(started)
				defer close(stopped)
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for index := 0; ; index++ {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						ui.Notify("version available", extensions.NotifyInfo)
						terminal.resize(70+index%5, 18+index%3)
					}
				}
			},
		})
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("startup version check did not start")
	}
	if !terminal.waitFor("version available", 2*time.Second) {
		t.Fatalf("startup notification was not rendered: %q", terminal.output())
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive mode did not stop")
	}
	select {
	case <-stopped:
	default:
		t.Fatal("startup version check outlived interactive mode")
	}
}

func TestStartupModelRefreshBeginsAfterTUIStart(t *testing.T) {
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)

	terminal := newLifecycleTerminal(72, 18)
	startedAfterTUI := make(chan bool, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- RunInteractiveMode(ctx, runtime, InteractiveModeOptions{
			Terminal: terminal,
			StartupModelRefresh: func(context.Context) error {
				terminal.mu.Lock()
				started := terminal.onInput != nil
				terminal.mu.Unlock()
				startedAfterTUI <- started
				return nil
			},
		})
	}()
	select {
	case started := <-startedAfterTUI:
		if !started {
			t.Fatal("model refresh began before terminal start")
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("startup model refresh did not run")
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("interactive mode did not stop")
	}
}

func TestConcurrentInfoNotificationsReplaceOneStatusLine(t *testing.T) {
	initTestTheme(t)
	mode := &InteractiveMode{
		ui:   tui.NewTUI(newFakeTerminal(72, 18)),
		chat: &tui.Container{},
	}
	uiContext := NewInteractiveUI(mode)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := range 64 {
				uiContext.Notify(fmt.Sprintf("status-%d-%d", worker, iteration), extensions.NotifyInfo)
			}
		}()
	}
	close(start)
	workers.Wait()

	t.Cleanup(func() { mode.showStatusMessage("") })
	lines := (compactStatus{Component: &IdleStatus{}, Notice: mode.statusNoticeText}).Render(72)
	if len(mode.chat.Children()) != 0 || len(lines) != 1 || !strings.Contains(lines[0], "status-") {
		t.Fatalf("concurrent adjacent statuses rendered %d lines: %#v", len(lines), lines)
	}
}

func TestCancelledExtensionDialogCannotClearReboundEditor(t *testing.T) {
	initTestTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(72, 18))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings,
		header: &tui.Container{}, chat: &tui.Container{}, status: &tui.Container{},
		widgetAbove: &tui.Container{}, editorContainer: &tui.Container{}, widgetBelow: &tui.Container{},
		footer: &tui.Container{}, footerStatuses: make(map[string]string),
	}
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	extensionUI := NewInteractiveUI(mode)
	mode.interactiveUI = extensionUI

	result := make(chan struct{})
	go func() {
		_, _, _ = extensionUI.Select(context.Background(), "Pick", []string{"one"}, nil)
		close(result)
	}()
	var dialog *openDialog
	deadline := time.Now().Add(time.Second)
	for dialog == nil && time.Now().Before(deadline) {
		extensionUI.mu.Lock()
		if len(extensionUI.dialogs) > 0 {
			dialog = extensionUI.dialogs[0]
		}
		extensionUI.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if dialog == nil {
		t.Fatal("extension selector did not open")
	}

	rebound := lifecycleText("rebound editor")
	mode.editorContainer.Clear()
	mode.editorContainer.AddChild(rebound)
	dialog.cancel()
	select {
	case <-result:
	case <-time.After(time.Second):
		t.Fatal("cancelled selector did not finish")
	}
	children := mode.editorContainer.Children()
	if len(children) != 1 || children[0] != rebound {
		t.Fatalf("late dialog cleanup replaced rebound editor: %#v", children)
	}
}

type recordingTerminal struct {
	*modesextShortcutTerminal
	mu      sync.Mutex
	written strings.Builder
}

func (terminal *recordingTerminal) Write(data string) {
	terminal.mu.Lock()
	terminal.written.WriteString(data)
	terminal.mu.Unlock()
}

func (terminal *recordingTerminal) take() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	written := terminal.written.String()
	terminal.written.Reset()
	return written
}

// An approval opens as a stretched-caps card that calls a user who left the
// terminal, and its answer stays in the transcript; a turn that ends while
// they are away calls them too, and nothing does while the terminal has focus.
func TestInterruptCardCallsAnAwayUserAndLeavesItsVerdict(t *testing.T) {
	initTestTheme(t)
	tui.SetCapabilities(tui.TerminalCapabilities{TrueColor: true, Notify: termcaps.NotifyOSC777})
	t.Cleanup(tui.ResetCapabilitiesCache)
	terminal := &recordingTerminal{modesextShortcutTerminal: &modesextShortcutTerminal{fakeTerminalImpl: newFakeTerminal(110, 48)}}
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		ui: tui.NewTUI(terminal), keybindings: bindings,
		header: &tui.Container{}, chat: &tui.Container{}, status: &tui.Container{},
		widgetAbove: &tui.Container{}, editorContainer: &tui.Container{}, widgetBelow: &tui.Container{},
		footer: &tui.Container{}, footerStatuses: make(map[string]string),
	}
	if err := mode.ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mode.ui.Stop() })
	extensionUI := NewInteractiveUI(mode)
	terminal.send("\x1b[O")

	approval := extensions.WithInputOptions(context.Background(), extensions.InputOptions{Presentation: &extensions.InputPresentation{Kind: permissions.Kind, Data: []byte(`{"tool":"bash"}`)}})
	answer := make(chan string, 1)
	go func() {
		value, _, _ := extensionUI.Select(approval, "Permission requested for bash", []string{"y approve once", "n deny"}, nil)
		answer <- value
	}()
	var screen string
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline) && !strings.Contains(tui.StripANSI(screen), "█▁█ █▄▀ █▄▀ █▄▀ █ █ █ █ █▁█ █"); time.Sleep(10 * time.Millisecond) {
		screen += terminal.take()
	}
	if !strings.Contains(tui.StripANSI(screen), "█▁█ █▄▀ █▄▀ █▄▀ █ █ █ █ █▁█ █") {
		t.Fatalf("approval did not raise its card:\n%s", tui.StripANSI(screen))
	}
	if !strings.Contains(screen, "\x1b]777;notify;Orb;Permission requested for bash\x1b\\") {
		t.Fatalf("approval did not call the away user: %q", screen)
	}
	terminal.send("y")
	if value := <-answer; value != "y approve once" {
		t.Fatalf("answer = %q", value)
	}
	if chat := tui.StripANSI(strings.Join(mode.chat.Render(80), "\n")); !strings.Contains(chat, " APPROVAL   approve once") {
		t.Fatalf("transcript lacks the verdict:\n%s", chat)
	}

	settle := func() string {
		terminal.take()
		mode.turnStart = time.Now()
		mode.handleEvent(agent.AgentSettledEvent{})
		return terminal.take()
	}
	if written := settle(); !strings.Contains(written, "\x1b]777;notify;Orb;done\x1b\\") {
		t.Fatalf("finished turn did not call the away user: %q", written)
	}
	terminal.send("\x1b[I")
	if written := settle(); strings.Contains(written, "777;notify") {
		t.Fatalf("finished turn notified a user at the terminal: %q", written)
	}
}

// A second dialog shows above the first instead of cancelling it; closing it shows the first again (pi 1.4.0).
func TestExtensionDialogsStack(t *testing.T) {
	initTestTheme(t)
	modeUI := tui.NewTUI(newFakeTerminal(72, 18))
	bindings := NewAppKeybindings(nil)
	tui.SetKeybindings(bindings)
	mode := &InteractiveMode{
		ui: modeUI, keybindings: bindings,
		header: &tui.Container{}, chat: &tui.Container{}, status: &tui.Container{},
		widgetAbove: &tui.Container{}, editorContainer: &tui.Container{}, widgetBelow: &tui.Container{},
		footer: &tui.Container{}, footerStatuses: make(map[string]string),
	}
	extensionUI := NewInteractiveUI(mode)
	first, second := make(chan bool, 1), make(chan bool, 1)
	open := func(result chan bool) {
		go func() {
			_, ok, _ := extensionUI.Select(context.Background(), "Pick", []string{"one"}, nil)
			result <- ok
		}()
	}
	waitFor := func(count int) []*openDialog {
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			extensionUI.mu.Lock()
			dialogs := slices.Clone(extensionUI.dialogs)
			extensionUI.mu.Unlock()
			if len(dialogs) == count {
				return dialogs
			}
		}
		t.Fatalf("%d dialogs did not open", count)
		return nil
	}
	open(first)
	waitFor(1)
	open(second)
	dialogs := waitFor(2)
	dialogs[1].cancel()
	<-second
	waitFor(1)
	select {
	case <-first:
		t.Fatal("the first dialog closed when the second opened or closed")
	case <-time.After(20 * time.Millisecond):
	}
	dialogs[0].cancel()
	<-first

	// Inline custom components stack the same way in the editor's place.
	mode.editor = NewCustomEditor(modeUI, theme.EditorTheme(), bindings)
	mode.editorContainer.AddChild(mode.editor)
	closers := make(chan extensions.CustomDone, 2)
	custom := func(text string) {
		go func() {
			_, _, _ = extensionUI.Custom(context.Background(), func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
				closers <- done
				return lifecycleText(text), nil
			}, nil)
		}()
	}
	shown := func(want any) {
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if children := mode.editorContainer.Children(); len(children) == 1 && children[0] == want {
				return
			}
		}
		t.Fatalf("editor container = %#v, want %#v", mode.editorContainer.Children(), want)
	}
	custom("a")
	closeA := <-closers
	shown(lifecycleText("a"))
	custom("b")
	closeB := <-closers
	shown(lifecycleText("b"))
	closeB(nil)
	shown(lifecycleText("a"))
	closeA(nil)
	shown(any(mode.editor))
}

// A message sent at any point of a turn is answered, after the turn or within it, or comes back to
// the editor when the turn cannot run; it never waits in the queue for a run that is not coming.
// Each case holds the first turn at one point, sends the second message there, then lets it go.
func TestAMessageSentAtAnyPointIsAnswered(t *testing.T) {
	for _, scenario := range []queueScenario{
		{name: "idle"},
		{name: "idle follow-up", followUp: true},
		{name: "input handler", hold: "input"},
		{name: "before_agent_start", hold: "before_agent_start"},
		{name: "streaming", hold: "stream"},
		{name: "streaming follow-up", hold: "stream", followUp: true},
		{name: "tool call", hold: "tool"},
		{name: "agent_end", hold: "agent_end"},
		{name: "agent_before_settle", hold: "agent_before_settle"},
		{name: "agent_settled", hold: "agent_settled"},
		{name: "agent_settled, two messages", hold: "agent_settled", third: true},
		{name: "command without a turn", hold: "command"},
		{name: "first message lacks auth", hold: "auth", restored: true},
		{name: "abort", hold: "stream", abort: true, restored: true},
		{name: "session loop streaming", loop: true, hold: "stream"},
		{name: "session loop agent_end", loop: true, hold: "agent_end"},
		{name: "session loop agent_settled", loop: true, hold: "agent_settled"},
		{name: "session loop failed turn", loop: true, hold: "fail"},
		{name: "extension's turn streaming", extension: true, hold: "stream"},
		{name: "extension's turn settling", extension: true, hold: "agent_settled", deferred: true},
	} {
		t.Run(scenario.name, func(t *testing.T) { scenario.run(t) })
	}
}

type queueScenario struct {
	name string
	// hold is where the first turn waits while the second message is sent: an extension event, the
	// model's stream ("stream"), a tool, a slash command, the API key lookup, or a failing turn.
	hold      string
	loop      bool // turns run in a session loop shaped like Claude's
	extension bool // an extension's message starts the first turn, not the user
	followUp  bool // the second message is sent with Alt+Enter
	third     bool // a third message follows the second while the turn waits
	abort     bool // Esc is pressed once the second message is queued
	restored  bool // the second message comes back to the editor instead
	deferred  bool // the second message waits for the settling turn, not in the queue
}

func (scenario queueScenario) run(t *testing.T) {
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	var holding atomic.Bool
	held, release := make(chan struct{}), make(chan struct{})
	hold := func(ctx context.Context, at string) bool {
		if at != scenario.hold || !holding.CompareAndSwap(false, true) {
			return false
		}
		close(held)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return true
	}
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<queue-test>", func(api extensions.API) error {
		for _, event := range []extensions.EventType{extensions.EventInput, extensions.EventBeforeAgentStart, extensions.EventAgentEnd, extensions.EventAgentBeforeSettle, extensions.EventAgentSettled} {
			api.On(event, func(ctx context.Context, _ extensions.Event, _ extensions.Context) (any, error) {
				hold(ctx, string(event))
				return nil, nil
			})
		}
		api.RegisterCommand("wait", extensions.Command{Handler: func(ctx context.Context, _ string, _ extensions.CommandContext) error {
			hold(ctx, "command")
			return nil
		}})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Each turn answers the user messages since the last answer, so the transcript shows which it took.
	answer := func(messages []ai.Message) string {
		var texts []string
		for _, message := range messages {
			switch typed := message.(type) {
			case *ai.AssistantMessage:
				texts = nil
			case *ai.UserMessage:
				if typed.Content.Text != nil {
					texts = append(texts, *typed.Content.Text)
				} else {
					texts = append(texts, ai.ContentText(typed.Content.Blocks, ""))
				}
			}
		}
		return "ANSWERED[" + strings.Join(texts, "+") + "]"
	}
	provider := faux.New()
	var toolCalled atomic.Bool
	respond := faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
		if scenario.hold == "tool" && !toolCalled.Swap(true) {
			return faux.AssistantMessage(faux.ToolCall("wait", map[string]any{}, faux.ToolCallOptions{ID: "wait"}), faux.AssistantMessageOptions{StopReason: ai.StopReasonToolUse}), nil
		}
		return faux.AssistantMessage(answer(request.Messages)), nil
	})
	provider.SetResponses(slices.Repeat([]faux.ResponseStep{respond}, 10))
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		hold(ctx, "stream")
		return provider.StreamSimple(ctx, model, request, options)
	}
	model := provider.GetModel()
	options := []engine.AgentOption{engine.WithInitialState(engine.AgentState{Model: model}), engine.WithConvertToLLM(agent.ConvertToLLM)}
	if scenario.loop {
		var failed atomic.Bool
		options = append(options, engine.WithSessionLoop(func(ctx context.Context, prompts engine.AgentMessages, _ engine.AgentContext, config engine.AgentLoopConfig, emit engine.EventSink) error {
			if err := emit(ctx, engine.AgentStartEvent{}); err != nil {
				return err
			}
			var transcript []ai.Message
			for len(prompts) > 0 && ctx.Err() == nil {
				_ = emit(ctx, engine.TurnStartEvent{})
				for _, prompt := range prompts {
					_ = emit(ctx, engine.MessageStartEvent{Message: prompt})
					_ = emit(ctx, engine.MessageEndEvent{Message: prompt})
					if message, ok := prompt.(ai.Message); ok {
						transcript = append(transcript, message)
					}
				}
				hold(ctx, "stream")
				if scenario.hold == "fail" && !failed.Swap(true) {
					hold(ctx, "fail")
					return errors.New("claude SDK exited without a result")
				}
				reply := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: answer(transcript)}}, StopReason: ai.StopReasonStop, API: model.API, Provider: model.Provider, Model: model.ID}
				transcript = append(transcript, reply)
				_ = emit(ctx, engine.MessageStartEvent{Message: reply})
				_ = emit(ctx, engine.MessageEndEvent{Message: reply})
				_ = emit(ctx, engine.TurnEndEvent{Message: reply})
				prompts, _ = config.GetSteeringMessages(ctx)
				if len(prompts) == 0 {
					prompts, _ = config.GetFollowUpMessages(ctx)
				}
			}
			return emit(ctx, engine.AgentEndEvent{})
		}))
	}
	wait := engine.AgentToolFunc{
		AgentToolSpec: engine.AgentToolSpec{Name: "wait", Label: "wait", Description: "wait", Parameters: jsonschema.Schema(`{"type":"object","properties":{}}`)},
		Run: func(ctx context.Context, _ string, _ any, _ engine.AgentToolUpdateCallback) (engine.AgentToolResult, error) {
			hold(ctx, "tool")
			return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "waited"}}}, nil
		},
	}
	var getAPIKey engine.GetAPIKeyFunc
	if scenario.hold == "auth" {
		getAPIKey = func(ctx context.Context, _ ai.ProviderID) (*string, error) {
			hold(ctx, "auth")
			return nil, nil
		}
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(stream, options...), SessionManager: manager, Settings: settings, GetAPIKey: getAPIKey,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeTUI, DeferExtensionStart: true,
		BaseTools: []engine.AgentTool{wait}, InitialActiveToolNames: []string{"wait"},
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := newLifecycleTerminal(100, 30)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- RunInteractiveMode(ctx, runtime, InteractiveModeOptions{Terminal: terminal}) }()
	defer func() {
		cancel()
		<-done
	}()
	var send func(string)
	for deadline := time.Now().Add(2 * time.Second); send == nil && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		terminal.mu.Lock()
		send = terminal.onInput
		terminal.mu.Unlock()
	}
	if send == nil {
		t.Fatal("the terminal never started")
	}
	submit := func(text string) {
		send(text)
		if scenario.followUp && text != "first" && text != "/wait" {
			send("\x1b[13;3u") // Alt+Enter; a bare ESC CR is a newline in the composer
		} else {
			send("\r")
		}
	}
	switch {
	case scenario.extension:
		trigger := true
		go func() {
			_ = runtime.SendCustomMessage(ctx, agent.CustomMessage{CustomType: "job", Content: "job done", Display: true}, &agent.SendCustomMessageOptions{TriggerTurn: &trigger})
		}()
	case scenario.hold == "command":
		submit("/wait")
	default:
		submit("first")
	}
	if scenario.hold == "" {
		if !terminal.waitFor("ANSWERED[first]", 2*time.Second) {
			t.Fatalf("the first message was not answered: screen ends %q", screenTail(terminal))
		}
	} else {
		select {
		case <-held:
		case <-time.After(2 * time.Second):
			t.Fatalf("the first turn never reached %s", scenario.hold)
		}
	}
	submit("second")
	if scenario.third {
		submit("third")
	}
	if scenario.hold != "" {
		queued := scenario.third && runtime.PendingMessageCount() == 2 || !scenario.third && runtime.PendingMessageCount() == 1
		for deadline := time.Now().Add(2 * time.Second); !scenario.deferred && !queued; time.Sleep(time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatalf("the second message was not queued: %d queued", runtime.PendingMessageCount())
			}
			queued = scenario.third && runtime.PendingMessageCount() == 2 || !scenario.third && runtime.PendingMessageCount() == 1
		}
		if scenario.deferred {
			time.Sleep(50 * time.Millisecond) // the submission reaches the settling turn, which runs it next
		}
		if scenario.abort {
			send("\x1b")
		}
		close(release)
	}
	if scenario.restored {
		if !terminal.waitFor("to editor", 2*time.Second) || runtime.PendingMessageCount() != 0 {
			t.Fatalf("the second message did not come back to the editor: %d queued, screen ends %q", runtime.PendingMessageCount(), screenTail(terminal))
		}
		return
	}
	// A message is answered alone or with others in one turn (first+second, second+third).
	answered := func(text string) bool {
		return regexp.MustCompile(`ANSWERED\[[^\]]*\b` + text + `\b`).MatchString(tui.StripANSI(terminal.output()))
	}
	for deadline := time.Now().Add(2 * time.Second); !answered("second") || scenario.third && !answered("third"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the messages sent were never answered: %d queued, screen ends %q", runtime.PendingMessageCount(), screenTail(terminal))
		}
	}
	for deadline := time.Now().Add(2 * time.Second); runtime.PendingMessageCount() != 0 || !runtime.IsIdle(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("the session did not settle with an empty queue: %d queued", runtime.PendingMessageCount())
		}
	}
}

func screenTail(terminal *lifecycleTerminal) string {
	screen := tui.StripANSI(terminal.output())
	return screen[max(0, len(screen)-1500):]
}

type lifecycleText string

func (value lifecycleText) Render(int) []string { return []string{string(value)} }

type lifecycleTerminal struct {
	mu            sync.Mutex
	columns, rows int
	onInput       func(string)
	onResize      func()
	writes        strings.Builder
}

func newLifecycleTerminal(columns, rows int) *lifecycleTerminal {
	return &lifecycleTerminal{columns: columns, rows: rows}
}
func (terminal *lifecycleTerminal) Start(onInput func(string), onResize func()) error {
	terminal.mu.Lock()
	terminal.onInput, terminal.onResize = onInput, onResize
	terminal.mu.Unlock()
	return nil
}
func (terminal *lifecycleTerminal) Stop() error                   { return nil }
func (terminal *lifecycleTerminal) DrainInput(_, _ time.Duration) {}
func (terminal *lifecycleTerminal) Write(value string) {
	terminal.mu.Lock()
	terminal.writes.WriteString(value)
	terminal.mu.Unlock()
}
func (terminal *lifecycleTerminal) Columns() int {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.columns
}
func (terminal *lifecycleTerminal) Rows() int {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.rows
}
func (*lifecycleTerminal) KittyProtocolActive() bool { return false }
func (*lifecycleTerminal) MoveBy(int)                {}
func (*lifecycleTerminal) HideCursor()               {}
func (*lifecycleTerminal) ShowCursor()               {}
func (*lifecycleTerminal) ClearLine()                {}
func (*lifecycleTerminal) ClearFromCursor()          {}
func (*lifecycleTerminal) ClearScreen()              {}
func (*lifecycleTerminal) SetTitle(string)           {}
func (*lifecycleTerminal) SetProgress(bool)          {}
func (terminal *lifecycleTerminal) resize(columns, rows int) {
	terminal.mu.Lock()
	terminal.columns, terminal.rows = columns, rows
	callback := terminal.onResize
	terminal.mu.Unlock()
	if callback != nil {
		callback()
	}
}
func (terminal *lifecycleTerminal) output() string {
	terminal.mu.Lock()
	defer terminal.mu.Unlock()
	return terminal.writes.String()
}
func (terminal *lifecycleTerminal) waitFor(value string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if strings.Contains(terminal.output(), value) {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}
