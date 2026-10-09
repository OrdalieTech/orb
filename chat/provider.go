package chat

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
)

// Conversation is exclusive ownership of one hydrated agent session.
type Conversation struct {
	Reset   func() error
	Session *agent.AgentSession
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
func WithSessionOptions(hook func(key ConversationKey, o *agent.AgentSessionOptions)) LocalProviderOption {
	return func(p *LocalProvider) { p.hook = hook }
}

// Open builds the agent session of the conversation key on its manager; close
// disposes it.
type Open func(ctx context.Context, key ConversationKey, manager *sessionstore.SessionManager) (session *agent.AgentSession, close func(), err error)

// WithWorkspace runs conversations as full agents working in cwd, each session
// built by open: the way to give an isolated deployment its tools. Without it
// sessions have no tools (D27).
func WithWorkspace(cwd string, open Open) LocalProviderOption {
	return func(p *LocalProvider) { p.cwd, p.open = cwd, open }
}

// WithAgentDir overrides the global agent config directory used for the
// shared model registry and settings. Defaults to ~/.orb/agent.
func WithAgentDir(dir string) LocalProviderOption {
	return func(p *LocalProvider) { p.agentDir = dir }
}

// WithPersistence selects explicit native session and configuration repositories.
func WithPersistence(repo func(ConversationKey) harness.SessionRepo, settings *config.SettingsManager, registry *config.ModelRegistry) LocalProviderOption {
	return func(p *LocalProvider) { p.repo = repo; p.settings = settings; p.registry = registry }
}

// LocalProvider maps conversation keys to per-conversation session
// directories under a root, resuming the most recent session file for a key
// or creating a new one. One ModelRegistry and one SettingsManager are shared
// across all sessions.
type LocalProvider struct {
	repo     func(ConversationKey) harness.SessionRepo
	root     string
	cwd      string
	open     Open
	agentDir string
	hook     func(ConversationKey, *agent.AgentSessionOptions)

	registry *config.ModelRegistry
	settings *config.SettingsManager

	mu     sync.Mutex
	inUse  map[string]bool
	cached map[string]*cachedSession
}

// cachedSession keeps a released conversation's SessionManager hot. Re-opening
// re-parsed the whole session (JSONL file or native journal) on every inbound
// message, which dominated both time and allocations per turn.
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
		agentDir: agent.DefaultAgentDir(),
		inUse:    map[string]bool{},
		cached:   map[string]*cachedSession{},
	}
	for _, opt := range opts {
		opt(provider)
	}
	if provider.registry == nil {
		provider.registry, err = config.NewModelRegistry(provider.agentDir)
	}
	if err != nil {
		return nil, fmt.Errorf("chat: create model registry: %w", err)
	}
	if provider.settings == nil {
		provider.settings, err = config.NewSettingsManager(absRoot, config.WithAgentDir(provider.agentDir))
	}
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
func (p *LocalProvider) Acquire(ctx context.Context, key ConversationKey) (*Conversation, error) {
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

	var manager *sessionstore.SessionManager
	if p.repo != nil && cached != nil {
		// The local provider is single-process (D27), and a write from a stale
		// handle fails on the journal's revision fence instead of diverging.
		manager = cached.manager
	} else if p.repo != nil {
		repo := p.repo(key)
		entries, err := repo.List(ctx, harness.SessionListOptions{})
		var stored *harness.Session
		if err == nil {
			if len(entries) > 0 {
				stored, err = repo.Open(ctx, entries[0])
			} else {
				stored, err = repo.Create(ctx, harness.SessionCreateOptions{CWD: p.workspace()})
			}
		}
		if err == nil {
			manager, err = sessionstore.FromHarnessStorage(stored.Storage(), sessionstore.WithHarnessRepo(repo))
		}
		if err != nil {
			release(nil)
			return nil, err
		}
	} else {
		sessionDir := filepath.Join(p.root, id)
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			release(nil)
			return nil, fmt.Errorf("chat: create session dir: %w", err)
		}
		recent := sessionstore.FindMostRecentSession(sessionDir, "")
		manager = cached.reuse(recent)
		if manager == nil {
			var err error
			if recent != "" {
				manager, err = sessionstore.Open(recent, sessionDir)
			} else {
				manager, err = sessionstore.Create(p.workspace(), sessionDir)
			}
			if err != nil {
				release(nil)
				return nil, fmt.Errorf("chat: open conversation session: %w", err)
			}
		}

	}

	options := agent.AgentSessionOptions{
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
	session, dispose, err := p.session(ctx, key, options)
	if err != nil {
		release(nil)
		return nil, fmt.Errorf("chat: create agent session: %w", err)
	}

	var once sync.Once
	conversation := &Conversation{
		Session: session,
		Manager: manager,
		Close: func(context.Context) error {
			once.Do(func() {
				dispose()
				release(manager)
			})
			return nil
		},
	}
	if p.repo != nil {
		conversation.Reset = func() error {
			staged, err := sessionstore.InMemory(p.root)
			if err != nil {
				return err
			}
			for _, marker := range carryableMarkers(conversation.Manager) {
				if _, err = appendTurnMarker(staged, marker); err != nil {
					return err
				}
			}
			data, err := staged.JSONL()
			if err != nil {
				return err
			}
			repo := p.repo(key)
			importer, ok := repo.(interface {
				Import(context.Context, []byte) (harness.SessionMetadata, error)
			})
			if !ok {
				return fmt.Errorf("chat: native reset requires atomic journal import")
			}
			metadata, err := importer.Import(context.Background(), data)
			if err != nil {
				return err
			}
			stored, err := repo.Open(context.Background(), metadata)
			if err != nil {
				return err
			}
			replacement, err := sessionstore.FromHarnessStorage(stored.Storage(), sessionstore.WithHarnessRepo(repo))
			if err != nil {
				return err
			}
			options.SessionManager = replacement
			next, closeNext, err := p.session(context.Background(), key, options)
			if err != nil {
				return err
			}
			dispose()
			session, dispose, manager = next, closeNext, replacement
			conversation.Session, conversation.Manager = next, replacement
			return nil
		}
	}
	return conversation, nil
}

func (p *LocalProvider) workspace() string {
	if p.cwd != "" {
		return p.cwd
	}
	return p.root
}

// session builds a conversation's agent: through the workspace's Open, or as a
// tool-less SDK session.
func (p *LocalProvider) session(ctx context.Context, key ConversationKey, options agent.AgentSessionOptions) (*agent.AgentSession, func(), error) {
	if p.open != nil {
		return p.open(ctx, key, options.SessionManager)
	}
	result, err := agent.NewAgentSession(options)
	if err != nil {
		return nil, nil, err
	}
	return result.Session, result.Session.Dispose, nil
}
