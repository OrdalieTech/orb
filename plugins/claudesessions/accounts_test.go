//go:build !windows

package claudesessions

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
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
		ambient.checked = time.Time{}
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
func TestOneConversationMovesBetweenOrbAndClaude(t *testing.T) {
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
	bind, err := Configure(&cfg, dir, []string{"PATH=" + filepath.Dir(node) + ":/usr/bin:/bin", "SDK_TEST_KEY=unchanged", "CLAUDE_CONFIG_DIR=" + filepath.Join(dir, "claude-config")})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	bind(runtime)
	t.Cleanup(runtime.Dispose)
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
}
