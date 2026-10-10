package modes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine"
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
	var dialog *ExtensionSelectorComponent
	deadline := time.Now().Add(time.Second)
	for dialog == nil && time.Now().Before(deadline) {
		extensionUI.mu.Lock()
		dialog = extensionUI.activeSelector
		extensionUI.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if dialog == nil {
		t.Fatal("extension selector did not open")
	}

	extensionUI.mu.Lock()
	extensionUI.activeSelector = nil
	extensionUI.mu.Unlock()
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
