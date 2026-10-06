package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine"
)

func TestDebugCommandErrorsRemainInteractive(t *testing.T) {
	for _, kind := range []string{"agent directory", "log directory", "serialization"} {
		t.Run(kind, func(t *testing.T) {
			initF12RawTheme(t)
			cwd := t.TempDir()
			agentDir := filepath.Join(cwd, "agent")
			settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(agentDir))
			if err != nil {
				t.Fatal(err)
			}
			manager, err := sessionstore.InMemory(cwd)
			if err != nil {
				t.Fatal(err)
			}
			core := engine.NewAgent(nil)
			runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{Agent: core, SessionManager: manager, Settings: settings})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runtime.Dispose)
			mode := newF12HiddenCommandMode(100, 24)
			mode.session = runtime
			mode.ui.AddChild(mode.chat)
			debugPath := filepath.Join(agentDir, "pi-debug.log")
			want := ""
			switch kind {
			case "agent directory":
				if err := os.RemoveAll(agentDir); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(agentDir, []byte("not a directory"), 0600); err != nil {
					t.Fatal(err)
				}
				want = "create debug log directory"
			case "log directory":
				if err := os.MkdirAll(debugPath, 0700); err != nil {
					t.Fatal(err)
				}
				want = "write debug log"
			case "serialization":
				core.SetMessages(engine.AgentMessages{make(chan int)})
				want = "serialize debug message"
			}
			if !mode.handleSlashCommand("debug", "") {
				t.Fatal("debug not handled")
			}
			frame := strings.Join(normalizeF12Lines(mode.ui.Render(200)), "\n")
			if !strings.Contains(frame, "Error: "+want) {
				t.Fatalf("missing contextual error: %s", frame)
			}
			if strings.Contains(frame, "✓ Debug log written") {
				t.Fatalf("failure announced success: %s", frame)
			}
			if kind == "serialization" {
				if _, err := os.Stat(debugPath); !os.IsNotExist(err) {
					t.Fatalf("serialization failure wrote log: %v", err)
				}
			}
			core.SetMessages(nil)
			if err := os.RemoveAll(agentDir); err != nil {
				t.Fatal(err)
			}
			if !mode.handleSlashCommand("debug", "") {
				t.Fatal("subsequent debug not handled")
			}
			frame = strings.Join(normalizeF12Lines(mode.ui.Render(200)), "\n")
			if !strings.Contains(frame, "✓ Debug log written") {
				t.Fatalf("subsequent UI failed: %s", frame)
			}
			if _, err := os.ReadFile(debugPath); err != nil {
				t.Fatal(err)
			}
		})
	}
}
