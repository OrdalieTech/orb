//go:build !windows

package claudesessions

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	plugins "github.com/OrdalieTech/orb/plugins/permissions"
)

// fakeClaudeCLI signs a config directory in by writing its email to "login";
// login prints the sign-in link, then takes the code from stdin, and records
// the BROWSER it was given.
const fakeClaudeCLI = `#!/bin/sh
dir="$CLAUDE_CONFIG_DIR"
case "$2" in
status)
  if [ -f "$dir/login" ]; then printf '{"loggedIn":true,"email":"%s","subscriptionType":"max"}' "$(cat "$dir/login")"; else printf '{"loggedIn":false}'; exit 1; fi ;;
login)
  printf '%s\n' "$BROWSER" > "$dir/browser"
  printf 'Opening browser to sign in…\nIf the browser didn'"'"'t open, visit: \033]8;;https://claude.test/authorize?x=1\033\\https://claude.test/authorize?x=1\033]8;;\033\\\nPaste code here if prompted > '
  read code
  [ "$code" = "good-code" ] || exit 1
  printf '%s' "$FAKE_EMAIL" > "$dir/login" ;;
logout)
  rm -f "$dir/login" ;;
esac
`

type fakeInteraction struct {
	code   string
	events []aiauth.AuthEvent
}

func (f *fakeInteraction) Prompt(context.Context, aiauth.AuthPrompt) (string, error) {
	return f.code, nil
}
func (f *fakeInteraction) Notify(event aiauth.AuthEvent) { f.events = append(f.events, event) }

func accountFixture(t *testing.T) (*provider, string) {
	t.Helper()
	root := t.TempDir()
	cli := filepath.Join(root, "claude")
	if err := os.WriteFile(cli, []byte(fakeClaudeCLI), 0o700); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(root, "base")
	for _, dir := range []string{base, filepath.Join(base, "skills")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "settings.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, ".claude.json"), []byte(`{"mcpServers":{"docs":{"command":"docs"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(root, config.WithAgentDir(filepath.Join(root, "agent")))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetPluginSetting(Name, "claude", cli)
	resetAmbient := func() {
		ambient.Lock()
		pending := ambient.pending
		ambient.Unlock()
		if pending != nil {
			<-pending
		}
		ambient.Lock()
		ambient.checked, ambient.status = time.Time{}, claudeStatus{}
		ambient.Unlock()
	}
	resetAmbient()
	t.Cleanup(resetAmbient)
	env := []string{"PATH=/usr/bin:/bin", "CLAUDE_CONFIG_DIR=" + base, "FAKE_EMAIL=second@example.com"}
	return newProvider(settings, filepath.Join(root, "agent"), env), base
}

func TestClaudeAccountSignsInThroughTheCLIAndSharesConfiguration(t *testing.T) {
	p, base := accountFixture(t)
	interaction := &fakeInteraction{code: "good-code"}
	credential, err := claudeLogin{p}.Login(t.Context(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	dir := accountDir(credential)
	if filepath.Dir(dir) != accountsDir(p.agentDir) {
		t.Fatalf("account directory %q is outside Orb's accounts", dir)
	}
	// The first account opens the browser; the link is shown without terminal codes.
	if browser, _ := os.ReadFile(filepath.Join(dir, "browser")); strings.TrimSpace(string(browser)) != "" {
		t.Fatalf("first account ran with BROWSER=%q", browser)
	}
	if len(interaction.events) != 1 || interaction.events[0].Links[0].URL != "https://claude.test/authorize?x=1" {
		t.Fatalf("sign-in events = %#v", interaction.events)
	}
	if name := credentialLabel(credential); name != "second@example.com · Max" {
		t.Fatalf("account label = %q", name)
	}
	auth, err := claudeLogin{p}.ToAuth(credential)
	if err != nil || *auth.Headers[accountDirHeader] != dir {
		t.Fatalf("account auth = %#v, %v", auth, err)
	}
	// Everything but the sign-in is the user's own configuration.
	for _, name := range []string{"settings.json", "skills", "projects"} {
		if target, err := os.Readlink(filepath.Join(dir, name)); err != nil || target != filepath.Join(base, name) {
			t.Fatalf("%s links to %q, %v", name, target, err)
		}
	}
	var state struct{ MCPServers map[string]any }
	data, _ := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if json.Unmarshal(data, &state) != nil || state.MCPServers["docs"] == nil {
		t.Fatalf("MCP servers were not shared: %s", data)
	}

	// A further account shows the link rather than letting a signed-in browser
	// approve the first account, and the same account is refused.
	second, err := claudeLogin{p}.Login(t.Context(), &fakeInteraction{code: "good-code"})
	if err == nil || !strings.Contains(err.Error(), "already connected") || second != nil {
		t.Fatalf("duplicate account = %#v, %v", second, err)
	}
	entries, _ := os.ReadDir(accountsDir(p.agentDir))
	if len(entries) != 1 {
		t.Fatalf("refused account left %d directories", len(entries))
	}

	// Removing the account signs it out and deletes only its own directory.
	logout(filepath.Join(filepath.Dir(base), "claude"), p.env, p.agentDir, dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("removed account directory remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "settings.json")); err != nil {
		t.Fatal("removing an account touched the shared configuration")
	}
}

func TestFurtherClaudeAccountShowsTheLinkInsteadOfOpeningTheBrowser(t *testing.T) {
	p, base := accountFixture(t)
	if err := os.WriteFile(filepath.Join(base, "login"), []byte("main@example.com"), 0o600); err != nil {
		t.Fatal(err)
	}
	var browser string
	credential, err := claudeLogin{p}.Login(t.Context(), &fakeInteraction{code: "good-code"})
	if err == nil {
		data, _ := os.ReadFile(filepath.Join(accountDir(credential), "browser"))
		browser = strings.TrimSpace(string(data))
	}
	if err != nil || browser != "true" {
		t.Fatalf("second account: BROWSER=%q, err=%v", browser, err)
	}
	if status := p.ambientStatus(t.Context()); !status.LoggedIn || status.label() != "main@example.com · Max" {
		t.Fatalf("ambient login = %#v", status)
	}
}

func TestCancelledClaudeSignInLeavesNothing(t *testing.T) {
	p, _ := accountFixture(t)
	if _, err := (claudeLogin{p}).Login(t.Context(), &fakeInteraction{code: "wrong"}); err == nil {
		t.Fatal("a failed sign-in added an account")
	}
	if entries, _ := os.ReadDir(accountsDir(p.agentDir)); len(entries) != 0 {
		t.Fatalf("failed sign-in left %d directories", len(entries))
	}
}

func credentialLabel(credential *aiauth.Credential) string {
	var label string
	_ = json.Unmarshal(credential.Extra["label"], &label)
	return label
}

// One conversation moves between an Orb model and Claude: Claude reads the
// turns another model answered, and the other model continues after Claude.
// shared is a runtime as the CLI builds it: an Orb model, Claude's turns through
// Configure, and the extensions register adds. live runs the official SDK
// (ORB_CLAUDE_LIVE_SDK) and Claude Code on this machine's account instead of fakes.
func shared(t *testing.T, live bool, register func(*extensions.Registry) error) (*agent.SessionRuntime, *faux.Provider, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for SDK host test", err)
	}
	dir := t.TempDir()
	sdk, cli := filepath.Join(dir, "sdk.mjs"), filepath.Join(dir, "claude")
	env := []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "SDK_TEST_KEY=unchanged", "CLAUDE_CONFIG_DIR=" + filepath.Join(dir, "claude-config")}
	if live {
		if sdk = os.Getenv("ORB_CLAUDE_LIVE_SDK"); sdk == "" {
			t.Skip("set ORB_CLAUDE_LIVE_SDK to the official sdk.mjs for a native account test")
		}
		if cli, err = exec.LookPath("claude"); err != nil {
			t.Fatal(err)
		}
		env = os.Environ()
	} else if err := errors.Join(os.WriteFile(sdk, []byte(fakeSDK), 0o600), os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o700)); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(dir, config.WithAgentDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	settings.SetPluginEnabled(Name, true)
	for key, value := range map[string]string{"sdk": sdk, "claude": cli, "node": node} {
		settings.SetPluginSetting(Name, key, value)
	}
	manager, err := session.InMemory(dir)
	if err != nil {
		t.Fatal(err)
	}
	orb := faux.New()
	agentState := engine.NewAgent(orb.StreamSimple, engine.WithInitialState(engine.AgentState{Model: orb.GetModel(), Messages: engine.AgentMessages{}}), engine.WithConvertToLLM(agent.ConvertToLLM))
	cfg := agent.SessionRuntimeConfig{Agent: agentState, SessionManager: manager, Settings: settings, StreamFn: orb.StreamSimple,
		GetAPIKey: func(context.Context, ai.ProviderID) (*string, error) { key := "test"; return &key, nil }}
	if register != nil {
		cfg.ExtensionRegistry = extensions.NewRegistry(dir)
		if err := register(cfg.ExtensionRegistry); err != nil {
			t.Fatal(err)
		}
	}
	bind, err := Configure(&cfg, dir, env)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bind(runtime)
	t.Cleanup(runtime.Dispose)
	return runtime, orb, dir
}

func TestOneConversationMovesBetweenOrbAndClaude(t *testing.T) {
	runtime, orb, dir := shared(t, false, nil)
	manager := runtime.Manager()
	reply := func() string {
		messages := runtime.State().Messages
		raw, _ := json.Marshal(messages[len(messages)-1])
		return string(raw)
	}
	claude := ai.Model{ID: "default", Provider: Name, API: Name}

	orb.SetResponses([]faux.ResponseStep{faux.AssistantMessage("noted mandarine")})
	if err := runtime.Prompt(t.Context(), "remember mandarine"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.SetModel(t.Context(), claude); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(t.Context(), "plain-fixture which word?"); err != nil {
		t.Fatal(err)
	}
	if got := reply(); !strings.Contains(got, `\"read\":[\"user:remember mandarine\",\"assistant:noted mandarine\"]`) {
		t.Fatalf("Claude did not read the Orb turn: %s", got)
	}
	if err := runtime.SetModel(t.Context(), *orb.GetModel()); err != nil {
		t.Fatal(err)
	}
	orb.SetResponses([]faux.ResponseStep{faux.AssistantMessage("back")})
	if err := runtime.Prompt(t.Context(), "and now?"); err != nil || !strings.Contains(reply(), "back") {
		t.Fatalf("Orb model did not continue after Claude: %v %s", err, reply())
	}
	if err := runtime.SetModel(t.Context(), claude); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(t.Context(), "plain-fixture still there?"); err != nil {
		t.Fatal(err)
	}
	if got := reply(); !strings.Contains(got, `\"user:plain-fixture which word?\"`) || !strings.Contains(got, `\"assistant:back\"`) {
		t.Fatalf("Claude lost its own turn or the Orb turn after it: %s", got)
	}
	// The conversation stays one Claude Code session, under its own ID.
	files, _ := filepath.Glob(filepath.Join(dir, "claude-config", "projects", "*", "*.jsonl"))
	if len(files) != 1 || filepath.Base(files[0]) != nativeSessionID(manager.GetSessionID())+".jsonl" {
		t.Fatalf("Claude Code sessions = %v", files)
	}
}

// Claude reaches the session's own tools through Orb's MCP server: it is offered
// only those it lacks, each call meets Orb's tool-call hooks once, where Orb runs
// it, and Orb records the call under the tool's own name.
func TestClaudeCallsSessionToolsThroughOrb(t *testing.T) {
	var hooks atomic.Int32
	runtime, _, _ := shared(t, false, func(registry *extensions.Registry) error {
		return registry.Register("echo", func(api extensions.API) error {
			// bash stands for the Orb tools Claude has its own of.
			for _, name := range []string{"echo", "bash"} {
				api.RegisterTool(extensions.ToolDefinition{
					Name: name, Label: name, Description: "Echo text.",
					Parameters: ai.JSONSchema(`{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`),
					Execute: func(_ context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
						return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: "echo " + raw.(map[string]any)["text"].(string)}}}, nil
					},
				})
			}
			api.On(extensions.EventToolCall, func(context.Context, extensions.Event, extensions.Context) (any, error) {
				hooks.Add(1)
				return nil, nil
			})
			return nil
		})
	})
	var rendered []string
	runtime.Subscribe(func(event any) {
		if start, ok := event.(engine.ToolExecutionStartEvent); ok {
			rendered = append(rendered, start.ToolName)
		}
	})
	if err := runtime.SetModel(t.Context(), ai.Model{ID: "default", Provider: Name, API: Name}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Prompt(t.Context(), `orb-mcp:[{"hook":"mcp__orb__echo"},{"list":true},{"call":"echo","args":{"text":"hi"},"render":true}]`); err != nil {
		t.Fatal(err)
	}
	if msg := runtime.State().ErrorMessage; msg != nil {
		t.Fatal(*msg)
	}
	messages := runtime.State().Messages
	reply, _ := messages[len(messages)-1].(*ai.AssistantMessage)
	if reply == nil || len(reply.Content) == 0 {
		t.Fatalf("no reply: %+v", messages[len(messages)-1])
	}
	var seen struct {
		Out     []json.RawMessage
		Allowed []string
	}
	if err := json.Unmarshal([]byte(reply.Content[0].(*ai.TextContent).Text), &seen); err != nil {
		t.Fatal(err)
	}
	var hook map[string]any
	var listed []string
	var called struct {
		Content []struct{ Text string }
		IsError bool
	}
	if len(seen.Out) != 3 || json.Unmarshal(seen.Out[0], &hook) != nil || json.Unmarshal(seen.Out[1], &listed) != nil || json.Unmarshal(seen.Out[2], &called) != nil {
		t.Fatalf("script answered %s", reply.Content[0].(*ai.TextContent).Text)
	}
	if !slices.Equal(listed, []string{"echo"}) {
		t.Errorf("offered %v, want echo alone", listed)
	}
	if len(hook) != 0 || !slices.Contains(seen.Allowed, "mcp__orb") {
		t.Errorf("Claude's own permission check took part: hook %v, allowed %v", hook, seen.Allowed)
	}
	if called.IsError || len(called.Content) != 1 || called.Content[0].Text != "echo hi" || hooks.Load() != 1 {
		t.Errorf("call %+v after %d tool-call hooks, want echo hi after one", called, hooks.Load())
	}
	if !slices.Equal(rendered, []string{"echo"}) {
		t.Errorf("Orb rendered %v, want the call as echo", rendered)
	}
}

// Stopping the Orb run stops the Orb tools Claude called in it, whether or not
// Claude cancels its own request.
func TestStoppingTheRunStopsOrbToolsClaudeCalled(t *testing.T) {
	started, stopped := make(chan time.Time, 1), make(chan time.Time, 1)
	runtime, _, _ := shared(t, false, func(registry *extensions.Registry) error {
		return registry.Register("slow", func(api extensions.API) error {
			api.RegisterTool(extensions.ToolDefinition{
				Name: "slow", Label: "slow", Description: "Waits.", Parameters: ai.JSONSchema(`{"type":"object"}`),
				Execute: func(ctx context.Context, _ string, _ any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
					started <- time.Now()
					<-ctx.Done()
					stopped <- time.Now()
					return engine.AgentToolResult{}, ctx.Err()
				},
			})
			return nil
		})
	})
	if err := runtime.SetModel(t.Context(), ai.Model{ID: "default", Provider: Name, API: Name}); err != nil {
		t.Fatal(err)
	}
	go func() {
		<-started
		runtime.Abort()
	}()
	aborted := time.Now()
	_ = runtime.Prompt(t.Context(), `orb-mcp:[{"call":"slow","args":{}}]`)
	select {
	case at := <-stopped:
		if at.Sub(aborted) > 2*time.Second {
			t.Fatalf("the tool stopped %v after the run started, only when its host went", at.Sub(aborted))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the Orb tool outlived the run it was called in")
	}
}

// Against the official SDK and Claude Code (ORB_CLAUDE_LIVE_SDK, this machine's
// account): Claude calls an Orb tool and sees the offer follow the session on
// the same host, Orb's policy refuses an unanswered ask, stopping the run stops
// the tool, and plain Claude Code resumes the conversation without Orb's server.
func TestLiveClaudeCallsSessionToolsThroughOrb(t *testing.T) {
	var echoed, shouted, guarded, waiting, stopped atomic.Int32
	runtime, _, dir := shared(t, true, func(registry *extensions.Registry) error {
		if err := registry.Register("permissions", plugins.Extension(&plugins.Policy{Mode: "enforce", Rules: []plugins.Rule{{Tool: "guarded", Action: plugins.Ask}}}, nil, nil)); err != nil {
			return err
		}
		return registry.Register("live", func(api extensions.API) error {
			schema := ai.JSONSchema(`{"type":"object","required":["text"],"properties":{"text":{"type":"string"}}}`)
			for name, run := range map[string]func(context.Context, string) (string, error){
				"echo": func(_ context.Context, text string) (string, error) { echoed.Add(1); return "ECHO:" + text, nil },
				"shout": func(_ context.Context, text string) (string, error) {
					shouted.Add(1)
					return "SHOUT:" + strings.ToUpper(text), nil
				},
				"guarded": func(_ context.Context, text string) (string, error) { guarded.Add(1); return text, nil },
				"slow": func(ctx context.Context, _ string) (string, error) {
					waiting.Add(1)
					<-ctx.Done()
					stopped.Add(1)
					return "", ctx.Err()
				},
			} {
				api.RegisterTool(extensions.ToolDefinition{
					Name: name, Label: name, Description: "Runs " + name + " on the text.", Parameters: schema,
					Execute: func(ctx context.Context, _ string, raw any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
						text, err := run(ctx, raw.(map[string]any)["text"].(string))
						return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}}, err
					},
				})
			}
			return nil
		})
	})
	var rendered []string
	runtime.Subscribe(func(event any) {
		if start, ok := event.(engine.ToolExecutionStartEvent); ok {
			rendered = append(rendered, start.ToolName)
		}
	})
	if err := runtime.SetModel(t.Context(), ai.Model{ID: "haiku", Provider: Name, API: Name}); err != nil {
		t.Fatal(err)
	}
	prompt := func(text string) string {
		ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
		defer cancel()
		if err := runtime.Prompt(ctx, text); err != nil {
			t.Fatal(err)
		}
		messages := runtime.State().Messages
		raw, _ := json.Marshal(messages[len(messages)-1])
		return string(raw)
	}
	if err := runtime.SetActiveToolsByName([]string{"echo", "guarded", "slow"}); err != nil {
		t.Fatal(err)
	}
	if reply := prompt("Call the mcp__orb__echo tool with text 'live' and reply with exactly its output. No other tools."); !strings.Contains(reply, "ECHO:live") || echoed.Load() != 1 || !slices.Contains(rendered, "echo") {
		t.Fatalf("echo: %d runs, rendered %v, reply %s", echoed.Load(), rendered, reply)
	}
	if err := runtime.SetActiveToolsByName([]string{"shout", "guarded", "slow"}); err != nil {
		t.Fatal(err)
	}
	if reply := prompt("Call the mcp__orb__shout tool with text 'hey' and reply with exactly its output. No other tools."); !strings.Contains(reply, "SHOUT:HEY") || shouted.Load() != 1 {
		t.Fatalf("shout after the offer changed: %d runs, reply %s", shouted.Load(), reply)
	}
	prompt("Call the mcp__orb__guarded tool once with text 'x', then reply with the result or error you got. No other tools.")
	if guarded.Load() != 0 {
		t.Fatal("an unanswered ask ran the tool")
	}
	go func() {
		for waiting.Load() == 0 {
			time.Sleep(50 * time.Millisecond)
		}
		runtime.Abort()
	}()
	prompt("Call the mcp__orb__slow tool with text 'x' and wait for it.")
	if stopped.Load() != 1 {
		t.Fatalf("stopping the run left the tool running (%d stopped)", stopped.Load())
	}
	resume := exec.Command("claude", "--resume", nativeSessionID(runtime.Manager().GetSessionID()), "-p", "Reply exactly RESUMED_OK.", "--model", "haiku")
	resume.Dir = dir
	if out, err := resume.CombinedOutput(); err != nil || !strings.Contains(string(out), "RESUMED_OK") {
		t.Fatalf("claude --resume: %v %s", err, out)
	}
}

// An account's plan limits read as the other providers' usage windows.
func TestClaudeAccountUsage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("Node required for SDK host test", err)
	}
	dir := t.TempDir()
	sdk, cli := filepath.Join(dir, "sdk.mjs"), filepath.Join(dir, "claude")
	if err := os.WriteFile(sdk, []byte(fakeSDK), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cli, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsManager(dir, config.WithAgentDir(dir))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range map[string]string{"sdk": sdk, "claude": cli, "node": node} {
		settings.SetPluginSetting(Name, key, value)
	}
	snapshot, err := Usage(t.Context(), settings, dir, []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin"}, nil)
	if err != nil || snapshot.Plan != "max" || len(snapshot.Windows) != 2 {
		t.Fatalf("usage = %+v, %v", snapshot, err)
	}
	if w := snapshot.Windows[0]; w.Name != "5h" || w.Remaining != 93 || w.ResetsAt.IsZero() || snapshot.Windows[1].Remaining != 32 {
		t.Fatalf("windows = %+v", snapshot.Windows)
	}
}

// Listing models never waits on the CLI once Claude's login was read: an old
// reading answers at once while a fresh one is taken in the background.
func TestAmbientLoginAnswersWhileRechecking(t *testing.T) {
	p, _ := accountFixture(t)
	ambient.Lock()
	ambient.checked, ambient.status = time.Now().Add(-time.Minute), claudeStatus{LoggedIn: true, Email: "old@example.com"}
	ambient.Unlock()
	start := time.Now()
	if status := p.ambientStatus(t.Context()); status.Email != "old@example.com" || time.Since(start) > 100*time.Millisecond {
		t.Fatalf("status %+v after %v", status, time.Since(start))
	}
	ambient.Lock()
	pending := ambient.pending
	ambient.Unlock()
	if pending == nil {
		t.Fatal("no fresh reading was started")
	}
	<-pending
	if status := p.ambientStatus(t.Context()); status.Email == "old@example.com" {
		t.Fatalf("status was not refreshed: %+v", status)
	}
}
