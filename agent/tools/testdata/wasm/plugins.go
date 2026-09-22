package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/plugins/memory"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/tasks"
	"github.com/OrdalieTech/orb/plugins/usage"
)

type memoryStore struct{ items []memory.Item }

func (s *memoryStore) Append(_ context.Context, item memory.Item) (string, error) {
	item.ID = fmt.Sprint(len(s.items) + 1)
	s.items = append(s.items, item)
	return item.ID, nil
}
func (s *memoryStore) Query(context.Context, memory.Filter) ([]memory.Item, error) {
	return append([]memory.Item(nil), s.items...), nil
}
func (s *memoryStore) Get(_ context.Context, id string) (memory.Item, error) {
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return memory.Item{}, fmt.Errorf("missing memory %s", id)
}
func (s *memoryStore) Delete(_ context.Context, id string) error {
	for i, item := range s.items {
		if item.ID == id {
			s.items = append(s.items[:i], s.items[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("missing memory %s", id)
}

type quotaTransport struct{}

func (quotaTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Header.Get("Authorization") != "Bearer test-key" {
		return nil, fmt.Errorf("missing quota credential")
	}
	return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"usage":{"rolling":{"percent":25,"resetsAt":"2030-01-01T00:00:00Z"}}}`))}, nil
}

func checkPlugins() {
	for _, fact := range []string{"First instance fact.", "Second instance fact."} {
		store := &memoryStore{}
		registry := extensions.NewRegistry("/workspace")
		if err := registry.Register("memory", memoryextension.Extension(store)); err != nil {
			panic(err)
		}
		if err := registry.Register("tasks", tasks.Extension()); err != nil {
			panic(err)
		}
		runner := extensions.NewRunner(registry, extensions.RunnerOptions{})
		for _, registered := range runner.AllRegisteredTools() {
			tool := extensions.WrapRegisteredTool(registered, runner)
			switch tool.Spec().Name {
			case "remember":
				run(tool, map[string]any{"target": "memory", "content": fact})
			case "todo":
				result := run(tool, map[string]any{"items": []any{map[string]any{"text": "Portable task", "status": "pending"}}})
				if !strings.Contains(ai.ContentText(result.Content), "Portable task") {
					panic("tasks result missing")
				}
			}
		}
		if len(store.items) != 1 || store.items[0].Content != fact {
			panic("memory stores were not isolated")
		}
	}
	client := usage.Client{HTTPClient: &http.Client{Transport: quotaTransport{}}}
	key := "test-key"
	snapshot, err := client.Fetch(context.Background(), "opencode-go", auth.ModelAuth{APIKey: &key})
	if err != nil {
		panic(err)
	}
	if len(snapshot.Windows) == 0 {
		panic("quota windows missing")
	}
	fmt.Println("portable plugins OK")
}
