//go:build !windows

package claudesessions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
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
