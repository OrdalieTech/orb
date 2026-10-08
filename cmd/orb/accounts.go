package main

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/usage"
)

// accountBook lists the connected provider accounts, reads their plan limits and switches between
// them. The TUI's Providers view and `orb accounts`, which the apps draw, go through it.
type accountBook struct {
	store       *accounts.Store
	credentials aiauth.CredentialStore
	registry    *config.ModelRegistry
	runtime     *runtimeCredentials
	settings    *config.SettingsManager
	agentDir    string
	requests    *usage.Cache
	// session, when set, is the conversation whose Claude turns report the active account's limits.
	session *agent.AgentSession
}

func (b accountBook) list(ctx context.Context) ([]accounts.Account, error) {
	rows, err := b.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	options, err := authOptions(ctx, b.credentials, b.registry, b.runtime)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Provider] = true
	}
	// The user's own Claude Code login stays listed beside added accounts, since selecting it is
	// how Claude returns to that login.
	var claudeLogin *accounts.Account
	if b.settings != nil && b.settings.GetPlugins()[claudesessions.Name] {
		if label, ok := claudesessions.AmbientAccount(ctx, b.settings, os.Environ()); ok {
			active := !slices.ContainsFunc(rows, func(row accounts.Account) bool { return row.Provider == claudesessions.Name && row.Active })
			claudeLogin = &accounts.Account{ID: "ambient", Provider: claudesessions.Name, Name: label, Type: aiauth.CredentialOAuth, Active: active}
		}
	}
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
	if b.runtime != nil {
		for provider := range seen {
			if b.runtime.HasRuntimeAPIKey(provider) {
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
	return rows, b.store.ApplyNames(rows)
}

func (b accountBook) name(provider string) string {
	if b.registry != nil {
		if name := b.registry.ProviderDisplayName(provider); name != provider {
			return name
		}
	}
	if provider == claudesessions.Name {
		return "Claude"
	}
	return provider
}

func (b accountBook) use(ctx context.Context, provider, id string) error {
	if b.runtime != nil && b.runtime.HasRuntimeAPIKey(provider) {
		return errors.New("restart without --api-key to switch this provider account")
	}
	if provider == claudesessions.Name && id == "ambient" {
		return b.store.Deselect(ctx, provider)
	}
	return b.store.Select(ctx, provider, id)
}

func (b accountBook) usage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	if provider == claudesessions.Name {
		var credential *aiauth.Credential
		if id != "ambient" {
			var err error
			if credential, err = b.store.View(provider, id).Read(ctx, provider); err != nil {
				return usage.Snapshot{}, err
			}
		}
		// A Claude conversation's turns already carry its account's limits.
		if b.session != nil && b.session.State().Model != nil && b.session.State().Model.Provider == claudesessions.Name {
			rows, _ := b.store.Accounts(ctx)
			active := slices.IndexFunc(rows, func(row accounts.Account) bool { return row.Provider == provider && row.Active })
			if (active >= 0 && rows[active].ID == id) || (active < 0 && id == "ambient") {
				if limits := claudesessions.Limits(b.session.Manager(), time.Now()); limits != nil && time.Since(limits.CheckedAt) <= 5*time.Minute {
					return *limits, nil
				}
			}
		}
		return claudesessions.Usage(ctx, b.settings, b.agentDir, os.Environ(), credential)
	}
	client := usage.Client{Cache: b.requests}
	if b.registry == nil || !client.Reads(provider) {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	credentials := aiauth.CredentialStore(b.store.View(provider, id))
	if id == "ambient" || id == "runtime" {
		credentials = aiauth.NewMemoryStore(nil)
		if b.runtime != nil {
			credentials = b.runtime
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := aiauth.ResolveProviderAuth(ctx, provider, b.registry.ProviderAuth(provider), credentials, aiauth.EnvironmentContext{}, nil)
	if err != nil {
		return usage.Snapshot{}, err
	}
	if result == nil {
		return usage.Snapshot{}, usage.ErrUnavailable
	}
	return client.Fetch(ctx, provider, result.Auth)
}

// runAccounts is the Providers view for apps that draw their own screens.
//
//	orb accounts --json               one JSON line per connected account, with its plan limits
//	orb accounts use <provider> <id>  makes that account the one the provider uses
func runAccounts(ctx context.Context, args []string, streams cliStreams) int {
	out := aiauth.NewJSONLines(streams.Stdout)
	fail := func(err error) int { out.Emit(map[string]string{"type": "error", "message": err.Error()}); return 1 }
	if !slices.Equal(args, []string{"--json"}) && (len(args) != 3 || args[0] != "use") {
		return fail(errors.New("usage: orb accounts --json | orb accounts use <provider> <id>"))
	}
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return fail(err)
	}
	if _, err = migrateAuthForContext(ctx, agentDir); err != nil {
		return fail(err)
	}
	state := stateFromContext(ctx)
	storage, err := state.Auth(agentDir)
	if err != nil {
		return fail(err)
	}
	cwd, _ := os.Getwd()
	settings, err := state.Settings(cwd, agentDir)
	if err != nil {
		return fail(err)
	}
	registry, err := state.Models(agentDir, storage, true)
	if err != nil {
		return fail(err)
	}
	book := accountBook{store: state.Accounts(agentDir, storage), credentials: storage, registry: registry, settings: settings, agentDir: agentDir}
	if args[0] == "use" {
		if err := book.use(ctx, args[1], args[2]); err != nil {
			return fail(err)
		}
		out.Emit(map[string]string{"type": "done"})
		return 0
	}
	rows, err := book.list(ctx)
	if err != nil {
		return fail(err)
	}
	readings := make([]*usage.Snapshot, len(rows))
	var wg sync.WaitGroup
	limit := make(chan struct{}, 4)
	for i, row := range rows {
		wg.Go(func() {
			limit <- struct{}{}
			defer func() { <-limit }()
			if snapshot, err := book.usage(ctx, row.Provider, row.ID); err == nil {
				readings[i] = &snapshot
			}
		})
	}
	wg.Wait()
	for i, row := range rows {
		line := map[string]any{"provider": row.Provider, "provider_name": book.name(row.Provider), "id": row.ID, "name": row.Name, "auth": row.Type, "active": row.Active}
		if readings[i] != nil {
			line["usage"] = readings[i]
		}
		out.Emit(line)
	}
	return 0
}
