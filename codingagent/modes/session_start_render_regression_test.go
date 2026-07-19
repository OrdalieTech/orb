package modes

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// recordingSessionHost captures the TUI callbacks so the test can drive the
// replacement flow the way cmd/pi's interactive host does: teardown, rebind
// (render + subscribe), then deferred session_start.
type recordingSessionHost struct {
	session          *codingagent.SessionRuntime
	rebind           func(*codingagent.SessionRuntime) error
	beforeInvalidate func()
	afterStart       func(*codingagent.SessionRuntime) error
}

func (host *recordingSessionHost) Session() *codingagent.SessionRuntime { return host.session }
func (host *recordingSessionHost) SetRebindSession(rebind func(*codingagent.SessionRuntime) error) {
	host.rebind = rebind
}
func (host *recordingSessionHost) SetBeforeSessionInvalidate(callback func()) {
	host.beforeInvalidate = callback
}
func (host *recordingSessionHost) SetAfterSessionStart(callback func(*codingagent.SessionRuntime) error) {
	host.afterStart = callback
}
func (host *recordingSessionHost) NewSession(context.Context, *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
	return extensions.SessionReplacementResult{}, errors.New("unused")
}
func (host *recordingSessionHost) SwitchSession(context.Context, string, string, *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
	return extensions.SessionReplacementResult{}, errors.New("unused")
}
func (host *recordingSessionHost) Fork(context.Context, string, *extensions.ForkOptions) (InteractiveForkResult, error) {
	return InteractiveForkResult{}, errors.New("unused")
}
func (host *recordingSessionHost) ImportSession(context.Context, string, string) (extensions.SessionReplacementResult, error) {
	return extensions.SessionReplacementResult{}, errors.New("unused")
}
func (host *recordingSessionHost) Reload(context.Context) error { return errors.New("unused") }
func (host *recordingSessionHost) ListProjectSessions(sessionstore.SessionListProgress) []sessionstore.SessionInfo {
	return nil
}
func (host *recordingSessionHost) ListAllSessions(sessionstore.SessionListProgress) []sessionstore.SessionInfo {
	return nil
}
func (host *recordingSessionHost) TrustState() (InteractiveTrustState, error) {
	return InteractiveTrustState{}, errors.New("unused")
}
func (host *recordingSessionHost) SetProjectTrust(context.Context, []config.ProjectTrustUpdate) error {
	return errors.New("unused")
}
func (host *recordingSessionHost) AuthOptions(context.Context) (InteractiveAuthOptions, error) {
	return InteractiveAuthOptions{}, errors.New("unused")
}
func (host *recordingSessionHost) Login(context.Context, string, aiauth.AuthType, aiauth.AuthInteraction) error {
	return errors.New("unused")
}
func (host *recordingSessionHost) Logout(context.Context, string) error { return errors.New("unused") }
func (host *recordingSessionHost) Dispose()                             {}

func newSessionStartRuntime(t *testing.T, settingsJSON map[string]any, registry *extensions.Registry, seed []any) *codingagent.SessionRuntime {
	t.Helper()
	cwd := t.TempDir()
	settings, err := config.NewSettingsManager(cwd, config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range settingsJSON {
		switch key {
		case "hideThinkingBlock":
			settings.SetHideThinkingBlock(value.(bool))
		}
	}
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range seed {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
		ExtensionRegistry: registry, ExtensionMode: extensions.ModeTUI, DeferSessionStart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// Port of upstream regression 5943-session-start-notify.test.ts for the
// surfaces pi-go has: a replacement renders its restored session state and is
// subscribed before deferred session_start handlers notify or send messages,
// and hide-thinking is refreshed from settings before the chat rebuild.
// (Upstream's loaded-resources container has no pi-go equivalent; the host
// ordering itself is pinned by cmd/pi interactive_host tests.)
func TestReplacementRendersAndSubscribesBeforeSessionStartHandlers(t *testing.T) {
	host := &recordingSessionHost{}
	initial := newSessionStartRuntime(t, nil, nil, []any{
		&ai.UserMessage{Content: ai.UserContent{Text: stringPtr("original message")}},
	})
	host.session = initial
	terminal := newLifecycleTerminal(100, 30)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() {
		done <- RunInteractiveMode(ctx, initial, InteractiveModeOptions{Terminal: terminal, Host: host})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("interactive mode did not stop")
		}
	}()
	if !terminal.waitFor("original message", 2*time.Second) {
		t.Fatalf("initial session did not render: %q", terminal.output())
	}
	if host.rebind == nil || host.beforeInvalidate == nil || host.afterStart == nil {
		t.Fatal("mode did not register host callbacks")
	}

	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:session-start>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(startCtx context.Context, _ extensions.Event, extensionContext extensions.Context) (any, error) {
			extensionContext.UI().Notify("HELLO-NOTIFY", extensions.NotifyError)
			return nil, api.SendMessage(startCtx, extensions.CustomMessage{
				CustomType: "session-start", Content: "custom from start", Display: true,
			}, nil)
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	replacement := newSessionStartRuntime(t, map[string]any{"hideThinkingBlock": true}, registry, []any{
		&ai.UserMessage{Content: ai.UserContent{Text: stringPtr("restored message")}},
		&ai.AssistantMessage{
			Content: ai.AssistantContent{&ai.ThinkingContent{Thinking: "SECRET-THOUGHT"}, &ai.TextContent{Text: "restored answer"}},
			API:     "faux", Provider: "faux", Model: "faux-1",
			StopReason: ai.StopReasonStop,
		},
	})

	// The host flow: shutdown+detach the old runtime, rebind the replacement
	// (render + subscribe), and only then fire the deferred session_start.
	host.beforeInvalidate()
	host.session = replacement
	if err := host.rebind(replacement); err != nil {
		t.Fatal(err)
	}
	if !terminal.waitFor("restored message", 2*time.Second) || !terminal.waitFor("restored answer", 2*time.Second) {
		t.Fatalf("replacement state not rendered before session_start: %q", terminal.output())
	}
	if strings.Contains(terminal.output(), "HELLO-NOTIFY") {
		t.Fatal("session_start notify fired before the replacement was rendered")
	}
	// hideThinkingBlock was refreshed from the replacement's settings before
	// the chat rebuild: thinking text must not be visible.
	if strings.Contains(terminal.output(), "SECRET-THOUGHT") {
		t.Fatalf("hide-thinking not refreshed before rebuild: %q", terminal.output())
	}

	replacement.StartExtensions()
	if err := host.afterStart(replacement); err != nil {
		t.Fatal(err)
	}
	if !terminal.waitFor("HELLO-NOTIFY", 2*time.Second) {
		t.Fatalf("session_start notify never rendered: %q", terminal.output())
	}
	// The mode subscribed before bind, so the handler's custom message was
	// observed as message events and rendered into the chat.
	if !terminal.waitFor("custom from start", 2*time.Second) {
		t.Fatalf("session_start custom message not rendered: %q", terminal.output())
	}
	output := terminal.output()
	if strings.Index(output, "restored message") > strings.Index(output, "HELLO-NOTIFY") {
		t.Fatalf("restored session state rendered after session_start notify:\n%s", output)
	}
}

func stringPtr(value string) *string { return &value }
