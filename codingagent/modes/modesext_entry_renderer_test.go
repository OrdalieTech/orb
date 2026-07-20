package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/tui"
)

// Sweep regression (modes-ext): registerEntryRenderer must receive the whole
// custom session entry ({type, customType, data, id, parentId, timestamp}),
// not just the decoded data payload. Upstream addCustomEntryToChat passes the
// entry object into CustomEntryComponent, which forwards it verbatim to the
// renderer (interactive-mode.ts:3151-3157, components/custom-entry.ts), and
// both docs/extensions.md and examples/extensions/entry-renderer.ts read
// entry.data off that object.

type modesextRenderedText struct{ text string }

func (component modesextRenderedText) Render(int) []string { return []string{component.text} }

func TestEntryRendererReceivesWholeEntryObject(t *testing.T) {
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
	entryID, err := manager.AppendCustomEntry("probe-card", map[string]any{"message": "hello-entry"})
	if err != nil {
		t.Fatal(err)
	}

	var seen any
	registry := extensions.NewRegistry(cwd)
	err = registry.Register("<inline:probe>", func(api extensions.API) error {
		api.RegisterEntryRenderer("probe-card", func(entry any, _ extensions.EntryRenderOptions, _ extensions.Theme) extensions.Component {
			seen = entry
			// Mirrors the upstream-style renderer shape: read entry.data.message.
			message := "?"
			if object, ok := entry.(map[string]any); ok {
				if data, ok := object["data"].(map[string]any); ok {
					if text, ok := data["message"].(string); ok {
						message = text
					}
				}
			}
			return modesextRenderedText{text: "[probe] " + message}
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Dispose()

	mode := &InteractiveMode{
		session:        runtime,
		ui:             tui.NewTUI(newFakeTerminal(120, 40)),
		chat:           &tui.Container{},
		toolComponents: make(map[string]*ToolExecutionComponent),
		cwd:            cwd,
	}
	mode.renderInitialMessages()

	object, ok := seen.(map[string]any)
	if !ok {
		t.Fatalf("renderer argument = %T (%v), want the entry object", seen, seen)
	}
	if object["type"] != "custom" {
		t.Fatalf("entry.type = %v, want %q", object["type"], "custom")
	}
	if object["customType"] != "probe-card" {
		t.Fatalf("entry.customType = %v, want %q", object["customType"], "probe-card")
	}
	if object["id"] != entryID {
		t.Fatalf("entry.id = %v, want %q", object["id"], entryID)
	}
	if timestamp, ok := object["timestamp"].(string); !ok || timestamp == "" {
		t.Fatalf("entry.timestamp = %v, want a non-empty string", object["timestamp"])
	}
	data, ok := object["data"].(map[string]any)
	if !ok {
		t.Fatalf("entry.data = %v, want the data payload object", object["data"])
	}
	if data["message"] != "hello-entry" {
		t.Fatalf("entry.data.message = %v, want %q", data["message"], "hello-entry")
	}
	rendered := strings.Join(mode.chat.Render(120), "\n")
	if !strings.Contains(rendered, "[probe] hello-entry") {
		t.Fatalf("chat render = %q, want it to contain %q", rendered, "[probe] hello-entry")
	}
}
