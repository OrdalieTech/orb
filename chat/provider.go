package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/config"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

// Conversation is exclusive ownership of one hydrated agent session.
type Conversation struct {
	Session *codingagent.AgentSession
	// Manager receives ledger writes and serves raw entry reads.
	Manager *sessionstore.SessionManager
	// Close persists and releases the conversation; it must be called
	// exactly once.
	Close func(ctx context.Context) error
}

// SessionProvider hands out exclusive conversation ownership. The local JSONL
// provider is single-process; cluster providers must fence externally.
type SessionProvider interface {
	Acquire(ctx context.Context, key ConversationKey) (*Conversation, error)
}

// LocalProviderOption configures [NewLocalProvider].
type LocalProviderOption func(*LocalProvider)

// WithSessionOptions installs a hook that can adjust the agent session
// options per conversation before construction — the sanctioned way to wire
// tools, models, or stream backends. Without it, sessions are created with
// all tools disabled (NoTools "all").
func WithSessionOptions(hook func(key ConversationKey, o *codingagent.AgentSessionOptions)) LocalProviderOption {
	return func(p *LocalProvider) { p.hook = hook }
}

// WithAgentDir overrides the global agent config directory used for the
// shared model registry and settings. Defaults to ~/.pi/agent.
func WithAgentDir(dir string) LocalProviderOption {
	return func(p *LocalProvider) { p.agentDir = dir }
}

// LocalProvider maps conversation keys to per-conversation session
// directories under a root, resuming the most recent session file for a key
// or creating a new one. One ModelRegistry and one SettingsManager are shared
// across all sessions.
type LocalProvider struct {
	root     string
	agentDir string
	hook     func(ConversationKey, *codingagent.AgentSessionOptions)

	registry *config.ModelRegistry
	settings *config.SettingsManager

	mu    sync.Mutex
	inUse map[string]bool
}

// NewLocalProvider creates a single-process session provider rooted at root.
// Sessions are constructed with tools disabled unless a [WithSessionOptions]
// hook overrides that explicitly.
func NewLocalProvider(root string, opts ...LocalProviderOption) (*LocalProvider, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("chat: resolve provider root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("chat: create provider root: %w", err)
	}
	provider := &LocalProvider{
		root:     absRoot,
		agentDir: codingagent.DefaultAgentDir(),
		inUse:    map[string]bool{},
	}
	for _, opt := range opts {
		opt(provider)
	}
	provider.registry, err = config.NewModelRegistry(provider.agentDir)
	if err != nil {
		return nil, fmt.Errorf("chat: create model registry: %w", err)
	}
	provider.settings, err = config.NewSettingsManager(absRoot, config.WithAgentDir(provider.agentDir))
	if err != nil {
		return nil, fmt.Errorf("chat: create settings manager: %w", err)
	}
	return provider, nil
}

// SessionDir returns the sanitized per-conversation session directory for key.
func (p *LocalProvider) SessionDir(key ConversationKey) string {
	return filepath.Join(p.root, key.String())
}

// Acquire implements [SessionProvider]. It errors when the conversation is
// already held; release goes through the returned Conversation's Close.
func (p *LocalProvider) Acquire(_ context.Context, key ConversationKey) (*Conversation, error) {
	if key.Platform == "" || key.Account == "" || key.ChatID == "" {
		return nil, fmt.Errorf("chat: conversation key requires platform, account, and chat id (got %q)", key.String())
	}
	id := key.String()
	p.mu.Lock()
	if p.inUse[id] {
		p.mu.Unlock()
		return nil, fmt.Errorf("chat: conversation %q is already acquired", id)
	}
	p.inUse[id] = true
	p.mu.Unlock()
	release := func() {
		p.mu.Lock()
		delete(p.inUse, id)
		p.mu.Unlock()
	}

	sessionDir := p.SessionDir(key)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		release()
		return nil, fmt.Errorf("chat: create session dir: %w", err)
	}
	var manager *sessionstore.SessionManager
	var err error
	if recent := sessionstore.FindMostRecentSession(sessionDir, ""); recent != "" {
		manager, err = sessionstore.Open(recent, sessionDir)
	} else {
		manager, err = sessionstore.Create(p.root, sessionDir)
	}
	if err != nil {
		release()
		return nil, fmt.Errorf("chat: open conversation session: %w", err)
	}

	options := codingagent.AgentSessionOptions{
		CWD:            p.root,
		AgentDir:       p.agentDir,
		SessionManager: manager,
		Settings:       p.settings,
		ModelRegistry:  p.registry,
		NoTools:        "all",
	}
	if p.hook != nil {
		p.hook(key, &options)
	}
	result, err := codingagent.NewAgentSession(options)
	if err != nil {
		release()
		return nil, fmt.Errorf("chat: create agent session: %w", err)
	}

	var once sync.Once
	return &Conversation{
		Session: result.Session,
		Manager: manager,
		Close: func(context.Context) error {
			once.Do(func() {
				result.Session.Dispose()
				release()
			})
			return nil
		},
	}, nil
}
