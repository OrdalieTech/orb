package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/ai/providers"
	"github.com/OrdalieTech/orb/internal/uuidv7"
	"github.com/OrdalieTech/orb/platforms/native/sqlite"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// interactiveSessionHost serves the TUI from the CLI's session runtime: it
// reloads by rebuilding the runtime, imports native exports, and answers the
// accounts, trust and session lists the TUI shows.
type interactiveSessionHost struct {
	*agent.AgentSessionRuntime

	usageCache usage.Cache
	mu         sync.Mutex
	args       CLIArgs
	agentDir   string
	inputs     runtimeInputs
}

func newInteractiveSessionHost(
	ctx context.Context,
	args CLIArgs,
	dependencies cliDependencies,
	manager *session.SessionManager,
	agentDir string,
	stderr io.Writer,
) (*interactiveSessionHost, error) {
	host := &interactiveSessionHost{args: args, agentDir: agentDir}
	runtime, err := newCLISessionRuntimeHost(ctx, cliSessionRuntimeHostOptions{
		Args: &host.args, Manager: manager, Dependencies: dependencies, Stderr: stderr, ExtensionMode: extensions.ModeTUI,
		Created: func(inputs runtimeInputs) {
			host.mu.Lock()
			host.inputs = inputs
			host.mu.Unlock()
		},
	})
	if err != nil {
		return nil, err
	}
	host.AgentSessionRuntime = runtime
	runtime.SetReload(host.Reload)
	return host, nil
}

var _ modes.InteractiveSessionHost = (*interactiveSessionHost)(nil)

func (host *interactiveSessionHost) currentInputs() runtimeInputs {
	host.mu.Lock()
	defer host.mu.Unlock()
	return host.inputs
}

func (host *interactiveSessionHost) SwitchSession(ctx context.Context, sessionPath, cwdOverride string, options *extensions.SwitchSessionOptions) (extensions.SessionReplacementResult, error) {
	switchOptions := &agent.AgentSessionRuntimeSwitchOptions{CWDOverride: cwdOverride}
	if options != nil {
		switchOptions.WithSession = options.WithSession
	}
	if host.args.native != nil {
		// Native conversations open from the store even when the current one
		// is not stored there (--no-session).
		switchOptions.Repo = host.args.native.Sessions()
	}
	return host.AgentSessionRuntime.SwitchSession(ctx, sessionPath, switchOptions)
}

func (host *interactiveSessionHost) Fork(ctx context.Context, entryID string, options *extensions.ForkOptions) (modes.InteractiveForkResult, error) {
	result, err := host.AgentSessionRuntime.Fork(ctx, entryID, options)
	forked := modes.InteractiveForkResult{Cancelled: result.Cancelled}
	if result.SelectedText != nil {
		forked.SelectedText = *result.SelectedText
	}
	return forked, err
}

// importCopy writes inputPath's journal under a new session ID, for an import
// whose ID is already stored with other content.
func importCopy(inputPath string) (string, error) {
	path, err := config.NormalizePath(inputPath)
	if err == nil {
		path, err = filepath.Abs(path)
	}
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	first, rest, _ := bytes.Cut(data, []byte("\n"))
	var header map[string]json.RawMessage
	if err := json.Unmarshal(first, &header); err != nil {
		return "", err
	}
	id, err := uuidv7.Generate(time.Now())
	if err != nil {
		return "", err
	}
	header["id"], _ = json.Marshal(id)
	if first, err = json.Marshal(header); err != nil {
		return "", err
	}
	file, err := os.CreateTemp("", "orb-import-*.jsonl")
	if err != nil {
		return "", err
	}
	_, err = file.Write(append(append(first, '\n'), rest...))
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return file.Name(), err
}

func (host *interactiveSessionHost) ImportSession(ctx context.Context, inputPath, cwdOverride string) (extensions.SessionReplacementResult, error) {
	if host.args.native == nil {
		return host.ImportFromJSONL(ctx, inputPath, cwdOverride)
	}
	result, err := host.SwitchSession(ctx, inputPath, cwdOverride, nil)
	if !errors.Is(err, sqlite.ErrImportConflict) {
		return result, err
	}
	// An export of a conversation that went on since opens as a copy of its own.
	copied, copyErr := importCopy(inputPath)
	if copyErr != nil {
		return result, err
	}
	defer func() { _ = os.Remove(copied) }()
	return host.SwitchSession(ctx, copied, cwdOverride, nil)
}

// Reload rebuilds the runtime on the same session so settings, extensions,
// tools, resources, auth, and the model catalog are read again.
func (host *interactiveSessionHost) Reload(ctx context.Context) error {
	if err := host.Rebuild(ctx); err != nil {
		return err
	}
	return host.args.bridgeLink.configureBridge(host.args.BridgeProfile != "" || host.currentInputs().Settings.GetPlugins()["bridge"])
}

func (host *interactiveSessionHost) Dispose() {
	host.AgentSessionRuntime.Dispose(context.Background())
}

func (host *interactiveSessionHost) ListProjectSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	manager := host.Session().Manager()
	if host.args.native != nil {
		rows, _ := host.args.native.Sessions().ListInfo(context.Background(), manager.GetCWD(), nil)
		return spoken(rows)
	}
	return session.List(manager.GetCWD(), manager.GetSessionDir(), onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListAllSessions(onProgress session.SessionListProgress) []session.SessionInfo {
	if host.args.native != nil {
		rows, _ := host.args.native.Sessions().ListInfo(context.Background(), "", nil)
		return spoken(rows)
	}
	return session.ListAll(host.allSessionsDir(), onProgress, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListProjectSessionsContext(ctx context.Context, onUpdate session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
	manager := host.Session().Manager()
	if host.args.native != nil {
		rows, err := host.args.native.Sessions().ListInfo(ctx, manager.GetCWD(), spokenUpdates(onUpdate))
		return spoken(rows), err
	}
	return session.ListContext(ctx, manager.GetCWD(), manager.GetSessionDir(), onUpdate, session.WithAgentDir(host.agentDir))
}

func (host *interactiveSessionHost) ListAllSessionsContext(ctx context.Context, onUpdate session.SessionListUpdateFunc) ([]session.SessionInfo, error) {
	if host.args.native != nil {
		rows, err := host.args.native.Sessions().ListInfo(ctx, "", spokenUpdates(onUpdate))
		return spoken(rows), err
	}
	return session.ListAllContext(ctx, host.allSessionsDir(), onUpdate, session.WithAgentDir(host.agentDir))
}

// allSessionsDir is the directory every project's sessions are listed from:
// "" lists them under the agent directory.
func (host *interactiveSessionHost) allSessionsDir() string {
	if manager := host.Session().Manager(); !manager.UsesDefaultSessionDir() {
		return manager.GetSessionDir()
	}
	return ""
}

// spoken leaves out conversations nobody wrote in, as the current one is
// until its first message; the ones left behind are pruned.
func spoken(rows []session.SessionInfo) []session.SessionInfo {
	return slices.DeleteFunc(rows, func(row session.SessionInfo) bool { return row.MessageCount == 0 && row.Name == nil })
}

func spokenUpdates(update session.SessionListUpdateFunc) session.SessionListUpdateFunc {
	if update == nil {
		return nil
	}
	return func(progress session.SessionListUpdate) {
		progress.Sessions = spoken(progress.Sessions)
		progress.Loaded, progress.Total = len(progress.Sessions), len(progress.Sessions)
		update(progress)
	}
}

func (host *interactiveSessionHost) TrustState() (modes.InteractiveTrustState, error) {
	cwd := host.Session().Manager().GetCWD()
	trust, err := host.args.native.Trust(host.agentDir)
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	entry, err := trust.GetEntry(cwd)
	if err != nil {
		return modes.InteractiveTrustState{}, err
	}
	settings := host.currentInputs().Settings
	return modes.InteractiveTrustState{
		CWD:            cwd,
		ProjectTrusted: settings != nil && settings.IsProjectTrusted(),
		SavedDecision:  entry,
		Options:        config.GetProjectTrustOptions(cwd, false),
	}, nil
}

func (host *interactiveSessionHost) SetProjectTrust(ctx context.Context, updates []config.ProjectTrustUpdate) error {
	trust, err := host.args.native.Trust(host.agentDir)
	if err != nil {
		return err
	}
	if err := trust.SetMany(updates); err != nil {
		return err
	}
	return host.Reload(ctx)
}

func (host *interactiveSessionHost) authStorage() (*config.AuthStorage, error) {
	if storage := host.currentInputs().Auth; storage != nil {
		return storage, nil
	}
	return host.args.native.Auth(host.agentDir)
}

func (host *interactiveSessionHost) authCredentials() (aiauth.CredentialStore, error) {
	if runtimeAuth := host.currentInputs().RuntimeAuth; runtimeAuth != nil {
		return runtimeAuth, nil
	}
	return host.authStorage()
}

func (host *interactiveSessionHost) refreshAuthState(_ context.Context, _ string) error {
	host.usageCache.Clear()
	if host.args.usageCache != nil {
		host.args.usageCache.Clear()
	}
	inputs := host.currentInputs()
	registry := inputs.ModelRegistry
	if registry == nil {
		return nil
	}
	if err := registry.RefreshAuth(); err != nil {
		return err
	}
	// Default-model selection after login belongs to the TUI; the host only
	// refreshes credentials and the current model projection.
	host.Session().RefreshCurrentModelFromRegistry(registry)
	if extensionsRegistry := inputs.Extensions; extensionsRegistry != nil {
		extensionsRegistry.Events().Emit(context.Background(), "orb.accounts.changed", nil)
	}
	return nil
}

func (host *interactiveSessionHost) AuthOptions(ctx context.Context) (modes.InteractiveAuthOptions, error) {
	credentials, err := host.authCredentials()
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	inputs := host.currentInputs()
	return authOptions(ctx, credentials, inputs.ModelRegistry, inputs.RuntimeAuth)
}

// authOptions lists what /login and /logout offer: every provider's sign-in methods with its
// current auth status, and the stored credentials. `orb login --json` lists the same.
func authOptions(ctx context.Context, credentials aiauth.CredentialStore, registry *config.ModelRegistry, runtimeAuth *runtimeCredentials) (modes.InteractiveAuthOptions, error) {
	stored, err := credentials.List(ctx)
	if err != nil {
		return modes.InteractiveAuthOptions{}, err
	}
	storedTypes := make(map[string]aiauth.CredentialType, len(stored))
	for _, credential := range stored {
		storedTypes[credential.ProviderID] = credential.Type
	}

	providerIDs := make([]string, 0)
	seen := make(map[string]struct{})
	appendProvider := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		providerIDs = append(providerIDs, id)
	}
	options := modes.InteractiveAuthOptions{}
	if registry != nil {
		for _, id := range registry.ProviderIDs() {
			appendProvider(id)
		}
	} else {
		for _, provider := range providers.List() {
			appendProvider(string(provider.ID))
		}
	}
	for _, id := range providerIDs {
		name := id
		methods := aiauth.ProviderAuth{}
		var status *modes.InteractiveAuthStatus
		if registry != nil {
			name = registry.ProviderDisplayName(id)
			methods = registry.ProviderAuth(id)
			authStatus := registry.GetProviderAuthStatus(id, nil)
			if authStatus.Configured {
				authType := aiauth.AuthTypeAPIKey
				if registry.IsUsingOAuth(id) {
					authType = aiauth.AuthTypeOAuth
				}
				status = &modes.InteractiveAuthStatus{Type: authType, Source: cmp.Or(authStatus.Label, authStatus.Source)}
			}
		} else if provider, known := providers.Get(ai.ProviderID(id)); known {
			name = provider.Name
			methods = provider.Methods
		}
		if runtimeAuth != nil && runtimeAuth.HasRuntimeAPIKey(id) {
			status = &modes.InteractiveAuthStatus{Type: aiauth.AuthTypeAPIKey, Source: "runtime"}
		}
		if id == claudesessions.Name {
			// Claude signs in only through its subscription; the Claude Code login
			// it already has is the same kind of account, not an API key.
			if status != nil {
				status.Type = aiauth.AuthTypeOAuth
			}
			methods.APIKey = nil
		}
		if status == nil {
			// Registry-less fallback only: with a registry, the stored
			// credential already surfaces as the raw "stored" source above.
			if storedType, exists := storedTypes[id]; exists {
				status = &modes.InteractiveAuthStatus{Type: aiauth.AuthType(storedType), Source: "stored"}
			}
		}
		configured := status != nil
		if methods.OAuth != nil {
			loginLabel := ""
			if labeled, ok := methods.OAuth.(aiauth.OAuthLoginLabel); ok {
				loginLabel = labeled.LoginLabel()
			}
			options.Login = append(options.Login, modes.InteractiveAuthProvider{
				ID: id, Name: name, AuthType: aiauth.AuthTypeOAuth, MethodName: methods.OAuth.Name(),
				LoginLabel: loginLabel, Configured: configured, Status: status, LoginAvailable: true,
			})
		}
		if methods.APIKey != nil {
			_, loginAvailable := methods.APIKey.(aiauth.APIKeyLogin)
			options.Login = append(options.Login, modes.InteractiveAuthProvider{
				ID: id, Name: name, AuthType: aiauth.AuthTypeAPIKey, MethodName: methods.APIKey.Name(),
				Configured: configured, Status: status, LoginAvailable: loginAvailable,
			})
		}
	}
	sort.SliceStable(options.Login, func(left, right int) bool {
		return options.Login[left].Name < options.Login[right].Name
	})
	for _, credential := range stored {
		name := credential.ProviderID
		if registry != nil {
			name = registry.ProviderDisplayName(credential.ProviderID)
		} else if provider, known := providers.Get(ai.ProviderID(credential.ProviderID)); known {
			name = provider.Name
		}
		options.Logout = append(options.Logout, modes.InteractiveAuthProvider{
			ID: credential.ProviderID, Name: name, AuthType: aiauth.AuthType(credential.Type), Configured: true,
			Status: &modes.InteractiveAuthStatus{Type: aiauth.AuthType(credential.Type), Source: "stored credential"},
		})
	}
	sort.SliceStable(options.Logout, func(left, right int) bool { return options.Logout[left].Name < options.Logout[right].Name })
	return options, nil
}

func (host *interactiveSessionHost) loginCredential(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	return loginCredential(ctx, host.currentInputs().ModelRegistry, providerID, authType, interaction)
}

// loginCredential runs one provider's sign-in method and returns the credential to store.
func loginCredential(ctx context.Context, registry *config.ModelRegistry, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	methods := aiauth.ProviderAuth{}
	known := false
	if registry != nil {
		if _, exists := registry.Provider(providerID); exists {
			methods, known = registry.ProviderAuth(providerID), true
		}
	} else if definition, exists := providers.Get(ai.ProviderID(providerID)); exists {
		methods, known = definition.Methods, true
	}
	if !known {
		return nil, fmt.Errorf("provider %q does not support login", providerID)
	}
	switch authType {
	case aiauth.AuthTypeOAuth:
		if methods.OAuth == nil {
			return nil, fmt.Errorf("provider %q does not support OAuth login", providerID)
		}
		return methods.OAuth.Login(ctx, withDeviceID(interaction))
	case aiauth.AuthTypeAPIKey:
		login, ok := methods.APIKey.(aiauth.APIKeyLogin)
		if !ok {
			return nil, fmt.Errorf("provider %q API-key auth is configured outside orb", providerID)
		}
		return login.Login(ctx, interaction)
	default:
		return nil, fmt.Errorf("provider %q has unknown auth type %q", providerID, authType)
	}
}

func (host *interactiveSessionHost) Login(ctx context.Context, providerID string, authType aiauth.AuthType, interaction aiauth.AuthInteraction) error {
	credential, err := host.loginCredential(ctx, providerID, authType, interaction)
	if err != nil {
		return err
	}
	storage, err := host.authStorage()
	if err != nil {
		return err
	}
	_, err = storage.Modify(ctx, providerID, func(*aiauth.Credential) (*aiauth.Credential, error) {
		return credential, nil
	})
	if err != nil {
		return err
	}
	return host.refreshAuthState(ctx, providerID)
}

func (host *interactiveSessionHost) Logout(ctx context.Context, providerID string) error {
	credentials, err := host.authCredentials()
	if err != nil {
		return err
	}
	runtimeAuth := host.currentInputs().RuntimeAuth
	wasRuntime := runtimeAuth != nil && runtimeAuth.HasRuntimeAPIKey(providerID)
	if err := credentials.Delete(ctx, providerID); err != nil {
		return err
	}
	if wasRuntime {
		host.mu.Lock()
		host.args.APIKey = nil
		host.mu.Unlock()
	}
	return host.refreshAuthState(ctx, "")
}

func (host *interactiveSessionHost) accountStore() (*accounts.Store, error) {
	if store := host.currentInputs().Accounts; store != nil {
		return store, nil
	}
	base, err := host.authStorage()
	if err != nil {
		return nil, err
	}
	return host.args.native.Accounts(host.agentDir, base), nil
}

func (host *interactiveSessionHost) ProviderAccounts(ctx context.Context) ([]accounts.Account, error) {
	store, err := host.accountStore()
	if err != nil {
		return nil, err
	}
	rows, err := store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	options, err := host.AuthOptions(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Provider] = true
	}
	claudeLogin := host.claudeAmbientAccount(ctx, rows)
	for _, option := range options.Login {
		if option.ID == claudesessions.Name || option.Status == nil || seen[option.ID] {
			continue
		}
		seen[option.ID] = true
		if option.Status.Source == "runtime" {
			continue
		}
		rows = append(rows, accounts.Account{ID: "ambient", Provider: option.ID, Name: option.Status.Source, Type: aiauth.CredentialType(option.Status.Type), Active: true})
	}
	if claudeLogin != nil {
		rows = append(rows, *claudeLogin)
	}
	if runtime := host.currentInputs().RuntimeAuth; runtime != nil {
		for provider := range seen {
			if runtime.HasRuntimeAPIKey(provider) {
				for i := range rows {
					if rows[i].Provider == provider {
						rows[i].Active = false
					}
				}
				rows = append(rows, accounts.Account{ID: "runtime", Provider: provider, Name: "Command-line API key", Type: aiauth.CredentialAPIKey, Active: true})
			}
		}
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Provider < rows[j].Provider })
	return rows, store.ApplyNames(rows)
}

// ProviderName is a provider's display name, as /login shows it.
func (host *interactiveSessionHost) ProviderName(id string) string {
	registry := host.currentInputs().ModelRegistry
	if registry == nil {
		return ""
	}
	return registry.ProviderDisplayName(id)
}

// claudeAmbientAccount is the user's own Claude Code login as an account row.
// Unlike other providers' ambient sources it stays listed beside added
// accounts, since selecting it is how Claude returns to that login.
func (host *interactiveSessionHost) claudeAmbientAccount(ctx context.Context, rows []accounts.Account) *accounts.Account {
	settings := host.currentInputs().Settings
	if settings == nil || !settings.GetPlugins()[claudesessions.Name] {
		return nil
	}
	label, ok := claudesessions.AmbientAccount(ctx, settings, os.Environ())
	if !ok {
		return nil
	}
	active := !slices.ContainsFunc(rows, func(row accounts.Account) bool { return row.Provider == claudesessions.Name && row.Active })
	return &accounts.Account{ID: "ambient", Provider: claudesessions.Name, Name: label, Type: aiauth.CredentialOAuth, Active: active}
}

// forgetClaudeAccount signs out the Claude directory an account ran with once
// it is removed or replaced; other providers keep nothing outside the store.
func (host *interactiveSessionHost) forgetClaudeAccount(credential *aiauth.Credential) {
	settings := host.currentInputs().Settings
	if settings != nil && credential != nil {
		claudesessions.ForgetAccount(settings, host.agentDir, os.Environ(), credential)
	}
}

func (host *interactiveSessionHost) accountChangeAllowed() error {
	if host.Session().State().IsStreaming {
		return errors.New("wait for the current response before changing accounts")
	}
	return nil
}

func (host *interactiveSessionHost) ChangeAccount(ctx context.Context, provider, id, action, name string) error {
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	store, err := host.accountStore()
	if err != nil {
		return err
	}
	var removed *aiauth.Credential
	switch action {
	case "select":
		if runtimeAuth := host.currentInputs().RuntimeAuth; runtimeAuth != nil && runtimeAuth.HasRuntimeAPIKey(provider) {
			return errors.New("restart without --api-key to switch this provider account")
		}
		if provider == claudesessions.Name && id == "ambient" {
			err = store.Deselect(ctx, provider)
			break
		}
		err = store.Select(ctx, provider, id)
	case "remove":
		if provider == claudesessions.Name {
			removed, _ = store.View(provider, id).Read(ctx, provider)
		}
		err = store.Remove(ctx, provider, id)
	case "rename":
		err = store.Rename(ctx, provider, id, name)
	default:
		return errors.New("unknown account action")
	}
	if err != nil {
		return err
	}
	host.forgetClaudeAccount(removed)
	return host.refreshAuthState(ctx, provider)
}

func (host *interactiveSessionHost) LoginAccount(ctx context.Context, provider string, kind aiauth.AuthType, id, name string, interaction aiauth.AuthInteraction) error {
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	credential, err := host.loginCredential(ctx, provider, kind, interaction)
	if err != nil {
		return err
	}
	if err := host.accountChangeAllowed(); err != nil {
		return err
	}
	store, err := host.accountStore()
	if err != nil {
		return err
	}
	var replaced *aiauth.Credential
	if id == "" {
		_, err = store.Add(ctx, provider, name, credential)
	} else {
		_, err = store.View(provider, id).Modify(ctx, provider, func(previous *aiauth.Credential) (*aiauth.Credential, error) {
			replaced = previous
			return credential, nil
		})
	}
	if err != nil {
		return err
	}
	if provider == claudesessions.Name {
		host.forgetClaudeAccount(replaced)
	}
	return host.refreshAuthState(ctx, provider)
}

func (host *interactiveSessionHost) CachedAccountUsage(provider, id string) (usage.Snapshot, bool) {
	return host.usageCache.Peek(provider + "/" + id)
}
func (host *interactiveSessionHost) AccountUsage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	return host.usageCache.Fetch(ctx, provider+"/"+id, func(ctx context.Context) (usage.Snapshot, error) { return host.fetchAccountUsage(ctx, provider, id) })
}
func (host *interactiveSessionHost) fetchAccountUsage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	store, err := host.accountStore()
	if err != nil {
		return usage.Snapshot{}, err
	}
	if provider == claudesessions.Name {
		settings := host.currentInputs().Settings
		var credential *aiauth.Credential
		if id != "ambient" {
			if credential, err = store.View(provider, id).Read(ctx, provider); err != nil {
				return usage.Snapshot{}, err
			}
		}
		return claudesessions.Usage(ctx, settings, host.agentDir, os.Environ(), credential)
	}
	inputs := host.currentInputs()
	registry, runtime := inputs.ModelRegistry, inputs.RuntimeAuth
	if registry == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	credentials := store.View(provider, id)
	if id == "ambient" || id == "runtime" {
		if runtime != nil {
			credentials = runtime
		} else {
			credentials = aiauth.NewMemoryStore(nil)
		}
	}
	if credentials == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := aiauth.ResolveProviderAuth(ctx, provider, registry.ProviderAuth(provider), credentials, aiauth.EnvironmentContext{}, nil)
	if err != nil {
		return usage.Snapshot{}, err
	}
	if result == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	return (usage.Client{Cache: host.args.usageCache}).Fetch(ctx, provider, result.Auth)
}

func (host *interactiveSessionHost) UsageEnabled() bool {
	settings := host.currentInputs().Settings
	return settings != nil && settings.GetPlugins()["provider-usage"]
}
func (host *interactiveSessionHost) SetUsageEnabled(enabled bool) error {
	settings := host.currentInputs().Settings
	if settings == nil {
		return errors.New("settings are unavailable")
	}
	settings.SetPluginEnabled("provider-usage", enabled)
	if errors := settings.DrainErrors(); len(errors) > 0 {
		return errors[0]
	}
	return nil
}

func (host *interactiveSessionHost) DeleteSession(reference string) (modes.SessionDeleteMethod, error) {
	if host.args.native == nil {
		// File-backed SDK hosts retain the selector's ordinary delete path.
		return modes.SessionDeleteUnlink, os.Remove(reference)
	}
	return modes.SessionDeleteUnlink, host.args.native.DeleteSession(reference)
}
