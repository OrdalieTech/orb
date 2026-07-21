package modes

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"

	theme "github.com/OrdalieTech/pigo/codingagent/modes/theme"
)

// Sweep regression (modes-ext): pi.registerShortcut registrations must be
// dispatched by the TUI. Upstream setupExtensionShortcuts installs
// defaultEditor.onExtensionShortcut, which matches raw input against the
// resolved shortcut map ahead of built-in keybindings and runs the handler
// asynchronously (interactive-mode.ts:1756-1809, components/custom-editor.ts:32).
// Reserved built-in bindings skip conflicting extension shortcuts entirely
// (runner.ts RESERVED_KEYBINDINGS_FOR_EXTENSION_CONFLICTS).

type modesextShortcutTerminal struct {
	*fakeTerminalImpl
	mu      sync.Mutex
	onInput func(string)
}

func (terminal *modesextShortcutTerminal) Start(input func(string), _ func()) error {
	terminal.mu.Lock()
	terminal.onInput = input
	terminal.mu.Unlock()
	return nil
}

func (terminal *modesextShortcutTerminal) send(data string) {
	terminal.mu.Lock()
	input := terminal.onInput
	terminal.mu.Unlock()
	if input != nil {
		input(data)
	}
}

func newShortcutTestMode(t *testing.T, register func(extensions.API)) (*InteractiveMode, *modesextShortcutTerminal) {
	t.Helper()
	initTestTheme(t)
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<inline:shortcut-probe>", func(api extensions.API) error {
		register(api)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)

	terminal := &modesextShortcutTerminal{fakeTerminalImpl: newFakeTerminal(100, 30)}
	mode := &InteractiveMode{
		session:        runtime,
		ui:             tui.NewTUI(terminal),
		chat:           &tui.Container{},
		toolComponents: make(map[string]*ToolExecutionComponent),
		cwd:            cwd,
		keybindings:    NewAppKeybindings(nil),
	}
	mode.editor = NewCustomEditor(mode.ui, theme.EditorTheme(), mode.keybindings)
	if err := mode.ui.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mode.ui.Stop() })
	mode.ui.SetFocus(mode.editor)
	return mode, terminal
}

func waitForShortcut(t *testing.T, fired <-chan string, want string) {
	t.Helper()
	select {
	case got := <-fired:
		if got != want {
			t.Fatalf("shortcut fired = %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("shortcut %q handler never fired", want)
	}
}

func TestRegisteredShortcutDispatchesFromTUIKeypress(t *testing.T) {
	fired := make(chan string, 4)
	mode, terminal := newShortcutTestMode(t, func(api extensions.API) {
		api.RegisterShortcut("alt+x", extensions.Shortcut{
			Description: "probe",
			Handler: func(_ context.Context, ctx extensions.Context) error {
				if ctx == nil {
					fired <- "nil-context"
					return nil
				}
				fired <- "alt+x"
				return nil
			},
		})
	})
	mode.setupExtensionShortcuts()

	if mode.editor.OnExtensionShortcut == nil {
		t.Fatal("setupExtensionShortcuts installed no dispatcher on the editor")
	}
	terminal.send("\x1bx")
	waitForShortcut(t, fired, "alt+x")
}

func TestRegisteredShortcutWinsOverNonReservedBuiltinBinding(t *testing.T) {
	// ctrl+y is tui.editor.yank: a built-in binding outside the reserved set,
	// so the extension shortcut overrides it with a diagnostic (upstream
	// getShortcuts "Using <extension>" branch) and dispatch happens before the
	// editor's own key handling (custom-editor.ts:32).
	fired := make(chan string, 4)
	mode, terminal := newShortcutTestMode(t, func(api extensions.API) {
		api.RegisterShortcut("ctrl+y", extensions.Shortcut{
			Handler: func(context.Context, extensions.Context) error {
				fired <- "ctrl+y"
				return nil
			},
		})
	})
	mode.setupExtensionShortcuts()

	terminal.send("\x19")
	waitForShortcut(t, fired, "ctrl+y")

	runner := mode.session.ExtensionRunner()
	diagnostics := runner.ShortcutDiagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("shortcut diagnostics = %v, want one conflict warning", diagnostics)
	}
}

func TestRegisteredShortcutConflictingWithReservedBindingIsSkipped(t *testing.T) {
	// ctrl+g is app.editor.external: reserved for built-ins, so the extension
	// shortcut is skipped with a diagnostic and never dispatched (upstream
	// getShortcuts "Skipping." branch).
	fired := make(chan string, 4)
	mode, terminal := newShortcutTestMode(t, func(api extensions.API) {
		api.RegisterShortcut("ctrl+g", extensions.Shortcut{
			Handler: func(context.Context, extensions.Context) error {
				fired <- "ctrl+g"
				return nil
			},
		})
	})
	mode.setupExtensionShortcuts()

	terminal.send("\x07")
	select {
	case <-fired:
		t.Fatal("reserved-conflicting shortcut fired, want it skipped")
	case <-time.After(200 * time.Millisecond):
	}
	runner := mode.session.ExtensionRunner()
	diagnostics := runner.ShortcutDiagnostics()
	if len(diagnostics) != 1 {
		t.Fatalf("shortcut diagnostics = %v, want one skip warning", diagnostics)
	}
}

func TestShortcutHandlerErrorSurfacesInChat(t *testing.T) {
	// Upstream wraps handler rejections as "Shortcut handler error: <message>"
	// through showError (interactive-mode.ts:1800-1802).
	mode, terminal := newShortcutTestMode(t, func(api extensions.API) {
		api.RegisterShortcut("alt+e", extensions.Shortcut{
			Handler: func(context.Context, extensions.Context) error {
				return context.Canceled
			},
		})
	})
	mode.setupExtensionShortcuts()

	terminal.send("\x1be")
	deadline := time.Now().Add(2 * time.Second)
	for {
		rendered := strings.Join(mode.chat.Render(100), "\n")
		if strings.Contains(rendered, "Error: Shortcut handler error: context canceled") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("chat render = %q, want the shortcut handler error", rendered)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
