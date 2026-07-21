package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"

	modeTheme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

type f12ShutdownFixture struct {
	Schema   int                `json:"schema"`
	Ordinary f12ShutdownCapture `json:"ordinary"`
	Signal   f12ShutdownCapture `json:"signal"`
}

type f12ShutdownCapture struct {
	Order  []string `json:"order"`
	Output string   `json:"output"`
}

func TestF12ShutdownLifecycleMatchesUpstream(t *testing.T) {
	fixture := loadF12ShutdownFixture(t)
	for _, test := range []struct {
		name       string
		fromSignal bool
		want       f12ShutdownCapture
	}{{"ordinary", false, fixture.Ordinary}, {"signal", true, fixture.Signal}} {
		t.Run(test.name, func(t *testing.T) {
			mode, host, temporary, output := newF12ShutdownMode(t)
			mode.shutdown(test.fromSignal)

			wantOrder := shutdownObservableOrder(test.want.Order)
			if got := host.Trace(); !reflect.DeepEqual(got, wantOrder) {
				t.Fatalf("shutdown order differs\nwant: %#v\n got: %#v", wantOrder, got)
			}
			gotOutput := strings.ReplaceAll(output.String(), temporary, "<tmp>")
			// D30 changes only the executable token in this upstream fixture.
			wantOutput := strings.Replace(test.want.Output, " pi --session", " pigo --session", 1)
			if gotOutput != wantOutput {
				t.Fatalf("shutdown output differs\nwant: %q\n got: %q", wantOutput, gotOutput)
			}
			mode.mu.Lock()
			requested, controller := mode.shutdownRequested, mode.themeController
			mode.mu.Unlock()
			if !requested || controller != nil {
				t.Fatalf("shutdown state = requested %t controller %p", requested, controller)
			}
		})
	}
}

func TestSIGTERMUsesSignalShutdownLifecycle(t *testing.T) {
	fixture := loadF12ShutdownFixture(t)
	cwd := t.TempDir()
	agentDir := filepath.Join(cwd, "agent")
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd, sessionstore.WithSessionID("signal-session"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeSession, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	terminal := &f12VisibleTerminal{columns: 100, rows: 40, started: make(chan struct{})}
	host := &f12VisibleHost{runtime: runtimeSession, cwd: cwd}
	terminal.trace = host.addTrace
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	go func() {
		done <- RunInteractiveMode(ctx, runtimeSession, InteractiveModeOptions{
			Terminal: terminal, Host: host, Output: &bytes.Buffer{}, OutputTTY: true,
		})
	}()
	select {
	case <-terminal.started:
	case <-time.After(2 * time.Second):
		t.Fatal("interactive terminal did not start")
	}
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("signal exit code = %d", code)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-done
		t.Fatal("SIGTERM did not stop interactive mode")
	}
	if got, want := host.Trace(), shutdownObservableOrder(fixture.Signal.Order); !reflect.DeepEqual(got, want) {
		t.Fatalf("SIGTERM shutdown order differs\nwant: %#v\n got: %#v", want, got)
	}
}

func newF12ShutdownMode(t *testing.T) (*InteractiveMode, *f12VisibleHost, string, *bytes.Buffer) {
	t.Helper()
	initTestTheme(t)
	temporary := t.TempDir()
	cwd := filepath.Join(temporary, "work")
	agentDir := filepath.Join(temporary, "agent")
	sessionDir := filepath.Join(temporary, "custom pi's sessions")
	for _, directory := range []string{cwd, agentDir, sessionDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.Create(cwd, sessionDir, sessionstore.WithSessionID("fixture-session"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manager.GetSessionFile(), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimeSession, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeSession.Dispose)
	terminal := &f12VisibleTerminal{columns: 100, rows: 40}
	uiInstance := tui.NewTUI(terminal)
	host := &f12VisibleHost{runtime: runtimeSession, cwd: cwd}
	terminal.trace = host.addTrace
	registry := modeTheme.Load(modeTheme.LoadOptions{CWD: cwd, AgentDir: agentDir, NoThemes: true})
	output := &bytes.Buffer{}
	mode := &InteractiveMode{
		session: runtimeSession, ui: uiInstance, options: InteractiveModeOptions{
			Host: host, Output: output, OutputTTY: true,
		},
		inputCh: make(chan inputEntry, 1), themeController: modeTheme.Initialize(registry, "dark", modeTheme.Dark, nil),
	}
	return mode, host, temporary, output
}

func loadF12ShutdownFixture(t testing.TB) f12ShutdownFixture {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve F12 shutdown fixture path")
	}
	encoded, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "conformance", "fixtures", "F12-shutdown", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture f12ShutdownFixture
	if err := json.Unmarshal(encoded, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Schema != 1 {
		t.Fatalf("fixture schema = %d, want 1", fixture.Schema)
	}
	return fixture
}

func shutdownObservableOrder(order []string) []string {
	result := make([]string, 0, len(order))
	for _, event := range order {
		if event != "theme-stop" && !strings.HasPrefix(event, "exit:") {
			result = append(result, event)
		}
	}
	return result
}
