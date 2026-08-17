package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	extensionhost "github.com/OrdalieTech/orb/agent/extensions/host"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers/faux"
)

// TestExtensionAgentSessionServiceRealHostFlow drives the REAL SDK
// createAgentSession through host.mjs and the wire protocol against the real
// NewAgentSession-backed service with a faux provider: create → prompt →
// events → stats → dispose, JS-tool callback round trip (prepareArguments
// host-side, schema-validated params, terminate), off-catalog model, and the
// events-before-terminal ordering as observed by the SDK proxy itself.
func TestExtensionAgentSessionServiceRealHostFlow(t *testing.T) {
	runtime, err := extensionhost.DiscoverRuntime(context.Background())
	if err != nil {
		t.Skip("extension-host e2e requires Node.js >=22.6 or Bun on PATH")
	}
	cwd, agentDir := t.TempDir(), t.TempDir()
	t.Setenv(config.EnvAgentDir, agentDir)
	manager := extensionhost.NewManager(extensionhost.Options{
		AgentDir: agentDir, CWD: cwd, Version: "test", Runtime: &runtime,
		RequestTimeout: 30 * time.Second, ShutdownTimeout: time.Second,
		BackoffBase: 10 * time.Millisecond, BackoffMax: 50 * time.Millisecond,
	})
	t.Cleanup(func() {
		if err := manager.Close(); err != nil {
			t.Errorf("close manager: %v", err)
		}
	})

	provider := testFaux(100000)
	provider.SetResponses([]faux.ResponseStep{
		serviceAssistant(provider, faux.ToolCall("store_put", map[string]any{"key": "alpha"}, faux.ToolCallOptions{ID: "call-1"}), ai.StopReasonToolUse, 7),
		serviceAssistant(provider, faux.ToolCall("structured_output", map[string]any{"value": map[string]any{"answer": float64(42)}}, faux.ToolCallOptions{ID: "call-2"}), ai.StopReasonToolUse, 5),
		serviceAssistant(provider, "must-not-run", ai.StopReasonStop, 3),
	})
	manager.SetAgentSessionService(NewExtensionAgentSessionService(ExtensionAgentSessionServiceOptions{
		CWD: cwd, AgentDir: agentDir, StreamFn: provider.StreamSimple,
	}))

	modelRegistry, err := config.NewOfflineModelRegistry(agentDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := extensions.NewRegistry(cwd)
	fixture, err := filepath.Abs(filepath.Join("testdata", "agent_session_flow.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	loadResult := manager.RegisterInto(context.Background(), registry, []string{fixture})
	if len(loadResult.Errors) != 0 || len(loadResult.Diagnostics) != 0 {
		t.Fatalf("load result = %#v", loadResult)
	}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{CWD: cwd, ModelRegistry: modelRegistry})
	definition := runner.ToolDefinition("child_flow")
	if definition == nil {
		t.Fatal("child_flow was not registered")
	}
	final, err := definition.Execute(context.Background(), "call-child-flow", map[string]any{}, nil, runner.CreateContext())
	if err != nil {
		t.Fatalf("child_flow failed: %v", err)
	}
	payload := ""
	for _, block := range final.Content {
		if textBlock, ok := block.(*ai.TextContent); ok {
			payload += textBlock.Text
		}
	}
	var result struct {
		ModelFallbackMessage  any      `json:"modelFallbackMessage"`
		EventsAtPromptResolve int      `json:"eventsAtPromptResolve"`
		SubscribeEventTypes   []string `json:"subscribeEventTypes"`
		Stats                 struct {
			Tokens struct {
				Total int64 `json:"total"`
			} `json:"tokens"`
			Cost float64 `json:"cost"`
		} `json:"stats"`
		MessageCount int            `json:"messageCount"`
		Roles        []string       `json:"roles"`
		LastRole     string         `json:"lastRole"`
		StoreParams  map[string]any `json:"storeParams"`
		Captured     map[string]any `json:"captured"`
	}
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		t.Fatalf("child_flow result %q: %v", payload, err)
	}

	if result.ModelFallbackMessage != nil {
		t.Fatalf("modelFallbackMessage = %#v (off-catalog model must resolve without fallback)", result.ModelFallbackMessage)
	}
	// Events-before-terminal, as observed by the SDK's own subscribe mirror.
	if result.EventsAtPromptResolve == 0 {
		t.Fatal("no subscribe events were delivered before prompt resolved")
	}
	eventTypes := map[string]bool{}
	for _, eventType := range result.SubscribeEventTypes {
		eventTypes[eventType] = true
	}
	for _, expected := range []string{"agent_start", "message_start", "message_end", "tool_execution_end", "agent_settled"} {
		if !eventTypes[expected] {
			t.Fatalf("subscribe event types %v missing %q", result.SubscribeEventTypes, expected)
		}
	}
	if result.Stats.Tokens.Total == 0 {
		t.Fatalf("stats mirror empty at prompt resolve: %#v", result.Stats)
	}
	// user + assistant(toolCall) + toolResult + assistant(toolCall) + toolResult
	if result.MessageCount < 5 || result.LastRole != "toolResult" {
		t.Fatalf("messages mirror = %d entries, last %q: %v", result.MessageCount, result.LastRole, result.Roles)
	}
	// prepareArguments ran host-side before the JS execute; params passed the
	// Go-side JSON-Schema gate first (D14).
	if result.StoreParams == nil || result.StoreParams["key"] != "ALPHA" {
		t.Fatalf("store_put params = %#v, want prepareArguments-uppercased key", result.StoreParams)
	}
	// The structured_output capture received the schema-parsed value exactly.
	if !reflect.DeepEqual(result.Captured, map[string]any{"answer": float64(42)}) {
		t.Fatalf("captured structured output = %#v", result.Captured)
	}
	// terminate:true ended the turn: the third faux response stayed queued.
	if pending := provider.PendingResponseCount(); pending != 1 {
		t.Fatalf("pending faux responses = %d, want 1 (terminate must end the turn)", pending)
	}
}
