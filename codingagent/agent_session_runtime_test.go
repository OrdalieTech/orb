package codingagent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
	"github.com/OrdalieTech/pi-go/internal/jsonwire"
)

type agentSessionRuntimeEventLog struct {
	mu     sync.Mutex
	events []string
}

type agentSessionRuntimeTrustContext struct{ cwd string }

func (contextValue agentSessionRuntimeTrustContext) CWD() string { return contextValue.cwd }
func (agentSessionRuntimeTrustContext) Mode() extensions.Mode    { return extensions.ModePrint }
func (agentSessionRuntimeTrustContext) HasUI() bool              { return false }
func (agentSessionRuntimeTrustContext) UI() extensions.TrustUI   { return extensions.NewNoopUI() }

func TestNewAgentSessionRuntimeRejectsMissingImplicitSessionCWD(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: missing, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if host != nil {
		host.Dispose(context.Background())
		t.Fatal("runtime was created for a missing cwd")
	}
	var failure *MissingSessionCWDError
	if !errors.As(err, &failure) || failure.SessionCWD != missing {
		t.Fatalf("missing cwd error = %#v, %v", failure, err)
	}
}

func TestNewAgentSessionRuntimeAllowsMissingInMemorySessionCWD(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing")
	manager, err := sessionstore.InMemory(missing)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: missing, AgentDir: t.TempDir(), SessionManager: manager,
		StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	host.Dispose(context.Background())
}

func TestAgentSessionRuntimeRejectsHarnessBackedReplacement(t *testing.T) {
	cwd := t.TempDir()
	storage, err := harness.NewInMemorySessionStorage(nil, harness.SessionMetadata{
		ID: "harness-runtime", CreatedAt: "2026-02-03T04:05:06.789Z", CWD: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), SessionManager: manager,
		StreamFn: provider.StreamSimple, Model: provider.GetModel(), Resources: &Resources{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })

	_, err = host.NewSession(context.Background(), nil)
	if !errors.Is(err, sessionstore.ErrHarnessStorageReplacement) {
		t.Fatalf("new-session error = %v", err)
	}
	if host.Session().Manager() != manager {
		t.Fatal("failed replacement detached the active harness-backed manager")
	}
}

func (log *agentSessionRuntimeEventLog) add(event string) {
	log.mu.Lock()
	log.events = append(log.events, event)
	log.mu.Unlock()
}

func (log *agentSessionRuntimeEventLog) reset() {
	log.mu.Lock()
	log.events = nil
	log.mu.Unlock()
}

func (log *agentSessionRuntimeEventLog) snapshot() []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.events...)
}

func TestAgentSessionRuntimeNewAndSwitchLifecycle(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "one", 10)})
	log := &agentSessionRuntimeEventLog{}
	registry := runtimeLifecycleRegistry(t, cwd, log, false)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"start:startup:"}) {
		t.Fatalf("startup events = %#v", got)
	}
	log.reset()
	if err := host.Session().PromptSync(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	original := host.Session().Manager().GetSessionFile()
	if original == "" {
		t.Fatal("initial prompt did not persist the session")
	}
	host.SetRebindSession(func(session *AgentSession) error {
		log.add("rebind")
		return session.BindExtensions(context.Background())
	})
	result, err := host.NewSession(context.Background(), &extensions.NewSessionOptions{
		WithSession: func(_ context.Context, replaced extensions.ReplacedSessionContext) error {
			if replaced == nil {
				t.Fatal("replacement context is nil")
			}
			log.add("with")
			return nil
		},
	})
	if err != nil || result.Cancelled {
		t.Fatalf("new session = %#v, %v", result, err)
	}
	second := host.Session().Manager().GetSessionFile()
	want := []string{
		"before:new:",
		"shutdown:new:" + second,
		"rebind",
		"start:new:" + original,
		"with",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("new lifecycle = %#v, want %#v", got, want)
	}
	log.reset()
	result, err = host.SwitchSession(context.Background(), original, &AgentSessionRuntimeSwitchOptions{
		WithSession: func(_ context.Context, replaced extensions.ReplacedSessionContext) error {
			if replaced == nil {
				t.Fatal("replacement context is nil")
			}
			log.add("with")
			return nil
		},
	})
	if err != nil || result.Cancelled {
		t.Fatalf("switch session = %#v, %v", result, err)
	}
	want = []string{
		"before:resume:" + original,
		"shutdown:resume:" + original,
		"rebind",
		"start:resume:" + second,
		"with",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("resume lifecycle = %#v, want %#v", got, want)
	}
}

func TestAgentSessionRuntimeHonorsSwitchCancellation(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	log := &agentSessionRuntimeEventLog{}
	registry := runtimeLifecycleRegistry(t, cwd, log, true)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	original := host.Session()
	log.reset()
	result, err := host.NewSession(context.Background(), nil)
	if err != nil || !result.Cancelled {
		t.Fatalf("new cancellation = %#v, %v", result, err)
	}
	if host.Session() != original {
		t.Fatal("cancelled replacement changed the active session")
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, []string{"before:new:"}) {
		t.Fatalf("cancel events = %#v", got)
	}
}

func TestAgentSessionRuntimeForkInMemory(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "one", 10)})
	log := &agentSessionRuntimeEventLog{}
	registry := runtimeLifecycleRegistry(t, cwd, log, false)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), SessionManager: manager, ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.SetRebindSession(func(session *AgentSession) error {
		return session.BindExtensions(context.Background())
	})
	if err := host.Session().PromptSync(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	users := host.Session().GetUserMessagesForForking()
	if len(users) != 1 {
		t.Fatalf("forkable messages = %#v", users)
	}
	log.reset()
	result, err := host.Fork(context.Background(), users[0].EntryID, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("fork = %#v, %v", result, err)
	}
	if result.SelectedText == nil || *result.SelectedText != "hello" {
		t.Fatalf("selected text = %#v", result.SelectedText)
	}
	want := []string{
		"before-fork:" + users[0].EntryID + ":before",
		"shutdown:fork:",
		"start:fork:",
	}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("fork lifecycle = %#v, want %#v", got, want)
	}
	if got := host.Session().Manager().BuildSessionContext().Messages; len(got) != 0 {
		t.Fatalf("fork-before-root retained %d messages", len(got))
	}
}

func TestAgentSessionRuntimeImportFromJSONL(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "one", 10)})
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().PromptSync(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	original := host.Session().Manager().GetSessionFile()
	contents, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	inputDir := t.TempDir()
	input := filepath.Join(inputDir, "imported.jsonl")
	if err := os.WriteFile(input, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := host.NewSession(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	result, err := host.ImportFromJSONL(context.Background(), input, "")
	if err != nil || result.Cancelled {
		t.Fatalf("import = %#v, %v", result, err)
	}
	wantPath := filepath.Join(host.Session().Manager().GetSessionDir(), filepath.Base(input))
	if got := host.Session().Manager().GetSessionFile(); got != wantPath {
		t.Fatalf("imported path = %q, want %q", got, wantPath)
	}
	if info, statErr := os.Stat(wantPath); statErr != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("imported mode = %v, %v", info, statErr)
	}
	if got := host.Session().Manager().BuildSessionContext().Messages; len(got) != 2 {
		t.Fatalf("imported messages = %d, want 2", len(got))
	}
	_, err = host.ImportFromJSONL(context.Background(), filepath.Join(inputDir, "missing.jsonl"), "")
	var missing *SessionImportFileNotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("missing import error = %T %v", err, err)
	}
}

func TestAgentSessionRuntimeReplacedContextTargetsFreshSession(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "replacement reply", 10)})
	registry := extensions.NewRegistry(cwd)
	instances := 0
	if err := registry.Register("<runtime-fresh>", func(extensions.API) error {
		instances++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	oldSession := host.Session()
	oldContext := oldSession.ExtensionRunner().CreateCommandContext()
	oldFile := oldSession.Manager().GetSessionFile()
	host.SetRebindSession(func(session *AgentSession) error {
		return session.BindExtensions(context.Background())
	})

	result, err := host.NewSession(context.Background(), &extensions.NewSessionOptions{
		WithSession: func(ctx context.Context, replaced extensions.ReplacedSessionContext) error {
			if replaced.SessionManager().GetSessionFile() == oldFile {
				t.Fatal("replacement context retained the old session")
			}
			assertRuntimeContextStale(t, func() { _ = oldContext.CWD() })
			return replaced.SendUserMessage(ctx, ai.NewUserText("hello from replacement"), nil)
		},
	})
	if err != nil || result.Cancelled {
		t.Fatalf("new session = %#v, %v", result, err)
	}
	if err := host.Session().WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if instances != 2 {
		t.Fatalf("extension instances = %d, want 2", instances)
	}
	messages := host.Session().Manager().BuildSessionContext().Messages
	if len(messages) != 2 {
		t.Fatalf("replacement messages = %d, want 2", len(messages))
	}
	if role, text := jsonwire.MessageRoleAndText(messages[0]); role != "user" || text != "hello from replacement" {
		t.Fatalf("replacement user = %s:%q", role, text)
	}
	if role, text := jsonwire.MessageRoleAndText(messages[1]); role != "assistant" || text != "replacement reply" {
		t.Fatalf("replacement assistant = %s:%q", role, text)
	}
}

func TestAgentSessionRuntimeSetupPrecedesRebindAndStart(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	log := &agentSessionRuntimeEventLog{}
	registry := extensions.NewRegistry(cwd)
	if err := registry.Register("<runtime-setup>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, extCtx extensions.Context) (any, error) {
			log.add(fmt.Sprintf("start:%d", len(extCtx.SessionManager().BuildSessionContext().Messages)))
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	log.reset()
	host.SetRebindSession(func(session *AgentSession) error {
		log.add("rebind")
		return session.BindExtensions(context.Background())
	})
	_, err = host.NewSession(context.Background(), &extensions.NewSessionOptions{
		Setup: func(manager *sessionstore.SessionManager) error {
			log.add("setup")
			_, appendErr := manager.AppendMessage(map[string]any{
				"role": "user", "content": "seeded", "timestamp": int64(1),
			})
			return appendErr
		},
		WithSession: func(context.Context, extensions.ReplacedSessionContext) error {
			log.add("with")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"setup", "rebind", "start:1", "with"}
	if got := log.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("setup lifecycle = %#v, want %#v", got, want)
	}
}

func TestAgentSessionRuntimeSetupFailureSkipsRebind(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	rebound := false
	host.SetRebindSession(func(*AgentSession) error {
		rebound = true
		return nil
	})
	want := errors.New("setup failed")
	_, err = host.NewSession(context.Background(), &extensions.NewSessionOptions{
		Setup: func(*sessionstore.SessionManager) error { return want },
	})
	if !errors.Is(err, want) {
		t.Fatalf("setup error = %v", err)
	}
	if rebound {
		t.Fatal("setup failure invoked rebind")
	}
}

func TestAgentSessionRuntimeReloadCreatesFreshExtensionContext(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	log := &agentSessionRuntimeEventLog{}
	registry := extensions.NewRegistry(cwd)
	instances := 0
	if err := registry.Register("<runtime-reload>", func(api extensions.API) error {
		instances++
		api.RegisterFlag("runtime-flag", extensions.Flag{Type: extensions.FlagString, Default: "initial"})
		api.On(extensions.EventSessionStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			log.add("start:" + string(raw.(extensions.SessionStartEvent).Reason))
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple,
		Model: provider.GetModel(), ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	session := host.Session()
	initialRegistry := host.Services().ExtensionRegistry
	oldRunner := session.ExtensionRunner()
	oldContext := oldRunner.CreateCommandContext()
	if err := session.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	if host.Session() != session {
		t.Fatal("reload replaced the session")
	}
	if session.ExtensionRunner() == oldRunner {
		t.Fatal("reload reused the extension runner")
	}
	reloadedRegistry := host.Services().ExtensionRegistry
	if reloadedRegistry == nil || reloadedRegistry == initialRegistry || host.options.ExtensionRegistry != reloadedRegistry {
		t.Fatalf("authoritative reload registry = services %p, options %p, initial %p", reloadedRegistry, host.options.ExtensionRegistry, initialRegistry)
	}
	if instances != 2 {
		t.Fatalf("extension instances = %d, want 2", instances)
	}
	if got, want := log.snapshot(), []string{"start:startup", "start:reload"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reload starts = %#v, want %#v", got, want)
	}
	assertRuntimeContextStale(t, func() { _ = oldContext.CWD() })
	if got := session.ExtensionRunner().CreateCommandContext().CWD(); got != cwd {
		t.Fatalf("fresh context cwd = %q, want %q", got, cwd)
	}
	reloadedRegistry.SetFlagValue("runtime-flag", "changed")
	host.SetRebindSession(func(session *AgentSession) error {
		return session.BindExtensions(context.Background())
	})
	if result, err := host.NewSession(context.Background(), nil); err != nil || result.Cancelled {
		t.Fatalf("replacement after reload = %#v, %v", result, err)
	}
	if got := host.Session().ExtensionRunner().FlagValues()["runtime-flag"]; got != "changed" {
		t.Fatalf("flag after reload and replacement = %#v", got)
	}
	if err := session.Reload(context.Background()); err == nil || err.Error() != "codingagent: cannot reload a replaced session" {
		t.Fatalf("stale session reload error = %v", err)
	}
}

func TestAgentSessionRuntimeWithSessionCanReplaceAgain(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.SetRebindSession(func(session *AgentSession) error {
		return session.BindExtensions(context.Background())
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	replacements := 0
	_, err = host.NewSession(ctx, &extensions.NewSessionOptions{
		WithSession: func(ctx context.Context, replaced extensions.ReplacedSessionContext) error {
			replacements++
			result, nestedErr := replaced.NewSession(ctx, nil)
			if nestedErr == nil && result.Cancelled {
				nestedErr = errors.New("nested replacement was cancelled")
			}
			return nestedErr
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replacements != 1 {
		t.Fatalf("withSession calls = %d", replacements)
	}
}

func TestAgentSessionRuntimeKeepsReplacementLockedThroughRebind(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	host.SetRebindSession(func(session *AgentSession) error {
		close(entered)
		<-release
		return session.BindExtensions(context.Background())
	})
	done := make(chan error, 1)
	go func() {
		_, replaceErr := host.NewSession(context.Background(), nil)
		done <- replaceErr
	}()
	<-entered
	if host.opMu.TryLock() {
		host.opMu.Unlock()
		close(release)
		t.Fatal("replacement mutex was released before rebind completed")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAgentSessionRuntimeForkPersistedBeforeRoot(t *testing.T) {
	t.Parallel()
	cwd := t.TempDir()
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{runtimeAssistant(provider, "reply", 10)})
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: cwd, AgentDir: t.TempDir(), StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if err := host.Session().BindExtensions(context.Background()); err != nil {
		t.Fatal(err)
	}
	host.SetRebindSession(func(session *AgentSession) error {
		return session.BindExtensions(context.Background())
	})
	if err := host.Session().PromptSync(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	oldFile := host.Session().Manager().GetSessionFile()
	users := host.Session().GetUserMessagesForForking()
	if len(users) != 1 {
		t.Fatalf("forkable messages = %d", len(users))
	}
	result, err := host.Fork(context.Background(), users[0].EntryID, nil)
	if err != nil || result.Cancelled {
		t.Fatalf("fork = %#v, %v", result, err)
	}
	newFile := host.Session().Manager().GetSessionFile()
	if newFile == "" || newFile == oldFile {
		t.Fatalf("fork path = %q, old %q", newFile, oldFile)
	}
	header := host.Session().Manager().GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != oldFile {
		t.Fatalf("fork parent = %#v, want %q", header, oldFile)
	}
	if got := host.Session().Manager().BuildSessionContext().Messages; len(got) != 0 {
		t.Fatalf("fork-before-root retained %d messages", len(got))
	}
}

func TestAgentSessionRuntimeRecreatesServicesForSwitchedCWD(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	initialCWD := filepath.Join(root, "initial")
	targetCWD := filepath.Join(root, "target")
	if err := os.MkdirAll(initialCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(targetCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	agentDir := filepath.Join(root, "agent")
	provider := testFaux(100000)
	var seenTrustContext extensions.ProjectTrustContext
	factory := func(_ context.Context, options AgentSessionOptions) (*AgentSessionResult, error) {
		seenTrustContext = options.ProjectTrustContext
		return NewAgentSession(options)
	}
	host, err := NewAgentSessionRuntime(context.Background(), AgentSessionOptions{
		CWD: initialCWD, AgentDir: agentDir, StreamFn: provider.StreamSimple, Model: provider.GetModel(),
	}, factory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { host.Dispose(context.Background()) })
	if host.Services() == nil || host.Services().SettingsManager == nil || host.Services().ModelRegistry == nil {
		t.Fatalf("initial services = %#v", host.Services())
	}
	if got := host.CWD(); got != initialCWD {
		t.Fatalf("initial cwd = %q", got)
	}
	target, err := sessionstore.Create(targetCWD, filepath.Join(root, "target-sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.AppendMessage(map[string]any{"role": "user", "content": "target", "timestamp": int64(1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.AppendMessage(map[string]any{
		"role": "assistant", "content": []any{}, "api": "faux", "provider": "faux",
		"model": "faux", "usage": map[string]any{}, "stopReason": "stop", "timestamp": int64(2),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.SwitchSession(context.Background(), target.GetSessionFile(), &AgentSessionRuntimeSwitchOptions{
		ProjectTrustContextFactory: func(cwd string) extensions.ProjectTrustContext {
			return agentSessionRuntimeTrustContext{cwd: cwd}
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := host.CWD(); got != targetCWD {
		t.Fatalf("switched cwd = %q, want %q", got, targetCWD)
	}
	services := host.Services()
	if services == nil || services.CWD != targetCWD || services.SettingsManager == nil || services.Resources == nil {
		t.Fatalf("switched services = %#v", services)
	}
	if seenTrustContext == nil || seenTrustContext.CWD() != targetCWD {
		t.Fatalf("project trust context = %#v", seenTrustContext)
	}
}

func assertRuntimeContextStale(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		recovered := recover()
		if recovered == nil || recovered != "This extension ctx is stale after session replacement or reload. Do not use a captured pi or command ctx after ctx.newSession(), ctx.fork(), ctx.switchSession(), or ctx.reload(). For newSession, fork, and switchSession, move post-replacement work into withSession and use the ctx passed to withSession. For reload, do not use the old ctx after await ctx.reload()." {
			t.Fatalf("stale context panic = %#v", recovered)
		}
	}()
	call()
}

func runtimeLifecycleRegistry(
	t *testing.T,
	cwd string,
	log *agentSessionRuntimeEventLog,
	cancelSwitch bool,
) *extensions.Registry {
	t.Helper()
	registry := extensions.NewRegistry(cwd)
	err := registry.Register("<runtime-test>", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionStartEvent)
			previous := ""
			if event.PreviousSessionFile != nil {
				previous = *event.PreviousSessionFile
			}
			log.add(fmt.Sprintf("start:%s:%s", event.Reason, previous))
			return nil, nil
		})
		api.On(extensions.EventSessionBeforeSwitch, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeSwitchEvent)
			target := ""
			if event.TargetSessionFile != nil {
				target = *event.TargetSessionFile
			}
			log.add(fmt.Sprintf("before:%s:%s", event.Reason, target))
			return extensions.SessionBeforeSwitchResult{Cancel: cancelSwitch}, nil
		})
		api.On(extensions.EventSessionBeforeFork, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionBeforeForkEvent)
			log.add(fmt.Sprintf("before-fork:%s:%s", event.EntryID, event.Position))
			return nil, nil
		})
		api.On(extensions.EventSessionShutdown, func(_ context.Context, raw extensions.Event, _ extensions.Context) (any, error) {
			event := raw.(extensions.SessionShutdownEvent)
			target := ""
			if event.TargetSessionFile != nil {
				target = *event.TargetSessionFile
			}
			log.add(fmt.Sprintf("shutdown:%s:%s", event.Reason, target))
			return nil, nil
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return registry
}
