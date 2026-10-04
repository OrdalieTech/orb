package titles

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
)

type fakeAPI struct {
	extensions.API
	handler extensions.Handler
	mu      sync.Mutex
	named   []string
}

func (api *fakeAPI) On(_ extensions.EventType, h extensions.Handler) { api.handler = h }
func (api *fakeAPI) SetSessionName(_ context.Context, name string) error {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.named = append(api.named, name)
	return nil
}

type fakeSessions struct {
	extensions.ReadonlySessionManager
	name *string
}

func (s *fakeSessions) GetSessionName() *string { return s.name }
func (*fakeSessions) GetSessionID() string      { return "s1" }

type fakeRegistry struct {
	extensions.ModelRegistry
	reply     string
	prompt    string
	refuse    ai.ProviderID // a provider that only runs whole conversations
	available []ai.Model
	used      []string
}

func (r *fakeRegistry) Available(map[string]string) []ai.Model { return r.available }

func (*fakeRegistry) ResolveProviderAuth(context.Context, string, map[string]string) (*aiauth.AuthResult, error) {
	return nil, nil
}
func (*fakeRegistry) ResolveModelHeaders(context.Context, ai.Model, map[string]string, ...*string) (*map[string]string, error) {
	return nil, nil
}
func (r *fakeRegistry) StreamSimple(_ context.Context, model *ai.Model, request ai.Context, _ *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
	r.used = append(r.used, model.ID)
	if model.Provider == r.refuse {
		return nil, errors.New("runs in its own conversation")
	}
	r.prompt = userText(request.Messages[0].(*ai.UserMessage).Content)
	message := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: r.reply}}, StopReason: ai.StopReasonStop}
	return func(yield func(ai.AssistantMessageEvent, error) bool) {
		yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}, nil)
	}, nil
}

type fakeContext struct {
	extensions.Context
	sessions *fakeSessions
	registry *fakeRegistry
}

func (c *fakeContext) SessionManager() extensions.ReadonlySessionManager { return c.sessions }
func (c *fakeContext) Model() *ai.Model                                  { return &ai.Model{ID: "m", Provider: "p"} }
func (c *fakeContext) ModelRegistry() extensions.ModelRegistry           { return c.registry }

func TestNamesAnUnnamedSessionOnceFromItsFirstExchange(t *testing.T) {
	api := &fakeAPI{}
	if err := Extension()(api); err != nil {
		t.Fatal(err)
	}
	registry := &fakeRegistry{reply: "“Title: Fix the Bridge pairing race.”\nextra"}
	ctx := &fakeContext{sessions: &fakeSessions{}, registry: registry}
	end := extensions.AgentEndEvent{Messages: []any{
		&ai.UserMessage{Content: ai.NewUserText("Two phones claimed the same code, why?")},
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "The first claimant wins."}}},
	}}
	for range 2 { // the second run of the same session asks nothing
		if _, err := api.handler(t.Context(), end, ctx); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		api.mu.Lock()
		n := len(api.named)
		api.mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	api.mu.Lock()
	defer api.mu.Unlock()
	if len(api.named) != 1 || api.named[0] != "Fix the Bridge pairing race" {
		t.Fatalf("named %q", api.named)
	}
	if !strings.Contains(registry.prompt, "Two phones claimed") || !strings.Contains(registry.prompt, "first claimant wins") {
		t.Fatalf("prompt %q", registry.prompt)
	}
}

func TestLeavesANamedSessionAlone(t *testing.T) {
	api := &fakeAPI{}
	_ = Extension()(api)
	name := "mine"
	registry := &fakeRegistry{reply: "Other"}
	ctx := &fakeContext{sessions: &fakeSessions{name: &name}, registry: registry}
	_, _ = api.handler(t.Context(), extensions.AgentEndEvent{Messages: []any{&ai.UserMessage{Content: ai.NewUserText("hi")}}}, ctx)
	time.Sleep(50 * time.Millisecond)
	if len(api.named) != 0 || registry.prompt != "" {
		t.Fatalf("named %q, asked %q", api.named, registry.prompt)
	}
}

func TestAConversationOnlyModelIsNamedByTheCheapestOther(t *testing.T) {
	registry := &fakeRegistry{reply: "Saluer en français", refuse: "claude-sessions", available: []ai.Model{
		{ID: "big", Provider: "openai", Cost: ai.ModelCost{Input: 5, Output: 20}},
		{ID: "sonnet", Provider: "claude-sessions"},
		{ID: "small", Provider: "opencode", Cost: ai.ModelCost{Input: 0.1, Output: 0.4}},
	}}
	title := ask(t.Context(), registry, &ai.Model{ID: "sonnet", Provider: "claude-sessions"}, "Request:\nhi")
	if title != "Saluer en français" || strings.Join(registry.used, ",") != "sonnet,small" {
		t.Fatalf("title %q, asked %v", title, registry.used)
	}
}
