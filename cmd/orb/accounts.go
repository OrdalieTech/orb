package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
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

// hostAccounts gives plugins the interactive host's accounts once it exists; elsewhere (print,
// RPC, Bridge-launched Orbs) there is no one to ask which account to switch to.
type hostAccounts struct {
	host atomic.Pointer[interactiveSessionHost]
}

func (h *hostAccounts) List(ctx context.Context) ([]accounts.Account, error) {
	if host := h.host.Load(); host != nil {
		return host.ProviderAccounts(ctx)
	}
	return nil, usage.ErrUnavailable
}

func (h *hostAccounts) Usage(ctx context.Context, provider, id string) (usage.Snapshot, error) {
	if host := h.host.Load(); host != nil {
		return host.AccountUsage(ctx, provider, id)
	}
	return usage.Snapshot{}, usage.ErrUnavailable
}

func (h *hostAccounts) Use(ctx context.Context, provider, id string) error {
	if host := h.host.Load(); host != nil {
		return host.ChangeAccount(ctx, provider, id, "select", "")
	}
	return usage.ErrUnavailable
}

// openBook opens this machine's accounts, credentials, models and settings, for the commands the
// apps run.
func openBook(ctx context.Context) (accountBook, error) {
	agentDir, err := config.GetAgentDir()
	if err != nil {
		return accountBook{}, err
	}
	if _, err = migrateAuthForContext(ctx, agentDir); err != nil {
		return accountBook{}, err
	}
	state := stateFromContext(ctx)
	storage, err := state.Auth(agentDir)
	if err != nil {
		return accountBook{}, err
	}
	cwd, _ := os.Getwd()
	settings, err := state.Settings(cwd, agentDir)
	if err != nil {
		return accountBook{}, err
	}
	store := state.Accounts(agentDir, storage)
	registry, err := state.Models(agentDir, store, true)
	if err != nil {
		return accountBook{}, err
	}
	if settings.GetPlugins()[claudesessions.Name] {
		ext := extensions.NewRegistry(cwd)
		_ = ext.Register(claudesessions.Name, claudesessions.Management(settings, agentDir, os.Environ()))
		ext.BindModelRegistry(registry, nil)
	}
	return accountBook{store: store, credentials: storage, registry: registry, settings: settings, agentDir: agentDir}, nil
}

// runModels is the model choice for apps that draw their own screens, before any session runs.
//
//	orb models --json                       one JSON line per model a new session can use: its
//	                                        reasoning levels, the one it starts at, the default marked
//	orb models use <provider> <id> [level]  makes that model, at that level, the one new sessions start on
func runModels(ctx context.Context, args []string, streams cliStreams) int {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, _ = fmt.Fprintln(streams.Stdout, "Usage: orb models --json | orb models use <provider> <id> [level]")
		return 0
	}
	out := aiauth.NewJSONLines(streams.Stdout)
	fail := func(err error) int { out.Emit(map[string]string{"type": "error", "message": err.Error()}); return 1 }
	if !slices.Equal(args, []string{"--json"}) && (len(args) < 3 || len(args) > 4 || args[0] != "use") {
		return fail(errors.New("usage: orb models --json | orb models use <provider> <id> [level]"))
	}
	book, err := openBook(ctx)
	if err != nil {
		return fail(err)
	}
	if args[0] == "use" {
		m, ok := book.registry.Find(args[1], args[2])
		if !ok || !book.registry.HasConfiguredAuth(args[1], nil) {
			return fail(errors.New("no signed-in model " + args[1] + "/" + args[2]))
		}
		var level ai.ModelThinkingLevel
		if len(args) == 4 {
			if level = ai.ModelThinkingLevel(args[3]); !slices.Contains(ai.SupportedThinkingLevels(&m), level) {
				return fail(errors.New(args[1] + "/" + args[2] + " has no reasoning level " + args[3]))
			}
		}
		book.settings.SetDefaultModelAndProvider(args[1], args[2])
		switch {
		case level == "":
		case book.settings.GetModelThinkingLevel(args[1], args[2]) != "":
			// A level kept for the model itself wins over the default when a session starts.
			book.settings.SetModelThinkingLevel(args[1], args[2], level)
		default:
			book.settings.SetDefaultThinkingLevel(level)
		}
		if errs := book.settings.DrainErrors(); len(errs) > 0 {
			return fail(errs[0].Err)
		}
		out.Emit(map[string]string{"type": "done"})
		return 0
	}
	def := agent.DefaultModel(book.registry, book.settings)
	for _, m := range book.registry.Available(nil) {
		// As a new session picks it: the model's own level, else the default, else medium.
		level := cmp.Or(book.settings.GetModelThinkingLevel(string(m.Provider), m.ID), book.settings.GetDefaultThinkingLevel(), ai.ModelThinkingMedium)
		out.Emit(map[string]any{"model": string(m.Provider) + "/" + m.ID, "default": def != nil && def.Provider == m.Provider && def.ID == m.ID,
			"levels": ai.SupportedThinkingLevels(&m), "thinking": ai.ClampThinkingLevel(&m, level)})
	}
	return 0
}

// runAccounts is the Providers view for apps that draw their own screens.
//
//	orb accounts --json               one JSON line per connected account, with its plan limits
//	orb accounts use <provider> <id>  makes that account the one the provider uses
func runAccounts(ctx context.Context, args []string, streams cliStreams) int {
	if slices.Contains(args, "--help") || slices.Contains(args, "-h") {
		_, _ = fmt.Fprintln(streams.Stdout, "Usage: orb accounts --json | orb accounts use <provider> <id>")
		return 0
	}
	out := aiauth.NewJSONLines(streams.Stdout)
	fail := func(err error) int { out.Emit(map[string]string{"type": "error", "message": err.Error()}); return 1 }
	if !slices.Equal(args, []string{"--json"}) && (len(args) != 3 || args[0] != "use") {
		return fail(errors.New("usage: orb accounts --json | orb accounts use <provider> <id>"))
	}
	book, err := openBook(ctx)
	if err != nil {
		return fail(err)
	}
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
