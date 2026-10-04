package modes

import (
	"context"
	"strings"
	"testing"
	"time"

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
	mode.StatusAction("orb:thinking")()
	if runtime.State().ThinkingLevel != ai.ModelThinkingMedium || !strings.Contains(tui.StripANSI(mode.statusNoticeText()), "Reasoning: medium") {
		t.Fatal("footer click did not advance reasoning and show its temporary label")
	}
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-45 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	replacing := mode.animatedStatusNoticeText()
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-180 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	entering := mode.animatedStatusNoticeText()
	mode.statusMessageMu.Lock()
	mode.statusNoticeStarted = time.Now().Add(-2800 * time.Millisecond)
	mode.statusMessageMu.Unlock()
	if leaving := mode.animatedStatusNoticeText(); tui.VisibleWidth(replacing) != tui.VisibleWidth(entering) || !strings.Contains(replacing, "\x1b[38;2;") || leaving == entering {
		t.Fatalf("reasoning replacement closed the gap or missed its fade: replacing=%q entering=%q leaving=%q", replacing, entering, leaving)
	}
	mode.showStatusMessage("")
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
