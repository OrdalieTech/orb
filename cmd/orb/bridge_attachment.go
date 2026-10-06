package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent"
	attach "github.com/OrdalieTech/orb/agent/bridge"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/tools"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/document"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
	"github.com/OrdalieTech/orb/plugins/usage"
	"github.com/OrdalieTech/orb/tui"
)

type cliBridgeLink struct {
	mu         sync.Mutex
	connection *protocol.Conn
	configure  func(bool) error
}

func (l *cliBridgeLink) invoke(ctx context.Context, method string, p any, result any) error {
	if l == nil {
		return bridge.Fail("unavailable")
	}
	l.mu.Lock()
	c := l.connection
	l.mu.Unlock()
	if c == nil {
		return bridge.Fail("unavailable")
	}
	return c.Call(ctx, method, p, result)
}
func (l *cliBridgeLink) set(c *protocol.Conn) { l.mu.Lock(); l.connection = c; l.mu.Unlock() }

type attachmentIdentity struct {
	Version    int    `json:"version"`
	PeerID     string `json:"peer_id"`
	InstanceID string `json:"instance_id"`
	Credential string `json:"credential"`
}

func attachEnabledBridge(lifetime context.Context, host attach.Host, args CLIArgs, settings *config.SettingsManager, writer io.Writer) (func(), error) {
	profile := args.BridgeProfile
	if profile == "" {
		if settings == nil || !settings.GetPlugins()["bridge"] {
			return func() {}, nil
		}
		profile = "personal"
	}
	// A Bridge that starts Orb for a peer (host.launch) names it, but it is as ephemeral as an unnamed one.
	alias, throwaway := args.InstanceAlias, args.InstanceAlias == "" || os.Getenv("ORB_BRIDGE_EPHEMERAL") == "1"
	_ = os.Unsetenv("ORB_BRIDGE_EPHEMERAL") // not inherited by what this Orb runs
	if alias == "" {
		alias = "instance-" + strings.ToLower(protocol.NewID()[:8])
	}
	if !nativebridge.ValidName(alias) {
		return nil, errors.New("invalid bridge instance alias")
	}
	dir, err := nativebridge.Dir(profile)
	if err != nil {
		return nil, err
	}
	if err = startBridge(lifetime, profile, false); err != nil {
		_, _ = fmt.Fprintln(writer, "Bridge disconnected:", err)
	}
	stateDir := filepath.Join(filepath.Dir(filepath.Dir(dir)), "instances", profile, alias)
	stateStore, err := args.native.BridgeStore(filepath.Join(stateDir, "attachment.json"), 4096)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = stateStore.Close() }
	raw, err := stateStore.Read(lifetime)
	if err != nil {
		cleanup()
		return nil, err
	}
	var identity attachmentIdentity
	if len(raw) == 0 {
		admin, err := bridgeAdmin(lifetime, profile)
		if err != nil {
			cleanup()
			return nil, err
		}
		var enrolled struct {
			Instance   bridge.Instance `json:"instance"`
			Credential string          `json:"credential"`
		}
		err = admin.Call(lifetime, "enroll", map[string]string{"alias": alias}, &enrolled)
		var status struct {
			PeerID string `json:"peer_id"`
		}
		if err == nil {
			err = admin.Call(lifetime, "status", struct{}{}, &status)
		}
		_ = admin.Close()
		if err != nil {
			cleanup()
			return nil, err
		}
		identity = attachmentIdentity{1, status.PeerID, enrolled.Instance.ID, enrolled.Credential}
		if err = document.Replace(lifetime, stateStore, bridge.JSON(identity)); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		if err = protocol.Decode(raw, &identity); err != nil || identity.Version != 1 || !protocol.ValidID(identity.InstanceID) {
			cleanup()
			return nil, errors.New("invalid instance attachment state")
		}
	}
	ledger, err := args.native.BridgeStore(filepath.Join(stateDir, "operations.json"), protocol.MaxFrame)
	if err != nil {
		cleanup()
		return nil, err
	}
	link := args.bridgeLink
	if link == nil {
		link = &cliBridgeLink{}
	}
	quota := &providerUsage{cache: args.usageCache}
	a, err := attach.Attach(lifetime, host, attach.Options{InstanceID: identity.InstanceID, Store: ledger, Status: func(s *agent.AgentSession) string {
		if model := s.State().Model; model != nil && model.Provider == claudesessions.Name {
			return claudesessions.LimitsStatus(s.Manager(), time.Now())
		}
		return ""
	}, Usage: quota.read, Complete: completeAt, Authorize: func(r bridge.Request) bool {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var allowed bool
		return link.invoke(ctx, "authorize", r, &allowed) == nil && allowed
	}})
	if err != nil {
		_ = ledger.Close()
		cleanup()
		return nil, err
	}
	ctx, cancel := context.WithCancel(lifetime)
	done := make(chan struct{})
	go func() {
		defer close(done)
		delay := 100 * time.Millisecond
		boot := protocol.NewID()
		for {
			if ctx.Err() != nil {
				return
			}
			c, err := nativebridge.Dial(ctx, filepath.Join(dir, "attach.sock"), nativebridge.Auth{Credential: identity.Credential, InstanceID: identity.InstanceID, BootID: boot}, a.Invoke, a.SetGeneration)
			if err == nil {
				link.set(c)
				delay = 100 * time.Millisecond
				select {
				case <-ctx.Done():
					_ = c.Close()
				case <-c.Done():
				}
				link.set(nil)
			}
			timer := time.NewTimer(delay + time.Duration(rand.Int64N(int64(delay/4)+1)))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			// The service is a local socket: a failed dial costs nothing, and a
			// restarted Bridge should see its instances again within seconds.
			delay = min(delay*2, 2*time.Second)
		}
	}()
	return func() {
		cancel()
		<-done
		_ = a.Close()
		// A throwaway instance's alias is never used again: it leaves no registration or state behind.
		if throwaway {
			ctx, stop := context.WithTimeout(context.WithoutCancel(lifetime), 2*time.Second)
			if admin, err := bridgeAdmin(ctx, profile); err == nil {
				_ = admin.Call(ctx, "retire", map[string][]string{"instance_ids": {identity.InstanceID}}, nil)
				_ = admin.Close()
			}
			_ = document.Replace(ctx, ledger, nil)
			_ = document.Replace(ctx, stateStore, nil)
			stop()
		}
		_ = ledger.Close()
		cleanup()
		if throwaway {
			_ = os.RemoveAll(stateDir)
		}
	}, nil
}

// attachCLIBridge keeps this Orb attached to Bridge while the bridge plugin is
// enabled; failures are reported to writer and retried, never returned.
func attachCLIBridge(lifetime context.Context, host attach.Host, args CLIArgs, settings *config.SettingsManager, writer io.Writer) func() {
	link := args.bridgeLink
	if link == nil {
		link = &cliBridgeLink{}
		args.bridgeLink = link
	}
	configured := args
	if configured.BridgeProfile == "" {
		configured.BridgeProfile = "personal"
	}
	retryCtx, cancelRetry := context.WithCancel(lifetime)
	var mu sync.Mutex
	var detach func()
	var retryOnce sync.Once
	closed, desired, initial := false, false, true
	attach := func(explicit bool) error {
		if err := startBridge(retryCtx, configured.BridgeProfile, explicit); err != nil {
			return err
		}
		var err error
		detach, err = attachEnabledBridge(lifetime, host, configured, settings, writer)
		return err
	}
	configure := func(enabled bool) error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return bridge.Fail("unavailable")
		}
		explicit := enabled && !desired && !initial
		initial = false
		desired = enabled
		if !enabled {
			if detach != nil {
				detach()
				detach = nil
			}
			return nil
		}
		retryOnce.Do(func() {
			go func() {
				tick := time.NewTicker(10 * time.Second)
				defer tick.Stop()
				for {
					select {
					case <-retryCtx.Done():
						return
					case <-tick.C:
						mu.Lock()
						if !closed && desired && detach == nil {
							_ = attach(false)
						}
						mu.Unlock()
					}
				}
			}()
		})
		if detach != nil {
			return nil
		}
		return attach(explicit)
	}
	link.mu.Lock()
	link.configure = configure
	link.mu.Unlock()
	enabled := args.BridgeProfile != "" || settings != nil && settings.GetPlugins()["bridge"]
	if err := configure(enabled); err != nil {
		_, _ = fmt.Fprintln(writer, "Bridge disconnected:", err)
	}
	return func() {
		cancelRetry()
		mu.Lock()
		closed = true
		if detach != nil {
			detach()
		}
		mu.Unlock()
	}
}
func (l *cliBridgeLink) configureBridge(enabled bool) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	configure := l.configure
	l.mu.Unlock()
	if configure == nil {
		return nil
	}
	return configure(enabled)
}

// providerUsage reads the plan limits of the session's provider without ever waiting, as the
// Bridge pulse asks often: Claude's arrive with its turns, so they are read again only when the
// conversation moves; the others are read behind it, at most once a minute, through the process's
// quota cache (shared with the TUI footer, cleared when accounts change).
type providerUsage struct {
	cache          *usage.Cache
	mu             sync.Mutex
	provider, leaf string
	reading        *usage.Snapshot
	asked          time.Time
}

func (u *providerUsage) read(s *agent.AgentSession) *usage.Snapshot {
	model := s.State().Model
	if model == nil {
		return nil
	}
	provider, leaf := string(model.Provider), ""
	if id := s.Manager().GetLeafID(); id != nil {
		leaf = *id
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if provider != u.provider {
		u.provider, u.leaf, u.reading, u.asked = provider, "", nil, time.Time{}
	}
	switch {
	case provider == claudesessions.Name && leaf != u.leaf:
		u.leaf, u.reading = leaf, claudesessions.Limits(s.Manager(), time.Now())
	case provider != claudesessions.Name && time.Since(u.asked) >= time.Minute:
		u.asked = time.Now()
		go u.refresh(s, provider)
	}
	return u.reading
}

func (u *providerUsage) refresh(s *agent.AgentSession, provider string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	runner := s.ExtensionRunner()
	if runner == nil || runner.ModelRegistry() == nil {
		return
	}
	resolved, err := runner.ModelRegistry().ResolveProviderAuth(ctx, provider, nil)
	if err != nil || resolved == nil {
		return
	}
	reading, err := usage.Client{Cache: u.cache}.Fetch(ctx, provider, resolved.Auth)
	u.mu.Lock()
	defer u.mu.Unlock()
	if err == nil && provider == u.provider {
		u.reading = &reading
	}
}

// completeAt is the TUI's `@` completion for a Bridge client: the session's skills, which insert
// their `/skill:name` token, then the files in its folder. A path query is files only.
func completeAt(ctx context.Context, s *agent.AgentSession, query string) []attach.Completion {
	var items []attach.Completion
	if !strings.ContainsAny(query, `/\"`) {
		var skills []agent.SlashCommandInfo
		for _, command := range s.Commands() {
			if command.Source == agent.SlashCommandSkill {
				skills = append(skills, command)
			}
		}
		for _, skill := range tui.FuzzyFilter(skills, query, func(c agent.SlashCommandInfo) string { return strings.TrimPrefix(c.Name, "skill:") }) {
			items = append(items, attach.Completion{Text: "/" + skill.Name, Label: strings.TrimPrefix(skill.Name, "skill:"), Detail: skill.Description})
		}
	}
	var files *tui.AutocompleteSuggestions
	at := ""
	if fd := tools.ManagedFDPath(); fd != "" {
		files = tui.NewCombinedAutocompleteProvider(nil, s.Manager().GetCWD(), fd).GetSuggestions(ctx, []string{"@" + query}, 0, utf8.RuneCountInString(query)+1, false)
	} else {
		// Without fd (a phone cannot run one), the folder lists as the TUI's Tab completion lists it.
		files, at = tui.NewCombinedAutocompleteProvider(nil, s.Manager().GetCWD(), "").GetSuggestions(ctx, []string{query}, 0, utf8.RuneCountInString(query), true), "@"
	}
	if files != nil {
		for _, file := range files.Items {
			items = append(items, attach.Completion{Text: at + file.Value, Label: file.Label, Detail: file.Description})
		}
	}
	return items
}
