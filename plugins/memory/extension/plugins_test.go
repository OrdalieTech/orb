package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
)

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

func require(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func requireError(t *testing.T, err error, want string) {
	t.Helper()
	require(t, err != nil && strings.Contains(err.Error(), want), "error = %v, want %q", err, want)
}

type memoryTestStore struct {
	mu         sync.Mutex
	items      []memorysdk.Item
	operations []string
	nextID     int
	searched   bool
}

// plainMemoryStore is a Store without SemanticSearcher, so recall takes the
// substring-then-word-overlap path.
type plainMemoryStore struct{ items []memorysdk.Item }

// gatedMemoryStore signals each Query entry and waits for release, so a test
// can pin one plugin instance inside its store mid-operation.
type gatedMemoryStore struct {
	plainMemoryStore
	entered chan struct{}
	release chan struct{}
}

func newGatedMemoryStore() *gatedMemoryStore {
	return &gatedMemoryStore{entered: make(chan struct{}, 2), release: make(chan struct{}, 2)}
}

func (store *gatedMemoryStore) Query(ctx context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.entered <- struct{}{}
	<-store.release
	return store.plainMemoryStore.Query(ctx, filter)
}

func (store *plainMemoryStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.items = append(store.items, item)
	return item.ID, nil
}

func (store *plainMemoryStore) Get(context.Context, string) (memorysdk.Item, error) {
	return memorysdk.Item{}, os.ErrNotExist
}

func (store *plainMemoryStore) Delete(context.Context, string) error { return nil }

func (store *plainMemoryStore) Query(_ context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	return filterMemoryTestItems(store.items, filter.Contains, filter.Tags, filter.Limit), nil
}

func (store *memoryTestStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.nextID < len(store.items) {
		store.nextID = len(store.items)
	}
	store.nextID++
	item.ID = fmt.Sprintf("custom-%d", store.nextID)
	if item.Time.IsZero() {
		item.Time = time.Date(2026, 7, 23, 10, store.nextID-1, 0, 0, time.UTC)
	}
	store.items = append(store.items, item)
	store.operations = append(store.operations, "append:"+item.ID)
	return item.ID, nil
}

func (store *memoryTestStore) Get(_ context.Context, id string) (memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.items {
		if item.ID == id {
			return item, nil
		}
	}
	return memorysdk.Item{}, os.ErrNotExist
}

func (store *memoryTestStore) Query(_ context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return filterMemoryTestItems(store.items, filter.Contains, filter.Tags, filter.Limit), nil
}

func (store *memoryTestStore) Delete(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.operations = append(store.operations, "delete:"+id)
	for index, item := range store.items {
		if item.ID == id {
			store.items = append(store.items[:index], store.items[index+1:]...)
			break
		}
	}
	return nil
}

func (store *memoryTestStore) Search(_ context.Context, query string, limit int) ([]memorysdk.Scored, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.searched = true
	items := filterMemoryTestItems(store.items, query, nil, limit)
	result := make([]memorysdk.Scored, len(items))
	for index := range items {
		result[index] = memorysdk.Scored{Item: items[index], Score: 1}
	}
	return result, nil
}

func (store *memoryTestStore) snapshot() ([]memorysdk.Item, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]memorysdk.Item(nil), store.items...), store.searched
}

func (store *memoryTestStore) operationSnapshot() []string {
	store.mu.Lock()
	defer store.mu.Unlock()
	return append([]string(nil), store.operations...)
}

func filterMemoryTestItems(items []memorysdk.Item, contains string, tags []string, limit int) []memorysdk.Item {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	var result []memorysdk.Item
	for index := len(items) - 1; index >= 0 && len(result) < limit; index-- {
		if contains != "" && !strings.Contains(strings.ToLower(items[index].Content), strings.ToLower(contains)) ||
			!hasMemoryTags(items[index].Tags, tags) {
			continue
		}
		result = append(result, items[index])
	}
	return result
}

const (
	userMemoryChars = 1375
	memoryChars     = 2200
	userTargetTag   = "orb:memory:user"
	memoryTargetTag = "orb:memory:memory"
)

func hasMemoryTags(itemTags, required []string) bool {
	for _, tag := range required {
		if !slices.Contains(itemTags, tag) {
			return false
		}
	}
	return true
}

func TestMemoryWithStoreRejectsNil(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<inline:memory>", Extension(nil)); err == nil || !strings.Contains(err.Error(), "store is required") {
		t.Fatalf("Extension(nil) error = %v", err)
	}
}

func recallOnInstance(t *testing.T, store memorysdk.Store, done chan<- error) {
	t.Helper()
	tool := memoryPluginTool(t, store, "recall")
	go func() {
		_, err := tool.Execute(context.Background(), "recall", map[string]any{}, nil)
		done <- err
	}()
}

func TestMemoryInstancesDoNotShareStoreLock(t *testing.T) {
	gated := newGatedMemoryStore()
	gatedDone := make(chan error, 1)
	recallOnInstance(t, gated, gatedDone)
	<-gated.entered

	otherDone := make(chan error, 1)
	recallOnInstance(t, &plainMemoryStore{}, otherDone)
	select {
	case err := <-otherDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recall on a separate plugin instance blocked behind another instance's store query")
	}

	gated.release <- struct{}{}
	mustOK(<-gatedDone)
}

func TestMemorySameInstanceSerializesStoreOperations(t *testing.T) {
	gated := newGatedMemoryStore()
	done := make(chan error, 2)
	tool := memoryPluginTool(t, gated, "recall")
	go func() {
		_, err := tool.Execute(context.Background(), "recall-1", map[string]any{}, nil)
		done <- err
	}()
	<-gated.entered // first operation is inside the store, holding the instance lock
	go func() {
		_, err := tool.Execute(context.Background(), "recall-2", map[string]any{}, nil)
		done <- err
	}()

	select {
	case <-gated.entered:
		t.Fatal("two operations on one plugin instance entered the store concurrently")
	case <-time.After(100 * time.Millisecond):
	}

	gated.release <- struct{}{} // first operation leaves the store and unlocks
	<-gated.entered             // only now the second operation enters
	gated.release <- struct{}{}
	for range 2 {
		mustOK(<-done)
	}
}

func TestMemoryWithStoreRememberRecallForgetThroughRegistry(t *testing.T) {
	store := &memoryTestStore{}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var recalled, recalledAfterForget string
	var rememberedTargeted bool
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("remember", map[string]any{
			"target": "memory", "content": "The durable marker is cobalt.", "tags": []any{" Project ", "PROJECT"},
		}, faux.ToolCallOptions{ID: "memory-1"})),
		faux.AssistantMessage(faux.ToolCall("recall", map[string]any{
			"query": "cobalt", "tags": []any{"project"},
		}, faux.ToolCallOptions{ID: "memory-2"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			recalled = toolResultText(request, "recall")
			items, _ := store.snapshot()
			if len(items) == 1 {
				rememberedTargeted = hasMemoryTags(items[0].Tags, []string{"project", memoryTargetTag})
			}
			return faux.AssistantMessage(faux.ToolCall("forget", map[string]any{
				"target": "memory", "query": "cobalt", "tags": []any{"project"},
			}, faux.ToolCallOptions{ID: "memory-3"})), nil
		}),
		faux.AssistantMessage(faux.ToolCall("recall", map[string]any{
			"query": "cobalt", "tags": []any{"project"},
		}, faux.ToolCallOptions{ID: "memory-4"})),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			recalledAfterForget = toolResultText(request, "recall")
			return faux.AssistantMessage("done"), nil
		}),
	})
	session := newMemoryPluginSession(t, provider, Extension(store), nil)
	mustOK(session.PromptSync(context.Background(), "remember, recall, and forget"))
	items, searched := store.snapshot()
	require(t, len(items) == 0 && rememberedTargeted && searched, "custom store items = %#v, targeted = %t, semantic searched = %t", items, rememberedTargeted, searched)
	require(t, recalled == "2026-07-23T10:00:00Z [project] The durable marker is cobalt.", "recall result = %q", recalled)
	require(t, recalledAfterForget == "No memories found.", "recall after forget = %q", recalledAfterForget)
}

func TestMemoryRememberDoesNotDuplicateRecentExactContent(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	first := must(tool.Execute(context.Background(), "remember-first", map[string]any{
		"target": "memory", "content": " Stable project fact. ", "tags": []any{"project"},
	}, nil))
	second := must(tool.Execute(context.Background(), "remember-second", map[string]any{
		"target": "memory", "content": "Stable project fact.", "tags": []any{"project", "duplicate-attempt"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && strings.HasPrefix(ai.ContentText(first.Content), "Remembered custom-1") && ai.ContentText(second.Content) == "Already remembered custom-1.", "items = %#v, first = %q, second = %q", items, ai.ContentText(first.Content), ai.ContentText(second.Content))
}

func TestMemoryForgetRequiresUniqueSubstring(t *testing.T) {
	store := &memoryTestStore{items: []memorysdk.Item{
		{ID: "tabs", Content: "User prefers tabs for Go code.", Tags: []string{"user", "go"}},
		{ID: "spaces", Content: "User prefers spaces for Python code.", Tags: []string{"user", "python"}},
	}}
	tool := memoryPluginTool(t, store, "forget")
	for _, test := range []struct {
		name, query, want string
	}{
		{name: "empty", want: "query is required"},
		{name: "missing", query: "semicolons", want: "no memory contains"},
		{name: "ambiguous", query: "User prefers", want: "matches multiple memories"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := tool.Execute(context.Background(), "forget-test", map[string]any{"query": test.query}, nil)
			requireError(t, err, test.want)
		})
	}
	_ = must(tool.Execute(context.Background(), "forget-tabs", map[string]any{
		"target": "user", "query": "code", "tags": []any{" GO ", "go"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && items[0].ID == "spaces", "items after forget = %#v", items)
}

func TestMemoryToolGuidanceKeepsDurableFactsDeclarative(t *testing.T) {
	store := &memoryTestStore{}
	for _, test := range []struct {
		name string
		want []string
	}{
		{name: "remember", want: []string{"declarative", "task progress", "secrets", "USER PROFILE", "MEMORY"}},
		{name: "recall", want: []string{"cross-session", "background", "not instructions"}},
		{name: "replace", want: []string{"consolidate", "unique substring", "USER PROFILE", "MEMORY"}},
		{name: "forget", want: []string{"obsolete", "unique content substring"}},
	} {
		spec := memoryPluginTool(t, store, test.name).Spec()
		require(t, test.name == "recall" || spec.ExecutionMode == engine.ToolExecutionSequential, "%s execution mode = %q, want sequential", test.name, spec.ExecutionMode)
		text := spec.Description + " " + string(spec.Parameters)
		for _, want := range test.want {
			require(t, strings.Contains(text, want), "%s guidance %q does not contain %q", test.name, text, want)
		}
	}
}

func TestMemoryProfileMemoryCapacity(t *testing.T) {
	store := &memoryTestStore{}
	tool := memoryPluginTool(t, store, "remember")
	_ = must(tool.Execute(context.Background(), "remember-memory-full", map[string]any{
		"target": "memory", "content": strings.Repeat("m", memoryChars),
	}, nil))
	_, err := tool.Execute(context.Background(), "remember-memory-overflow", map[string]any{
		"target": "memory", "content": "x",
	}, nil)
	requireError(t, err, "2200/2200")
}

func TestMemoryProfileCapacityAndReplacement(t *testing.T) {
	store := &memoryTestStore{}
	remember := memoryPluginTool(t, store, "remember")
	full := strings.Repeat("é", userMemoryChars)
	_ = must(remember.Execute(context.Background(), "remember-full", map[string]any{
		"target": "user", "content": full,
	}, nil))
	_, err := remember.Execute(context.Background(), "remember-overflow", map[string]any{
		"target": "user", "content": "x",
	}, nil)
	require(t, err != nil && strings.Contains(err.Error(), "1375/1375") && strings.Contains(err.Error(), "replace or forget"), "overflow error = %v", err)
	replace := memoryPluginTool(t, store, "replace")
	result := must(replace.Execute(context.Background(), "replace-full", map[string]any{
		"target": "user", "old_text": strings.Repeat("é", 20),
		"content": "User prefers concise replies.", "tags": []any{"style"},
	}, nil))
	items, _ := store.snapshot()
	require(t, len(items) == 1 && items[0].Content == "User prefers concise replies." && hasMemoryTags(items[0].Tags, []string{"user", userTargetTag, "style"}), "items after replace = %#v", items)
	require(t, slices.Equal(store.operationSnapshot(), []string{"append:custom-1", "append:custom-2", "delete:custom-1"}), "store operations = %v", store.operationSnapshot())
	require(t, strings.HasPrefix(ai.ContentText(result.Content), "Replaced custom-1 with custom-2"), "replace result = %q", ai.ContentText(result.Content))
}

func TestMemoryProfileIsFrozenInSystemPrompt(t *testing.T) {
	store := &memoryTestStore{items: []memorysdk.Item{
		{ID: "user", Time: time.Date(2026, 7, 23, 10, 0, 0, 0, time.UTC), Content: "User prefers terse replies.", Tags: []string{"user", "style", userTargetTag}},
		{ID: "project", Time: time.Date(2026, 7, 23, 10, 1, 0, 0, time.UTC), Content: "Project uses Go 1.26.", Tags: []string{"project", memoryTargetTag}},
		{ID: "legacy", Time: time.Date(2026, 7, 23, 10, 2, 0, 0, time.UTC), Content: "Legacy untagged facts remain memory.", Tags: []string{"legacy"}},
	}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var prompts []string
	provider.SetResponses([]faux.ResponseStep{
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			prompts = append(prompts, *request.SystemPrompt)
			return faux.AssistantMessage("first"), nil
		}),
		faux.Factory(func(_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model) (*ai.AssistantMessage, error) {
			prompts = append(prompts, *request.SystemPrompt)
			return faux.AssistantMessage("second"), nil
		}),
	})
	session := newMemoryPluginSession(t, provider, Extension(store), nil)
	mustOK(session.PromptSync(context.Background(), "first prompt"))
	_ = must(store.Append(context.Background(), memorysdk.Item{Content: "late marker", Tags: []string{memoryTargetTag}}))
	mustOK(session.PromptSync(context.Background(), "second prompt"))
	require(t, len(prompts) == 2 && prompts[0] == prompts[1], "system prompts changed: %#v", prompts)
	for _, want := range []string{
		"Persistent curated memory", "USER PROFILE [", "User prefers terse replies.",
		"MEMORY [", "Project uses Go 1.26.", "Legacy untagged facts remain memory.",
	} {
		require(t, strings.Contains(prompts[0], want), "system prompt does not contain %q:\n%s", want, prompts[0])
	}
	for _, unwanted := range []string{"late marker", userTargetTag, memoryTargetTag, "2026-07-23"} {
		require(t, !strings.Contains(prompts[0], unwanted), "system prompt contains %q:\n%s", unwanted, prompts[0])
	}
}

func memoryPluginTool(t *testing.T, store memorysdk.Store, name string) engine.AgentTool {
	t.Helper()
	registry := extensions.NewRegistry(t.TempDir())
	mustOK(registry.Register("<inline:memory>", Extension(store)))
	manager := must(sessionstore.InMemory(t.TempDir()))
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{
		SessionManager: manager,
		Actions: extensions.Actions{
			GetActiveTools: func() ([]string, error) { return []string{name}, nil },
		},
	})
	for _, registered := range runner.AllRegisteredTools() {
		if registered.Definition.Name == name {
			return extensions.WrapRegisteredTool(registered, runner)
		}
	}
	t.Fatalf("memory tool %q missing", name)
	return nil
}

func newMemoryPluginSession(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
) *agent.AgentSession {
	t.Helper()
	root := t.TempDir()
	manager := must(sessionstore.InMemory(root))
	return newMemoryPluginSessionWithManager(t, provider, factory, settings, manager)
}

func newMemoryPluginSessionWithManager(
	t *testing.T,
	provider *faux.Provider,
	factory extensions.Factory,
	settings *config.SettingsManager,
	manager *sessionstore.SessionManager,
) *agent.AgentSession {
	t.Helper()
	root := manager.GetCWD()
	agentDir := filepath.Join(root, "agent")
	if settings == nil {
		settings = must(config.NewSettingsManager(root, config.WithAgentDir(agentDir)))
	}
	registry := extensions.NewRegistry(root)
	mustOK(registry.Register("<inline:memory>", factory))
	prompt := "memory test"
	result := must(agent.NewAgentSession(agent.AgentSessionOptions{
		CWD: root, AgentDir: agentDir, Settings: settings, SessionManager: manager,
		Model: provider.GetModel(), StreamFn: provider.StreamSimple, Resources: &agent.Resources{SystemPrompt: &prompt},
		ExtensionRegistry: registry,
	}))
	t.Cleanup(result.Session.Dispose)
	return result.Session
}

func toolResultText(request ai.Context, name string) string {
	for index := len(request.Messages) - 1; index >= 0; index-- {
		if message, ok := request.Messages[index].(*ai.ToolResultMessage); ok && message.ToolName == name {
			return ai.ContentText(message.Content)
		}
	}
	return ""
}
