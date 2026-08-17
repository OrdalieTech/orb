package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

// Upstream 518855dd: custom message renderers receive the outputPad setting,
// and rebuilding messages after an outputPad change re-invokes them with the
// new value.
func TestRenderCustomMessagePassesOutputPadToRenderer(t *testing.T) {
	initTestTheme(t)
	cwd, agentDir := t.TempDir(), t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	var seen []extensions.MessageRenderOptions
	if err := registry.Register("pad-probe-extension", func(api extensions.API) error {
		api.RegisterMessageRenderer("pad-probe", func(_ extensions.CustomMessage, options extensions.MessageRenderOptions, _ extensions.Theme) extensions.Component {
			seen = append(seen, options)
			return tui.NewText("probe", options.OutputPad, 0, nil)
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Dispose)

	mode := &InteractiveMode{session: runtime, chat: &tui.Container{}, outputPad: 1}
	mode.renderCustomMessage("pad-probe", "hello", nil)
	if len(seen) != 1 || seen[0].Expanded || seen[0].OutputPad != 1 {
		t.Fatalf("renderer options = %#v, want outputPad 1", seen)
	}

	// The live output-padding setting rebuilds custom messages with the new
	// pad value.
	mode.mu.Lock()
	mode.outputPad = 0
	mode.mu.Unlock()
	mode.renderCustomMessage("pad-probe", "hello", nil)
	if len(seen) != 2 || seen[1].OutputPad != 0 {
		t.Fatalf("rebuilt renderer options = %#v, want outputPad 0", seen)
	}
}
