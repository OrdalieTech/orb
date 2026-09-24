package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/rpc"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/platforms/memory"
)

// The object's path namespace: tools work in Workspace; settings, model
// catalogs and session journals live under AgentDir, as ~/.pi/agent does.
const (
	AgentDir  = "/agent"
	Workspace = "/workspace"
	// DefaultMaxBytes bounds the persisted tree, which the object holds in
	// memory, well inside a 128 MB isolate.
	DefaultMaxBytes = 32 << 20
)

// Storage namespaces within the object's key-value storage.
const (
	filesNamespace     = "fs/"
	documentsNamespace = "doc/"
	// currentSession records the journal a restarted object resumes.
	currentSession = "/worker/session"
)

type Options struct {
	KV KV
	// Env resolves provider credentials (Worker secrets) and ORB_* vars.
	Env Env
	// Settings and Models serve settings.json and models.json until the
	// object stores its own (Worker vars ORB_SETTINGS and ORB_MODELS).
	Settings, Models []byte
	// MaxBytes and MaxFiles bound the persisted tree; zero MaxBytes is
	// DefaultMaxBytes.
	MaxBytes int64
	MaxFiles int
	// Model pins the model instead of restoring it from the session or
	// settings; StreamFn replaces the default provider dispatcher.
	Model    *ai.Model
	StreamFn engine.StreamFn
	// Tools adds tools to every session the object starts.
	Tools ToolsFunc
}

// Instance is one Durable Object's Orb: a Host over the object's storage and
// the session it resumes. It is the RPC session host: new_session and
// switch_session replace the session; fork and clone are not offered.
type Instance struct {
	Host  *host.Host
	Files *FileSystem

	documents *FileSystem
	repo      *harness.JSONLSessionRepo
	model     *ai.Model
	streamFn  engine.StreamFn
	tools     ToolsFunc

	mu           sync.Mutex
	session      *agent.AgentSession
	control      *agent.SessionControl
	observers    map[uint64]func(*agent.AgentSession)
	nextObserver uint64
}

// Open restores the object's files and documents and resumes its current
// session, or its newest, or starts one.
func Open(ctx context.Context, options Options) (*Instance, error) {
	if options.KV == nil {
		return nil, errors.New("worker: a KV storage port is required")
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultMaxBytes
	}
	files, err := OpenFileSystem(ctx, options.KV, filesNamespace, memory.Options{MaxBytes: options.MaxBytes, MaxFiles: options.MaxFiles})
	if err != nil {
		return nil, err
	}
	documents, err := OpenFileSystem(ctx, options.KV, documentsNamespace, memory.Options{})
	if err != nil {
		return nil, err
	}
	if err := files.CreateDir(ctx, Workspace, true); err != nil {
		return nil, err
	}
	defaults := map[string][]byte{}
	if options.Settings != nil {
		defaults[path.Join(AgentDir, "settings.json")] = options.Settings
	}
	if options.Models != nil {
		defaults[path.Join(AgentDir, "models.json")] = options.Models
	}
	instance := &Instance{
		Files: files, documents: documents, model: options.Model, streamFn: options.StreamFn, tools: options.Tools,
		repo: harness.NewJSONLSessionRepo(files, path.Join(AgentDir, "sessions")),
	}
	instance.Host = &host.Host{AgentDir: AgentDir, FS: files, Store: NewStore(documents, defaults), Env: options.Env, Sessions: instance.repo}
	session, err := instance.resume(ctx)
	if err != nil {
		return nil, err
	}
	if instance.session, err = instance.start(ctx, session); err != nil {
		return nil, err
	}
	return instance, nil
}

func (instance *Instance) resume(ctx context.Context) (*harness.Session, error) {
	current, err := instance.documents.ReadTextFile(ctx, currentSession)
	if err == nil && current != "" {
		session, err := instance.repo.OpenPath(ctx, current)
		if err == nil {
			return session, nil
		}
		var missing *harness.SessionError
		if !errors.As(err, &missing) || missing.Code != harness.SessionErrorNotFound {
			return nil, err
		}
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	sessions, err := instance.repo.List(ctx, harness.SessionListOptions{CWD: Workspace})
	if err != nil {
		return nil, err
	}
	if len(sessions) > 0 {
		return instance.repo.Open(ctx, sessions[0])
	}
	return instance.repo.Create(ctx, harness.SessionCreateOptions{CWD: Workspace})
}

// start builds the agent session over a journal and records it as current.
// Resources stay empty: context files, skills and prompts are not yet
// served through the FS port.
func (instance *Instance) start(ctx context.Context, journal *harness.Session) (*agent.AgentSession, error) {
	manager, err := sessionstore.FromHarnessStorage(journal.Storage(), sessionstore.WithHarnessRepo(instance.repo), sessionstore.WithCwdOverride(Workspace))
	if err != nil {
		return nil, err
	}
	options := agent.AgentSessionOptions{
		CWD: Workspace, Host: instance.Host, SessionManager: manager, Model: instance.model, StreamFn: instance.streamFn,
		Resources: &agent.Resources{}, DeferExtensionStart: true,
	}
	if instance.tools != nil {
		if options.Settings, err = instance.settings(); err != nil {
			return nil, err
		}
		options.CustomTools = instance.tools(options.Settings)
	}
	result, err := agent.NewAgentSession(options)
	if err != nil {
		return nil, err
	}
	if err := instance.documents.WriteFile(ctx, currentSession, []byte(manager.GetSessionFile())); err != nil {
		result.Session.Dispose()
		return nil, err
	}
	return result.Session, nil
}

// replaceContext swaps the session inside a control transition, so an attached
// controller sees a new revision, and tells session observers.
func (instance *Instance) replaceContext(ctx context.Context, journal *harness.Session, err error) (bool, error) {
	if err != nil {
		return false, err
	}
	finish, err := instance.beginTransition(ctx)
	if err != nil {
		return false, err
	}
	defer finish()
	next, err := instance.start(ctx, journal)
	if err != nil {
		return false, err
	}
	instance.mu.Lock()
	previous := instance.session
	instance.session = next
	instance.mu.Unlock()
	instance.notify(next)
	if previous != nil {
		previous.Dispose()
	}
	return false, nil
}

func (instance *Instance) Session() *agent.SessionRuntime {
	instance.mu.Lock()
	defer instance.mu.Unlock()
	return instance.session
}

func (instance *Instance) NewSession(parentSession string) (bool, error) {
	return instance.NewSessionContext(context.Background(), parentSession)
}

// NewSessionContext is NewSession under ctx's control target.
func (instance *Instance) NewSessionContext(ctx context.Context, parentSession string) (bool, error) {
	options := harness.SessionCreateOptions{CWD: Workspace}
	if parentSession != "" {
		options.ParentSessionPath = &parentSession
	}
	journal, err := instance.repo.Create(ctx, options)
	return instance.replaceContext(ctx, journal, err)
}

func (instance *Instance) SwitchSession(sessionPath string) (bool, error) {
	return instance.SwitchSessionContext(context.Background(), sessionPath)
}

// SwitchSessionContext is SwitchSession under ctx's control target.
func (instance *Instance) SwitchSessionContext(ctx context.Context, sessionPath string) (bool, error) {
	if !strings.HasPrefix(path.Clean(sessionPath), path.Join(AgentDir, "sessions")+"/") {
		return false, fmt.Errorf("worker: sessions live under %s/sessions", AgentDir)
	}
	journal, err := instance.repo.OpenPath(ctx, sessionPath)
	return instance.replaceContext(ctx, journal, err)
}

func (instance *Instance) Fork(string, bool) (string, bool, error) {
	return "", false, errors.New("worker: fork and clone are not supported by the Worker host")
}

func (instance *Instance) Dispose() {
	instance.mu.Lock()
	session := instance.session
	instance.session = nil
	instance.mu.Unlock()
	if session != nil {
		session.Dispose()
	}
}

// Serve speaks kernel RPC frames (agent/rpc) until input ends or ctx is done,
// then disposes the session.
func (instance *Instance) Serve(ctx context.Context, input io.Reader, output, diagnostics io.Writer) int {
	return rpc.Serve(ctx, instance, rpc.Options{
		Input: input, Output: output, Diagnostics: diagnostics,
		Commands: func() []rpc.SlashCommand { return rpc.SlashCommands(instance.Session()) },
	})
}
