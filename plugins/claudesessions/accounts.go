package claudesessions

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// A Claude account is a Claude Code configuration directory. The official CLI
// signs in and keeps the credential; Orb never reads it, it only chooses the
// directory a Claude session runs with. Accounts are ordinary provider
// accounts: listed, named, switched and removed like any other, and resolved
// through the same auth pipeline, which hands the directory to the driver.
//
// Everything but the sign-in is shared with the user's own Claude
// configuration through symlinks, so settings, skills, agents, hooks and
// transcripts follow across accounts, and a conversation continues when the
// account changes.

// accountDirHeader carries an account's directory from its credential to the
// driver; it never reaches a network request.
const accountDirHeader = "x-orb-claude-config-dir"

// sharedConfig is what every account links from the user's own configuration.
var sharedConfig = []string{
	"projects", "settings.json", "CLAUDE.md", "agents", "commands", "skills", "plugins", "hooks",
	"output-styles", "keybindings.json", "todos", "file-history", "shell-snapshots", "session-env", "history.jsonl",
}

type claudeStatus struct {
	LoggedIn         bool   `json:"loggedIn"`
	Email            string `json:"email"`
	OrgName          string `json:"orgName"`
	SubscriptionType string `json:"subscriptionType"`
}

// label names a signed-in Claude login the way Claude does: email and plan.
func (status claudeStatus) label() string {
	label := status.Email
	if label == "" {
		label = "Claude Code"
	}
	if status.SubscriptionType != "" {
		label += " · " + strings.ToUpper(status.SubscriptionType[:1]) + status.SubscriptionType[1:]
	}
	return label
}

// authStatus asks the CLI who is signed in for env's configuration. A signed-out
// CLI still prints its JSON status and exits non-zero.
func authStatus(ctx context.Context, claude string, env []string) (claudeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "auth", "status", "--json")
	cmd.Env = env
	out, runErr := cmd.Output()
	var status claudeStatus
	if err := json.Unmarshal(out, &status); err != nil {
		if runErr != nil {
			return status, runErr
		}
		return status, err
	}
	return status, nil
}

// baseConfig is the configuration the user runs claude with: its directory and
// the state file beside it.
func baseConfig(env []string) (dir, state string) {
	for _, item := range slices.Backward(env) {
		if value, ok := strings.CutPrefix(item, "CLAUDE_CONFIG_DIR="); ok && value != "" {
			return value, filepath.Join(value, ".claude.json")
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")
}

func withConfigDir(env []string, dir string) []string {
	env = slices.DeleteFunc(slices.Clone(env), func(item string) bool { return strings.HasPrefix(item, "CLAUDE_CONFIG_DIR=") })
	if dir == "" {
		return env
	}
	return append(env, "CLAUDE_CONFIG_DIR="+dir)
}

// prepareAccount links the shared configuration into an account directory
// and copies the user's MCP servers into its state; both are cheap no-ops once
// in place, so every session start keeps them current.
func prepareAccount(dir string, env []string) error {
	base, state := baseConfig(env)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Transcripts must be shared even before the user's first native session.
	_ = os.MkdirAll(filepath.Join(base, "projects"), 0o700)
	for _, name := range sharedConfig {
		target, link := filepath.Join(base, name), filepath.Join(dir, name)
		if _, err := os.Stat(target); err != nil {
			continue
		}
		if _, err := os.Lstat(link); err == nil {
			continue
		}
		// ponytail: without symlinks (Windows without developer mode) an account
		// keeps its own configuration and cannot resume the others' sessions.
		_ = os.Symlink(target, link)
	}
	return syncMCPServers(state, filepath.Join(dir, ".claude.json"))
}

func syncMCPServers(from, to string) error {
	read := func(path string) map[string]json.RawMessage {
		values := map[string]json.RawMessage{}
		if data, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(data, &values)
		}
		return values
	}
	source, target := read(from), read(to)
	if string(source["mcpServers"]) == string(target["mcpServers"]) {
		return nil
	}
	if servers, ok := source["mcpServers"]; ok {
		target["mcpServers"] = servers
	} else {
		delete(target, "mcpServers")
	}
	data, err := json.MarshalIndent(target, "", "  ")
	if err != nil {
		return err
	}
	temporary := to + ".orb"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, to)
}

func accountsDir(agentDir string) string { return filepath.Join(agentDir, "plugins", Name, "accounts") }

// accountDir reads the directory a stored Claude account runs with.
func accountDir(credential *aiauth.Credential) string {
	if credential == nil {
		return ""
	}
	var dir string
	_ = json.Unmarshal(credential.Extra["claudeConfigDir"], &dir)
	return dir
}

var terminalCodes = regexp.MustCompile(`\x1b\]8;[^\x07\x1b]*(\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-z]`)
var loginURL = regexp.MustCompile(`https://\S+`)

// login runs the CLI's own sign-in against env's configuration. For a first
// account the CLI opens the browser and waits for its callback. A browser
// already signed in to Claude would approve that account at once, so adding
// another one only shows the link, to open where the other account is signed
// in, and takes the code it shows.
func login(ctx context.Context, claude string, env []string, another bool, interaction aiauth.AuthInteraction) error {
	message := "Sign in to Claude in the browser that just opened. On another machine, open this link and paste the code it shows."
	if another {
		// ponytail: BROWSER is how the CLI opens its page; true opens nothing.
		env = append(slices.Clone(env), "BROWSER=true")
		message = "Open this link in a private window, or a browser signed in to the Claude account to add, then paste the code it shows."
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "auth", "login")
	cmd.Env = env
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	reader, writer := io.Pipe()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		return err
	}
	var output strings.Builder
	var mu sync.Mutex
	go func() {
		scanner := bufio.NewScanner(reader)
		shown := false
		for scanner.Scan() {
			line := terminalCodes.ReplaceAllString(scanner.Text(), "")
			mu.Lock()
			output.WriteString(line + "\n")
			mu.Unlock()
			if url := loginURL.FindString(line); url != "" && !shown {
				shown = true
				interaction.Notify(aiauth.AuthEvent{
					Type:    aiauth.EventInfo,
					Message: message,
					Links:   []aiauth.AuthInfoLink{{URL: url, Label: "Sign-in page"}},
				})
				go func() {
					code, err := interaction.Prompt(ctx, aiauth.AuthPrompt{Type: aiauth.PromptManualCode, Message: "Code from the sign-in page (only if asked)", Placeholder: "code"})
					if err == nil && strings.TrimSpace(code) != "" {
						_, _ = io.WriteString(stdin, strings.TrimSpace(code)+"\n")
					}
				}()
			}
		}
	}()
	err = cmd.Wait()
	_ = writer.Close()
	if err != nil {
		if ctx.Err() != nil {
			return context.Canceled
		}
		mu.Lock()
		detail := strings.TrimSpace(output.String())
		mu.Unlock()
		if lines := strings.Split(detail, "\n"); detail != "" {
			return errors.New(lines[len(lines)-1])
		}
		return err
	}
	return nil
}

// logout signs a Claude account out and deletes its directory; the shared
// configuration it links to is untouched.
func logout(claude string, env []string, agentDir, dir string) {
	if dir == "" || filepath.Dir(dir) != accountsDir(agentDir) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, claude, "auth", "logout")
	cmd.Env = withConfigDir(env, dir)
	_ = cmd.Run()
	_ = os.RemoveAll(dir)
}

// provider registers Claude among Orb's providers: its models, its accounts,
// and the Claude Code login the user already has.
type provider struct {
	settings *config.SettingsManager
	agentDir string
	env      []string

	mu         sync.Mutex
	models     []ai.Model
	refreshing bool
}

// ambient caches the user's own Claude Code login: availability is asked on
// every model listing, and the CLI takes up to seconds to answer.
var ambient struct {
	sync.Mutex
	status  claudeStatus
	checked time.Time
	pending chan struct{} // closed when the check in flight ends
}

func newProvider(settings *config.SettingsManager, agentDir string, env []string) *provider {
	p := &provider{settings: settings, agentDir: agentDir, env: env}
	if raw, ok := settings.GetPluginSettings(Name)["catalog"]; ok {
		data, _ := json.Marshal(raw)
		_ = json.Unmarshal(data, &p.models)
	}
	return p
}

func (p *provider) claude() (string, error) {
	name, _ := p.settings.GetPluginSettings(Name)["claude"].(string)
	if name == "" {
		name = "claude"
	}
	return executable(name, p.env)
}

// ambientStatus is the user's own Claude Code login, rechecked at most every
// half minute. Only the first reading is waited for: later ones refresh in the
// background while the last one answers, so listing models never waits on the CLI.
func (p *provider) ambientStatus(ctx context.Context) claudeStatus {
	ambient.Lock()
	if ambient.pending == nil && time.Since(ambient.checked) >= 30*time.Second {
		done := make(chan struct{})
		ambient.pending = done
		go func() {
			var status claudeStatus
			if claude, err := p.claude(); err == nil {
				status, _ = authStatus(context.Background(), claude, p.env)
			}
			ambient.Lock()
			ambient.status, ambient.checked, ambient.pending = status, time.Now(), nil
			ambient.Unlock()
			close(done)
		}()
	}
	pending, first := ambient.pending, ambient.checked.IsZero()
	ambient.Unlock()
	if first && pending != nil {
		select {
		case <-pending:
		case <-ctx.Done():
		}
	}
	ambient.Lock()
	defer ambient.Unlock()
	return ambient.status
}

func (p *provider) registration() extensions.Provider {
	go p.ambientStatus(context.Background()) // the first reading is ready before anyone lists models
	unavailable := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		return nil, errors.New("Claude runs in its own conversation; select a Claude model to start one") //nolint:staticcheck // Product text.
	}
	return extensions.Provider{
		ID: Name, Name: "Claude",
		Auth:          aiauth.ProviderAuth{OAuth: claudeLogin{p}, APIKey: claudeCode{p}},
		GetModels:     p.catalog,
		RefreshModels: p.refresh,
		Stream:        engine.StreamFn(unavailable), StreamSimple: engine.StreamFn(unavailable),
	}
}

func (p *provider) catalog() ([]ai.Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.models), nil
}

// refresh rediscovers the catalog in the background, one discovery at a time,
// and keeps it for the next listing and start: asking Claude takes a CLI start,
// and a listing (opening the model picker) never waits for it.
func (p *provider) refresh(refresh extensions.RefreshModelsContext) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshing {
		return nil
	}
	p.refreshing = true
	go func() {
		defer func() {
			p.mu.Lock()
			p.refreshing = false
			p.mu.Unlock()
		}()
		ctx := context.Background()
		options, err := configuredOptions(ctx, p.settings, p.agentDir, p.env)
		if err != nil {
			return
		}
		if dir := accountDir(refresh.Credential); dir != "" {
			options.Env = withConfigDir(options.Env, dir)
		}
		cwd, _ := os.Getwd()
		models, err := discoverModels(ctx, options, cwd)
		if err != nil {
			return
		}
		p.mu.Lock()
		p.models = models
		p.mu.Unlock()
		p.settings.SetPluginSetting(Name, "catalog", models)
	}()
	return nil
}

// claudeLogin adds a Claude account: a fresh directory the CLI signs into.
type claudeLogin struct{ p *provider }

func (claudeLogin) Name() string       { return "Claude subscription" }
func (claudeLogin) LoginLabel() string { return "Sign in with Claude" }

func (method claudeLogin) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	claude, err := method.p.claude()
	if err != nil {
		return nil, errors.New("install the official Claude Code CLI on this host to add a Claude account")
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	dir := filepath.Join(accountsDir(method.p.agentDir), hex.EncodeToString(id))
	if err := prepareAccount(dir, method.p.env); err != nil {
		return nil, err
	}
	env := withConfigDir(method.p.env, dir)
	known := method.p.signedIn(ctx, claude, dir)
	if err := login(ctx, claude, env, len(known) > 0, interaction); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	status, err := authStatus(ctx, claude, env)
	if err != nil || !status.LoggedIn {
		_ = os.RemoveAll(dir)
		return nil, errors.New("claude did not finish signing in")
	}
	if slices.Contains(known, status.Email) {
		logout(claude, method.p.env, method.p.agentDir, dir)
		return nil, errors.New(status.Email + " is already connected; sign in with another Claude account")
	}
	credential := &aiauth.Credential{Type: aiauth.CredentialOAuth, Expires: time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()}
	for key, value := range map[string]string{"claudeConfigDir": dir, "email": status.Email, "label": status.label()} {
		raw, _ := json.Marshal(value)
		credential.SetExtra(key, raw)
	}
	return credential, nil
}

// signedIn lists the emails Claude is already signed in with: the user's own
// login and every account directory but except.
func (p *provider) signedIn(ctx context.Context, claude, except string) []string {
	var emails []string
	if status := p.ambientStatus(ctx); status.LoggedIn {
		emails = append(emails, status.Email)
	}
	entries, _ := os.ReadDir(accountsDir(p.agentDir))
	for _, entry := range entries {
		dir := filepath.Join(accountsDir(p.agentDir), entry.Name())
		if dir == except || !entry.IsDir() {
			continue
		}
		if status, err := authStatus(ctx, claude, withConfigDir(p.env, dir)); err == nil && status.LoggedIn {
			emails = append(emails, status.Email)
		}
	}
	return emails
}

// Refresh keeps the credential as is: the CLI refreshes its own sign-in.
func (claudeLogin) Refresh(_ context.Context, credential *aiauth.Credential) (*aiauth.Credential, error) {
	return credential, nil
}

func (claudeLogin) ToAuth(credential *aiauth.Credential) (aiauth.ModelAuth, error) {
	dir := accountDir(credential)
	if dir == "" {
		return aiauth.ModelAuth{}, errors.New("claude account has no configuration directory")
	}
	return aiauth.ModelAuth{Headers: map[string]*string{accountDirHeader: &dir}}, nil
}

// claudeCode is the user's own Claude Code login, used when no account is
// selected; Orb cannot add or remove it.
type claudeCode struct{ p *provider }

func (claudeCode) Name() string { return "Claude Code login" }

func (method claudeCode) Resolve(ctx context.Context, _ aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
	if credential != nil {
		return nil, nil
	}
	if status := method.p.ambientStatus(ctx); status.LoggedIn {
		return &aiauth.AuthResult{Source: status.label()}, nil
	}
	return nil, nil
}

func (method claudeCode) Check(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthCheck, error) {
	result, err := method.Resolve(ctx, authContext, credential)
	if err != nil || result == nil {
		return nil, err
	}
	return &aiauth.AuthCheck{Source: result.Source, Type: aiauth.CredentialOAuth}, nil
}

// AmbientAccount is the user's own Claude Code login as an account row, or
// false when it is signed out.
func AmbientAccount(ctx context.Context, settings *config.SettingsManager, env []string) (string, bool) {
	p := newProvider(settings, "", env)
	status := p.ambientStatus(ctx)
	return status.label(), status.LoggedIn
}

// Usage reads an account's plan limits as Claude's /usage shows them; a nil
// credential reads the user's own Claude Code login.
func Usage(ctx context.Context, settings *config.SettingsManager, agentDir string, env []string, credential *aiauth.Credential) (usage.Snapshot, error) {
	options, err := configuredOptions(ctx, settings, agentDir, env)
	if err != nil {
		return usage.Snapshot{}, err
	}
	if dir := accountDir(credential); dir != "" {
		options.Env = withConfigDir(options.Env, dir)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var response struct {
		Type, Plan string
		Limits     map[string]json.RawMessage
	}
	cwd, _ := os.Getwd()
	if oneShot(ctx, options, cwd, map[string]any{"usage": true}, &response) != nil || response.Type != "usage" || response.Limits == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	snapshot := usage.Snapshot{Plan: response.Plan, CheckedAt: time.Now()}
	for key, raw := range response.Limits {
		var window struct {
			Utilization *float64
			ResetsAt    *string `json:"resets_at"`
		}
		// Limits also carries non-window entries (extra usage, flags); only windows are shown.
		if label := limitLabel(key); label != "" && json.Unmarshal(raw, &window) == nil && window.Utilization != nil {
			entry := usage.Window{Name: label, Remaining: 100 - *window.Utilization}
			if window.ResetsAt != nil {
				entry.ResetsAt, _ = time.Parse(time.RFC3339, *window.ResetsAt)
			}
			snapshot.Windows = append(snapshot.Windows, entry)
		}
	}
	slices.SortFunc(snapshot.Windows, func(a, b usage.Window) int { return strings.Compare(a.Name, b.Name) })
	return snapshot, nil
}

// ForgetAccount signs a removed or replaced Claude account out and deletes its
// directory.
func ForgetAccount(settings *config.SettingsManager, agentDir string, env []string, credential *aiauth.Credential) {
	p := newProvider(settings, agentDir, env)
	if claude, err := p.claude(); err == nil {
		logout(claude, env, agentDir, accountDir(credential))
	}
}
