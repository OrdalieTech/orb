package tui

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type panicOnRenderComponent struct {
	panicking atomic.Bool
}

func (component *panicOnRenderComponent) Render(width int) []string {
	if component.panicking.Load() {
		panic("boom in render")
	}
	return []string{"ok"}
}

func TestGuardedRenderTimerPanicRestoresTerminalBeforeExit(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	component := &panicOnRenderComponent{}
	ui.AddChild(component)

	type crashReport struct {
		message         string
		terminalStopped bool
	}
	crashes := make(chan crashReport, 1)
	previousExit := crashExit
	crashExit = func(message string) {
		terminal.mu.Lock()
		stopped := terminal.stopped
		terminal.mu.Unlock()
		crashes <- crashReport{message: message, terminalStopped: stopped}
	}
	defer func() { crashExit = previousExit }()

	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	component.panicking.Store(true)
	ui.RequestRender()

	select {
	case report := <-crashes:
		if !report.terminalStopped {
			t.Fatal("terminal was not restored before crash exit")
		}
		if !strings.Contains(report.message, "pi exiting due to uncaughtException") {
			t.Fatalf("crash message missing upstream banner: %q", report.message)
		}
		if !strings.Contains(report.message, "boom in render") || !strings.Contains(report.message, "goroutine") {
			t.Fatalf("crash message missing panic value or stack: %q", report.message)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("guarded render goroutine did not report the panic")
	}
	if !ui.isStopped() {
		t.Fatal("ui not marked stopped by crash restore")
	}
	terminal.mu.Lock()
	hidden := terminal.hidden
	terminal.mu.Unlock()
	if hidden {
		t.Fatal("cursor not restored by crash restore")
	}
	if output := terminal.output(); !strings.Contains(output, scrollOnOutputOn) {
		t.Fatalf("scroll-on-output not restored: %q", output)
	}
}

func crashRestoreCount() int {
	crashMu.Lock()
	defer crashMu.Unlock()
	return len(crashRestores)
}

func TestCrashRestoreUnregistersOnStop(t *testing.T) {
	terminal := newFakeTerminal(80, 24)
	ui := NewTUI(terminal)
	before := crashRestoreCount()
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	if count := crashRestoreCount(); count != before+1 {
		t.Fatalf("crash restore registrations after Start = %d, want %d", count, before+1)
	}
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if count := crashRestoreCount(); count != before {
		t.Fatalf("crash restore registrations after Stop = %d, want %d", count, before)
	}
}
