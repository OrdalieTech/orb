package modes

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

func TestThinkingCommandSelectionAndPersistence(t *testing.T) {
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
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: engine.NewAgent(nil, engine.WithInitialState(engine.AgentState{Model: &ai.Model{Provider: "anthropic", ID: "claude-sonnet-4", Reasoning: true}, ThinkingLevel: ai.ModelThinkingLow})), SessionManager: manager, Settings: settings})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)
	mode := &InteractiveMode{session: runtime, ui: tui.NewTUI(newFakeTerminal(80, 24)), chat: &tui.Container{}}
	action, ok := mode.resolveSlashCommand("thinking", "HiGh")
	if !ok || action.name != "handleThinkingCommand" || !slashCommandAllowsArguments("thinking") || !slashCommandClearsEditorFirst("thinking") {
		t.Fatal("thinking command not routed")
	}
	action.run()
	if runtime.State().ThinkingLevel != ai.ModelThinkingHigh || settings.GetDefaultThinkingLevel() != "" {
		t.Fatal("explicit thinking level should affect only session")
	}
	mode.handleThinkingCommand("invalid")
	if runtime.State().ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatal("invalid level changed session")
	}
	if rendered := strings.Join(mode.chat.Render(120), "\n"); !strings.Contains(rendered, `Unknown thinking level "invalid".`) {
		t.Fatalf("missing diagnostic: %s", rendered)
	}
	mode.selectThinkingLevel(ai.ModelThinkingLow, true)
	if settings.GetDefaultThinkingLevel() != ai.ModelThinkingLow {
		t.Fatal("default selection not persisted")
	}
	if err := runtime.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestThinkingSelectorSaveDefaultAndCancel(t *testing.T) {
	initTestTheme(t)
	chosen := ""
	persisted := false
	cancelled := false
	choose := func(level string, persist bool) { chosen, persisted = level, persist }
	selector := &thinkingSelector{ExtensionSelectorComponent: NewExtensionSelectorItemsComponent("Thinking", []tui.SelectItem{{Value: "low"}, {Value: "high"}}, func(level string) { choose(level, false) }, func() { cancelled = true }, nil), choose: choose}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b[B"})
	selector.HandleInput(tui.KeyEvent{Raw: "\x13"})
	if chosen != "high" || !persisted {
		t.Fatalf("default choice %q %v", chosen, persisted)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\r"})
	if chosen != "high" || persisted {
		t.Fatalf("session choice %q %v", chosen, persisted)
	}
	selector.HandleInput(tui.KeyEvent{Raw: "\x1b"})
	if !cancelled {
		t.Fatal("escape did not cancel")
	}
}

func TestThinkingCommandRouteMatchesReleasedSource(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F12-commands/commands.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ThinkingCommands []struct {
			Input string `json:"input"`
			Trace []struct {
				Selector bool   `json:"selector"`
				Level    string `json:"level"`
				Persist  bool   `json:"persist"`
				Error    string `json:"error"`
			} `json:"trace"`
		} `json:"thinkingCommands"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.ThinkingCommands) != 3 {
		t.Fatal("missing source-extracted thinking cases")
	}
	for _, probe := range fixture.ThinkingCommands {
		mode := &InteractiveMode{}
		action, ok := mode.resolveSlashCommand("thinking", probe.Input)
		if !ok || action.name != "handleThinkingCommand" || !reflect.DeepEqual(action.arguments, []string{probe.Input}) {
			t.Fatalf("route missing for %q", probe.Input)
		}
		if len(probe.Trace) != 1 {
			t.Fatalf("unexpected upstream trace: %#v", probe)
		}
		trace := probe.Trace[0]
		if probe.Input == "" && !trace.Selector {
			t.Fatal("empty argument does not select")
		}
		if probe.Input == "HiGh" && (trace.Level != "high" || trace.Persist) {
			t.Fatal("upstream explicit selection differs")
		}
		if probe.Input == "invalid" && trace.Error != `Unknown thinking level "invalid". Available levels: off, low, medium, high.` {
			t.Fatalf("upstream diagnostic: %q", trace.Error)
		}
	}
}
