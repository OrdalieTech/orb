package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
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

	mu     sync.Mutex
	inUse  map[string]bool
	cached map[string]*cachedSession
}

// cachedSession keeps a released conversation's SessionManager hot. Re-opening
// re-parsed the whole session JSONL on every inbound message, which dominated
// both time and allocations per turn.
//
// ponytail: unbounded — one live manager per conversation ever acquired. Add
// an idle TTL or LRU when a deployment holds more conversations than memory.
type cachedSession struct {
	manager *sessionstore.SessionManager
	path    string
	modTime time.Time
	size    int64
}

// reuse returns the cached manager when the conversation would resolve to the
// exact bytes it was released with; anything written outside this process
// forces a re-parse.
func (c *cachedSession) reuse(recent string) *sessionstore.SessionManager {
	if c == nil || c.path != recent {
		return nil
	}
	if c.path == "" {
		return c.manager // never flushed: nothing on disk to diverge from
	}
	info, err := os.Stat(c.path)
	if err != nil || !info.ModTime().Equal(c.modTime) || info.Size() != c.size {
		return nil
	}
	return c.manager
}

func snapshotSession(manager *sessionstore.SessionManager) *cachedSession {
	entry := &cachedSession{manager: manager, path: manager.GetSessionFile()}
	if info, err := os.Stat(entry.path); err == nil {
		entry.modTime, entry.size = info.ModTime(), info.Size()
	}
	return entry
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
		cached:   map[string]*cachedSession{},
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
	cached := p.cached[id]
	p.mu.Unlock()
	// keep is nil on every failure path, which drops the cache entry rather
	// than handing a half-wired manager to the next turn.
	release := func(keep *sessionstore.SessionManager) {
		p.mu.Lock()
		delete(p.inUse, id)
		if keep == nil {
			delete(p.cached, id)
		} else {
			p.cached[id] = snapshotSession(keep)
		}
		p.mu.Unlock()
	}

	sessionDir := filepath.Join(p.root, id)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		release(nil)
		return nil, fmt.Errorf("chat: create session dir: %w", err)
	}
	recent := sessionstore.FindMostRecentSession(sessionDir, "")
	manager := cached.reuse(recent)
	if manager == nil {
		var err error
		if recent != "" {
			manager, err = sessionstore.Open(recent, sessionDir)
		} else {
			manager, err = sessionstore.Create(p.root, sessionDir)
		}
		if err != nil {
			release(nil)
			return nil, fmt.Errorf("chat: open conversation session: %w", err)
		}
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
		release(nil)
		return nil, fmt.Errorf("chat: create agent session: %w", err)
	}

	var once sync.Once
	return &Conversation{
		Session: result.Session,
		Manager: manager,
		Close: func(context.Context) error {
			once.Do(func() {
				result.Session.Dispose()
				release(manager)
			})
			return nil
		},
	}, nil
}
