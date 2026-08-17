package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/engine"
)

// TestLazyToolPromptGuidelinesRefreshPerTurn proves the runtime side of the
// lazy-promptGuidelines wire gap closure: a tool's PromptGuidelinesFunc is
// re-read before each turn and the system prompt handed to the provider
// carries the current value, not the registration-time snapshot.
func TestLazyToolPromptGuidelinesRefreshPerTurn(t *testing.T) {
	isolateSDKAgentDir(t)
	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{
		runtimeAssistant(provider, "one", 3),
		runtimeAssistant(provider, "two", 3),
	})

	var promptsMu sync.Mutex
	var systemPrompts []string
	stream := func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		prompt := ""
		if request.SystemPrompt != nil {
			prompt = *request.SystemPrompt
		}
		promptsMu.Lock()
		systemPrompts = append(systemPrompts, prompt)
		promptsMu.Unlock()
		return provider.StreamSimple(ctx, model, request, options)
	}

	reads := 0
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("<test:lazy>", func(api extensions.API) error {
		api.RegisterTool(extensions.ToolDefinition{
			Name:             "lazy_tool",
			Description:      "tool with lazily changing guidelines",
			Parameters:       ai.JSONSchema(`{"type":"object"}`),
			PromptGuidelines: []string{"guideline-snapshot"},
			PromptGuidelinesFunc: func(context.Context) ([]string, error) {
				reads++
				return []string{fmt.Sprintf("guideline-read-%d", reads)}, nil
			},
			Execute: func(context.Context, string, any, engine.AgentToolUpdateCallback, extensions.Context) (engine.AgentToolResult, error) {
				return engine.AgentToolResult{}, nil
			},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := NewAgentSession(AgentSessionOptions{
		StreamFn:          stream,
		Model:             provider.GetModel(),
		Resources:         &Resources{},
		ExtensionRegistry: registry,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Session.Dispose()

	if err := result.Session.PromptSync(context.Background(), "first"); err != nil {
		t.Fatal(err)
	}
	if err := result.Session.PromptSync(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}

	promptsMu.Lock()
	defer promptsMu.Unlock()
	if len(systemPrompts) != 2 {
		t.Fatalf("system prompts captured = %d, want 2", len(systemPrompts))
	}
	if !strings.Contains(systemPrompts[0], "guideline-read-1") || strings.Contains(systemPrompts[0], "guideline-snapshot") {
		t.Fatalf("first turn system prompt did not carry the lazily-read guidelines:\n%s", systemPrompts[0])
	}
	if !strings.Contains(systemPrompts[1], "guideline-read-2") {
		t.Fatalf("second turn system prompt kept a stale guideline read:\n%s", systemPrompts[1])
	}
}
