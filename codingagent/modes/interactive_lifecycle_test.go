package modes

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/tui"
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
	if err := registry.Register("<lifecycle-test>", func(api extensions.API) error {
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
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeTUI, DeferSessionStart: true,
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
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
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

	lines := mode.chat.Render(72)
	if len(lines) != 2 || lines[0] != "" || !strings.Contains(lines[1], "status-") {
		t.Fatalf("concurrent adjacent statuses rendered %d lines: %#v", len(lines), lines)
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
