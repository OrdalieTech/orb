package agentmemory

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
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
	if limit == 0 || limit > 100 {
		limit = 100
	}
	result := make([]memorysdk.Item, 0, len(store.items))
	for index := len(store.items) - 1; index >= 0 && (limit < 0 || len(result) < limit); index-- {
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
	target := engine.NewAgent(provider.StreamSimple, engine.WithInitialState(engine.AgentState{
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
	store, err := filestore.NewFileStore(t.TempDir())
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

func TestSyncedOverFullDevicesKeepRememberWorkingAndRecallOlder(t *testing.T) {
	ctx := context.Background()
	storeA, err := filestore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storeB, err := filestore.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Each device fills MEMORY near 2200 chars and USER near 1375 chars.
	oldestMem := "Ancient aurora observatory note: " + strings.Repeat("a", 900)
	if _, err := storeA.Append(ctx, memorysdk.Item{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Content: oldestMem, Tags: []string{memoryTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Append(ctx, memorysdk.Item{Time: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC), Content: "Device A second fact: " + strings.Repeat("b", 1000), Tags: []string{memoryTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeA.Append(ctx, memorysdk.Item{Time: time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), Content: "Device A user pref: " + strings.Repeat("u", 1000), Tags: []string{"user", userTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.Append(ctx, memorysdk.Item{Time: time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC), Content: "Device B first fact: " + strings.Repeat("c", 1000), Tags: []string{memoryTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.Append(ctx, memorysdk.Item{Time: time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC), Content: "Device B second fact: " + strings.Repeat("d", 1000), Tags: []string{memoryTargetTag}}); err != nil {
		t.Fatal(err)
	}
	if _, err := storeB.Append(ctx, memorysdk.Item{Time: time.Date(2026, 3, 3, 0, 0, 0, 0, time.UTC), Content: "Device B user pref: " + strings.Repeat("v", 1000), Tags: []string{"user", userTargetTag}}); err != nil {
		t.Fatal(err)
	}

	// Merge B into A as Mitosis sync does: combined MEMORY ~4000 chars, USER ~2000 chars.
	fromB, err := memorysdk.Shared{Store: storeB}.Shareable(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (memorysdk.Shared{Store: storeA}).MergeShared(ctx, []map[string]json.RawMessage{fromB}, true, nil); err != nil {
		t.Fatal(err)
	}

	rt, err := New(storeA)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]engine.AgentTool{}
	for _, tool := range rt.Tools() {
		tools[tool.Spec().Name] = tool
	}

	// remember still works even though both MEMORY and USER exceed their prompt budgets.
	if _, err := tools["remember"].Execute(ctx, "r1", map[string]any{"target": "memory", "content": "Fresh fact after sync merge."}, nil); err != nil {
		t.Fatalf("remember failed on over-full merged store: %v", err)
	}
	if _, err := tools["remember"].Execute(ctx, "r2", map[string]any{"target": "user", "content": "User likes concise summaries."}, nil); err != nil {
		t.Fatalf("remember(user) failed on over-full merged store: %v", err)
	}

	// Rendered prompt stays bounded (newest first) and advertises spilled older memories.
	if err := rt.Load(ctx); err != nil {
		t.Fatal(err)
	}
	prompt := rt.SystemPrompt("base")
	if !strings.Contains(prompt, "Fresh fact after sync merge.") || !strings.Contains(prompt, "User likes concise summaries.") {
		t.Fatalf("newest remembered facts missing from prompt:\n%s", prompt)
	}
	if strings.Contains(prompt, "Ancient aurora observatory note") {
		t.Fatalf("oldest spilled memory should not be rendered inside prompt budget:\n%s", prompt)
	}
	if !strings.Contains(prompt, "older memories · use recall to search)") && !strings.Contains(prompt, "older memory · use recall to search)") {
		t.Fatalf("missing spill footer in prompt:\n%s", prompt)
	}

	// recall finds the spilled oldest memory (both substring and word overlap).
	res, err := tools["recall"].Execute(ctx, "q1", map[string]any{"query": "aurora"}, nil)
	if err != nil || !strings.Contains(ai.ContentText(res.Content), "Ancient aurora observatory note") {
		t.Fatalf("substring recall missed spilled item: %v, %q", err, ai.ContentText(res.Content))
	}
	res, err = tools["recall"].Execute(ctx, "q2", map[string]any{"query": "observatory ancient"}, nil)
	if err != nil || !strings.Contains(ai.ContentText(res.Content), "Ancient aurora observatory note") {
		t.Fatalf("word-overlap recall missed spilled item: %v, %q", err, ai.ContentText(res.Content))
	}

	// replace and forget work on spilled older memories too.
	if _, err := tools["replace"].Execute(ctx, "rep1", map[string]any{
		"target": "memory", "old_text": "Ancient aurora observatory note", "content": "Updated aurora note.",
	}, nil); err != nil {
		t.Fatalf("replace failed on spilled item: %v", err)
	}
	if _, err := tools["forget"].Execute(ctx, "f1", map[string]any{"query": "Updated aurora note"}, nil); err != nil {
		t.Fatalf("forget failed on spilled item: %v", err)
	}
}

func TestMemoryStoreCapRefusesWithoutEvicting(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	for i := range memoryStoreItemLimit {
		if _, err := store.Append(ctx, memorysdk.Item{
			Content: fmt.Sprintf("fact number %03d", i),
			Tags:    []string{memoryTargetTag},
		}); err != nil {
			t.Fatal(err)
		}
	}
	rt, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	tools := map[string]engine.AgentTool{}
	for _, tool := range rt.Tools() {
		tools[tool.Spec().Name] = tool
	}
	_, err = tools["remember"].Execute(ctx, "over", map[string]any{"target": "memory", "content": "one too many"}, nil)
	if err == nil || !strings.Contains(err.Error(), "memory store is full") {
		t.Fatalf("expected store cap refusal, got %v", err)
	}
	all, _ := store.Query(ctx, memorysdk.Filter{Limit: -1})
	if len(all) != memoryStoreItemLimit {
		t.Fatalf("store cap evicted items: have %d, want %d", len(all), memoryStoreItemLimit)
	}
	// Oldest item (index 0, well past 100) is still searchable and forgettable, freeing space.
	res, err := tools["recall"].Execute(ctx, "old", map[string]any{"query": "number 000"}, nil)
	if err != nil || !strings.Contains(ai.ContentText(res.Content), "fact number 000") {
		t.Fatalf("recall missed oldest item past 100: %v, %q", err, ai.ContentText(res.Content))
	}
	if _, err := tools["forget"].Execute(ctx, "drop", map[string]any{"query": "fact number 000"}, nil); err != nil {
		t.Fatalf("forget oldest item failed: %v", err)
	}
	if _, err := tools["remember"].Execute(ctx, "ok", map[string]any{"target": "memory", "content": "fits now"}, nil); err != nil {
		t.Fatalf("remember after forget failed: %v", err)
	}

	// Char cap refusal without eviction; shrinking replace still works.
	charStore := &testStore{}
	big := strings.Repeat("x", 8000)
	for i := range 8 {
		_, _ = charStore.Append(ctx, memorysdk.Item{Content: fmt.Sprintf("block-%d %s", i, big), Tags: []string{memoryTargetTag}})
	}
	crt, _ := New(charStore)
	ctools := map[string]engine.AgentTool{}
	for _, tool := range crt.Tools() {
		ctools[tool.Spec().Name] = tool
	}
	if _, err := ctools["remember"].Execute(ctx, "c-over", map[string]any{"target": "memory", "content": strings.Repeat("y", 2000)}, nil); err == nil || !strings.Contains(err.Error(), "memory store is full") {
		t.Fatalf("expected char cap refusal, got %v", err)
	}
	if _, err := ctools["replace"].Execute(ctx, "c-shrink", map[string]any{"target": "memory", "old_text": "block-0", "content": "short"}, nil); err != nil {
		t.Fatalf("shrinking replace at char cap failed: %v", err)
	}
}
