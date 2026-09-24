package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	attach "github.com/OrdalieTech/orb/agent/bridge"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	nativebridge "github.com/OrdalieTech/orb/platforms/native/bridge"
	"github.com/OrdalieTech/orb/plugins/claudesessions"
)

type bridgeInteractiveHost struct{ *interactiveSessionHost }

func (h bridgeInteractiveHost) EnableControl() (*agent.SessionControl, error) {
	h.mu.Lock()
	existing := h.bridgeControl
	h.mu.Unlock()
	if existing != nil {
		return existing, nil
	}
	c, err := agent.NewSessionControl(h.Session)
	if err != nil {
		return nil, err
	}
	h.mu.Lock()
	h.bridgeControl = c
	h.mu.Unlock()
	return c, nil
}
func (h bridgeInteractiveHost) ObserveSessions(f func(*agent.AgentSession)) func() {
	h.mu.Lock()
	h.bridgeNextObserver++
	id := h.bridgeNextObserver
	if h.bridgeObservers == nil {
		h.bridgeObservers = map[uint64]func(*agent.AgentSession){}
	}
	h.bridgeObservers[id] = f
	s := h.session
	h.mu.Unlock()
	f(s)
	return func() { h.mu.Lock(); delete(h.bridgeObservers, id); h.mu.Unlock() }
}
func (h bridgeInteractiveHost) SwitchSession(ctx context.Context, path string, opts *agent.AgentSessionRuntimeSwitchOptions) (extensions.SessionReplacementResult, error) {
	cwd := ""
	var with func(context.Context, extensions.ReplacedSessionContext) error
	if opts != nil {
		cwd = opts.CWDOverride
		with = opts.WithSession
	}
	return h.interactiveSessionHost.SwitchSession(ctx, path, cwd, &extensions.SwitchSessionOptions{WithSession: with})
}
func (h bridgeInteractiveHost) Fork(ctx context.Context, id string, opts *extensions.ForkOptions) (agent.AgentSessionRuntimeForkResult, error) {
	r, err := h.interactiveSessionHost.Fork(ctx, id, opts)
	return agent.AgentSessionRuntimeForkResult{Cancelled: r.Cancelled, SelectedText: &r.SelectedText}, err
}

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
	alias := args.InstanceAlias
	if alias == "" {
		alias = "instance-" + strings.ToLower(protocol.NewID()[:8])
	}
	if !validBridgeName(alias) {
		return nil, errors.New("invalid bridge instance alias")
	}
	dir, err := bridgeDir(profile)
	if err != nil {
		return nil, err
	}
	if err = startBridge(lifetime, profile, false); err != nil {
		_, _ = fmt.Fprintln(writer, "Bridge disconnected:", err)
	}
	stateDir := filepath.Join(filepath.Dir(filepath.Dir(dir)), "instances", profile, alias)
	stateStore, err := args.native.bridgeStore(filepath.Join(stateDir, "attachment.json"), 4096)
	if err != nil {
		return nil, err
	}
	cleanup := func() { _ = stateStore.Close() }
	raw, err := stateStore.Load()
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
		if err = stateStore.Save(bridge.JSON(identity)); err != nil {
			cleanup()
			return nil, err
		}
	} else {
		if err = protocol.Decode(raw, &identity); err != nil || identity.Version != 1 || !protocol.ValidID(identity.InstanceID) {
			cleanup()
			return nil, errors.New("invalid instance attachment state")
		}
	}
	ledger, err := args.native.bridgeStore(filepath.Join(stateDir, "operations.json"), protocol.MaxFrame)
	if err != nil {
		cleanup()
		return nil, err
	}
	link := args.bridgeLink
	if link == nil {
		link = &cliBridgeLink{}
	}
	a, err := attach.Attach(lifetime, host, attach.Options{InstanceID: identity.InstanceID, Store: ledger, Status: func(s *agent.AgentSession) string {
		if model := s.State().Model; model != nil && model.Provider == claudesessions.Name {
			return claudesessions.LimitsStatus(s.Manager(), time.Now())
		}
		return ""
	}, Authorize: func(r bridge.Request) bool {
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
			delay = min(delay*2, 10*time.Second)
		}
	}()
	return func() { cancel(); <-done; _ = a.Close(); _ = ledger.Close(); cleanup() }, nil
}

func attachCLIBridge(lifetime context.Context, host attach.Host, args CLIArgs, settings *config.SettingsManager, writer io.Writer) (func(), error) {
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
	}, nil
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
