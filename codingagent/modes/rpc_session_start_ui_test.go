package modes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
)

// Partial-fix regression (out-of-box RPC UI): an extension's session_start
// handler must observe a live ctx.ui, not the headless noop. The RPC path now
// builds the runtime with DeferSessionStart so session_start fires inside
// bindReplacement AFTER BindExtensionUI, mirroring upstream (bind UI, then emit
// session_start). Before the fix, session_start fired at construction with the
// noop UI and its notify/setTitle/setWidget were silently dropped.
func TestRPCSessionStartSeesLiveExtensionUI(t *testing.T) {
	root := t.TempDir()
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.InMemory(root, sessionstore.WithSessionID("start-ui"))
	if err != nil {
		t.Fatal(err)
	}
	var startHadUI bool
	registry := extensions.NewRegistry(root)
	if err := registry.Register("<inline:start-ui>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			startHadUI = ctx.HasUI()
			ctx.UI().Notify("session_start saw ui", extensions.NotifyInfo)
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	created := agent.NewAgent(nil, agent.WithInitialState(agent.AgentState{Messages: agent.AgentMessages{}}))
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: created, SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeRPC,
		DeferSessionStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// The notify fires during bindSession (before any input line), so close
	// stdin immediately and let RPC mode drain and exit.
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- RunRPCMode(context.Background(), &rpcTestHost{runtime: runtime}, RPCModeOptions{
			Stdin: io.NopCloser(bytes.NewReader(nil)), Stdout: &stdout, Stderr: &stderr,
		})
	}()
	select {
	case exitCode := <-done:
		if exitCode != 0 || stderr.Len() != 0 {
			t.Fatalf("exit=%d stderr=%q", exitCode, stderr.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("RPC mode did not stop")
	}

	if !startHadUI {
		t.Fatal("session_start ran with HasUI()=false; UI bound after session_start")
	}
	var sawNotify bool
	for _, line := range bytes.Split(bytes.TrimSuffix(stdout.Bytes(), []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var probe struct {
			Type    string `json:"type"`
			Method  string `json:"method"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			t.Fatalf("bad line %s: %v", line, err)
		}
		if probe.Type == "extension_ui_request" && probe.Method == "notify" && probe.Message == "session_start saw ui" {
			sawNotify = true
		}
	}
	if !sawNotify {
		t.Fatalf("session_start notify never reached the wire: %s", stdout.String())
	}
}
