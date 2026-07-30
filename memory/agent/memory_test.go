package agentmemory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	memorysdk "github.com/OrdalieTech/orb/memory"
)

type testStore struct {
	mu     sync.Mutex
	items  []memorysdk.Item
	nextID int
}

func (store *testStore) Append(_ context.Context, item memorysdk.Item) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.nextID++
	item.ID = fmt.Sprintf("item-%d", store.nextID)
	if item.Time.IsZero() {
		item.Time = time.Date(2026, 7, 30, 12, store.nextID, 0, 0, time.UTC)
	}
	store.items = append(store.items, item)
	return item.ID, nil
}

func (store *testStore) Get(_ context.Context, id string) (memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, item := range store.items {
		if item.ID == id {
			return item, nil
		}
	}
	return memorysdk.Item{}, fmt.Errorf("missing %s", id)
}

func (store *testStore) Query(_ context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	result := make([]memorysdk.Item, 0, min(limit, len(store.items)))
	for index := len(store.items) - 1; index >= 0 && len(result) < limit; index-- {
		item := store.items[index]
		if filter.Contains != "" && !strings.Contains(strings.ToLower(item.Content), strings.ToLower(filter.Contains)) ||
			!containsAll(item.Tags, filter.Tags) {
			continue
		}
		result = append(result, item)
	}
	return result, nil
}

func (store *testStore) Delete(_ context.Context, id string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items = slices.DeleteFunc(store.items, func(item memorysdk.Item) bool { return item.ID == id })
	return nil
}

func containsAll(values, required []string) bool {
	for _, value := range required {
		if !slices.Contains(values, value) {
			return false
		}
	}
	return true
}

func TestAttachMakesMemoryAvailableToPlainAgent(t *testing.T) {
	store := &testStore{items: []memorysdk.Item{{
		ID: "profile", Time: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC),
		Content: "The user prefers concise answers.", Tags: []string{"user", "orb:memory:user"},
	}}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	var systemPrompt string
	var toolNames []string
	provider.SetResponses([]faux.ResponseStep{faux.Factory(func(
		_ context.Context, request ai.Context, _ *ai.StreamOptions, _ faux.State, _ *ai.Model,
	) (*ai.AssistantMessage, error) {
		systemPrompt = *request.SystemPrompt
		for _, tool := range *request.Tools {
			toolNames = append(toolNames, tool.Name)
		}
		return faux.AssistantMessage("done"), nil
	})})
	target := agent.NewAgent(provider.StreamSimple, agent.WithInitialState(agent.AgentState{
		Model: provider.GetModel(), SystemPrompt: "base",
	}))
	if err := Attach(context.Background(), target, store); err != nil {
		t.Fatal(err)
	}
	if err := target.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(systemPrompt, "The user prefers concise answers.") ||
		!slices.Equal(toolNames, []string{"remember", "recall", "replace", "forget"}) {
		t.Fatalf("system prompt = %q, tools = %v", systemPrompt, toolNames)
	}
}

type gatedStore struct {
	testStore
	entered chan struct{}
	release chan struct{}
}

func (store *gatedStore) Query(ctx context.Context, filter memorysdk.Filter) ([]memorysdk.Item, error) {
	store.entered <- struct{}{}
	select {
	case <-store.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return store.testStore.Query(ctx, filter)
}

func TestRuntimesDoNotSerializeDifferentTenantStores(t *testing.T) {
	blocked := &gatedStore{entered: make(chan struct{}, 1), release: make(chan struct{})}
	first, err := New(blocked)
	if err != nil {
		t.Fatal(err)
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- first.Load(context.Background()) }()
	<-blocked.entered

	second, err := New(&testStore{})
	if err != nil {
		t.Fatal(err)
	}
	secondDone := make(chan error, 1)
	go func() { secondDone <- second.Load(context.Background()) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("one tenant's store blocked another tenant's memory runtime")
	}
	close(blocked.release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRuntimesShareTransactionalStoreAtomically(t *testing.T) {
	store, err := memorysdk.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	left, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	right, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 2)
	for _, runtime := range []*Runtime{left, right} {
		go func() {
			_, executeErr := runtime.Tools()[0].Execute(context.Background(), "remember", map[string]any{
				"target": "memory", "content": "One shared fact.",
			}, nil)
			errs <- executeErr
		}()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.Query(context.Background(), memorysdk.Filter{})
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %#v, error = %v", items, err)
	}
}

type semanticStore struct {
	testStore
	scored []memorysdk.Scored
}

func (store *semanticStore) Search(context.Context, string, int) ([]memorysdk.Scored, error) {
	return append([]memorysdk.Scored(nil), store.scored...), nil
}

type overeagerQueryStore struct {
	testStore
	items []memorysdk.Item
}

func (store *overeagerQueryStore) Query(context.Context, memorysdk.Filter) ([]memorysdk.Item, error) {
	return append([]memorysdk.Item(nil), store.items...), nil
}

func TestRecallDefensivelyEnforcesStoreLimit(t *testing.T) {
	store := &overeagerQueryStore{items: make([]memorysdk.Item, 200)}
	items, err := recallItems(context.Background(), store, "", nil, 3)
	if err != nil || len(items) != 3 {
		t.Fatalf("items = %d, error = %v", len(items), err)
	}
}

func TestSemanticRecallFiltersTagsAndEnforcesLimit(t *testing.T) {
	store := &semanticStore{scored: []memorysdk.Scored{
		{Item: memorysdk.Item{ID: "wrong", Content: "wrong", Tags: []string{"other"}}, Score: 1},
		{Item: memorysdk.Item{ID: "right", Content: "right", Tags: []string{"wanted"}}, Score: .9},
		{Item: memorysdk.Item{ID: "extra", Content: "extra", Tags: []string{"wanted"}}, Score: .8},
	}}
	items, err := recallItems(context.Background(), store, "query", []string{"wanted"}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "right" {
		t.Fatalf("semantic recall = %#v", items)
	}
}

func TestRecallFallsBackToWordOverlap(t *testing.T) {
	store := &testStore{items: []memorysdk.Item{
		{ID: "tabs", Content: "The user prefers tabs over spaces."},
		{ID: "deploy", Content: "Deploys run on Fridays."},
	}}
	for _, test := range []struct{ query, want string }{
		{query: "indentation preference", want: "tabs"},
		{query: "Fridays", want: "deploy"},
		{query: "prefers tabs", want: "tabs"},
	} {
		items, err := recallItems(context.Background(), store, test.query, nil, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ID != test.want {
			t.Fatalf("recall(%q) = %#v, want %q", test.query, items, test.want)
		}
	}
	items, err := recallItems(context.Background(), store, "kubernetes rollout", nil, 10)
	if err != nil || len(items) != 0 {
		t.Fatalf("unrelated query matched %#v (%v)", items, err)
	}
}
