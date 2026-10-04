package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

// Ports of upstream test/cache-stats.test.ts. The price source is a model list
// whose cacheRead price is $0.30/million tokens, used as the fallback on
// full-miss turns.

var cacheStatsModels = []ai.Model{
	{Provider: "test", ID: "test-model", Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{CacheRead: 0.3}}},
	{Provider: "test", ID: "other-model", Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{CacheRead: 0.3}}},
}

type cacheStatsAssistantOptions struct {
	input      int64
	cacheRead  int64
	cacheWrite int64
	cost       ai.Cost
	model      string
	timestamp  int64
}

func cacheStatsAssistant(t *testing.T, options cacheStatsAssistantOptions) *ai.AssistantMessage {
	t.Helper()
	model := options.model
	if model == "" {
		model = "test-model"
	}
	return &ai.AssistantMessage{
		Content:  ai.AssistantContent{},
		API:      "anthropic-messages",
		Provider: "test",
		Model:    model,
		Usage: ai.Usage{
			Input:      options.input,
			Output:     10,
			CacheRead:  options.cacheRead,
			CacheWrite: options.cacheWrite,
			Cost:       options.cost,
		},
		StopReason: ai.StopReasonStop,
		Timestamp:  options.timestamp,
	}
}

func newCacheStatsRuntime(t *testing.T, manager *sessionstore.SessionManager) *agent.SessionRuntime {
	t.Helper()
	settings, err := config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := agent.NewSessionRuntime(agent.SessionRuntimeConfig{
		Agent: engine.NewAgent(nil), SessionManager: manager, Settings: settings,
		AvailableModels: func() []ai.Model { return cacheStatsModels },
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func renderSessionInfo(t *testing.T, manager *sessionstore.SessionManager) string {
	t.Helper()
	terminal := newFakeTerminal(120, 24)
	ui := tui.NewTUI(terminal)
	mode := &InteractiveMode{session: newCacheStatsRuntime(t, manager), ui: ui, chat: &tui.Container{}}
	mode.handleSessionCommand()
	return strings.Join(normalizeWP450Lines(mode.chat.Render(120)), "\n")
}

func TestHandleSessionCommandShowsCacheWasteAndPerModelBreakdown(t *testing.T) {
	initTestTheme(t)
	cwd := t.TempDir()
	manager, err := sessionstore.InMemory(cwd)
	if err != nil {
		t.Fatal(err)
	}
	turn1 := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 100_000, cost: ai.Cost{CacheWrite: 0.375, Total: 0.375},
	})
	missTurn := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 110_000, cost: ai.Cost{CacheWrite: 0.4125, Total: 0.4125},
		model: "other-model", timestamp: 120_000,
	})
	for _, message := range []*ai.AssistantMessage{turn1, missTurn} {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	rendered := renderSessionInfo(t, manager)
	// Per-model cost breakdown (two models used), sorted by cost descending.
	if !strings.Contains(rendered, "test/other-model: $0.412 (110k tokens)") ||
		!strings.Contains(rendered, "test/test-model: $0.375 (100k tokens)") {
		t.Fatalf("missing per-model breakdown:\n%s", rendered)
	}
	if strings.Index(rendered, "test/other-model:") > strings.Index(rendered, "test/test-model:") {
		t.Fatalf("per-model breakdown not sorted by cost desc:\n%s", rendered)
	}
	// Upstream "Cache Re-billed: $x (N tokens, M misses)" totals.
	if !strings.Contains(rendered, "Cache Re-billed: $0.345 (100,000 tokens, 1 miss)") {
		t.Fatalf("missing cache re-billed line:\n%s", rendered)
	}
	// Cached/uncached split under Input.
	if !strings.Contains(rendered, "Cached: 0 (0.0%)") ||
		!strings.Contains(rendered, "Uncached: 210,000 (210,000 written to cache)") {
		t.Fatalf("missing cached split:\n%s", rendered)
	}
}

func TestUsageUIIncludesAuxiliaryUsageAndLatestAssistantCacheHit(t *testing.T) {
	initTestTheme(t)
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	assistant := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		input: 50, cacheRead: 25, cacheWrite: 25, cost: ai.Cost{Total: 0.5},
	})
	assistant.Usage.Output = 0
	assistant.Usage.TotalTokens = 100
	root, err := manager.AppendMessage(assistant)
	if err != nil {
		t.Fatal(err)
	}
	toolUsage := &ai.Usage{Input: 90, Output: 10, TotalTokens: 100, Cost: ai.Cost{Total: 1}}
	if _, err := manager.AppendMessage(&ai.ToolResultMessage{ToolCallID: "tool-call-1", ToolName: "test_tool", Content: ai.ToolResultContent{}, Usage: toolUsage, Timestamp: 1}); err != nil {
		t.Fatal(err)
	}
	compactionUsage := &ai.Usage{Input: 80, Output: 20, TotalTokens: 100, Cost: ai.Cost{Total: 2}}
	if _, err := manager.AppendCompaction("summary", root, 100, sessionstore.OptionalEntryFields{Usage: compactionUsage}); err != nil {
		t.Fatal(err)
	}
	branchUsage := &ai.Usage{Input: 70, Output: 30, TotalTokens: 100, Cost: ai.Cost{Total: 3}}
	if _, err := manager.BranchWithSummary(nil, "branch summary", sessionstore.OptionalEntryFields{Usage: branchUsage}); err != nil {
		t.Fatal(err)
	}

	footer := NewFooterComponent(newCacheStatsRuntime(t, manager), &fakeFooterDataProvider{}, true)
	footerLine := normalizeWP450Lines(footer.Render(120))[1]
	for _, want := range []string{"↑290", "↓60", "R25", "W25", "CH25.0%", "$6.500"} {
		if !strings.Contains(footerLine, want) {
			t.Fatalf("footer = %q, missing %q", footerLine, want)
		}
	}

	rendered := renderSessionInfo(t, manager)
	if !strings.Contains(rendered, "Tools/summaries: $6.000 (300 tokens)") ||
		!strings.Contains(rendered, "test/test-model: $0.500 (100 tokens)") {
		t.Fatalf("missing reconciled usage breakdown:\n%s", rendered)
	}
	if strings.Index(rendered, "Tools/summaries:") > strings.Index(rendered, "test/test-model:") {
		t.Fatalf("usage breakdown not sorted by cost:\n%s", rendered)
	}
}

func TestFooterRenderCostDoesNotScaleWithSessionHistory(t *testing.T) {
	initTestTheme(t)
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	message := cacheStatsAssistant(t, cacheStatsAssistantOptions{input: 100})
	for range 1_000 {
		if _, err := manager.AppendMessage(message); err != nil {
			t.Fatal(err)
		}
	}
	footer := NewFooterComponent(newCacheStatsRuntime(t, manager), &fakeFooterDataProvider{}, true)
	allocations := testing.AllocsPerRun(1, func() { _ = footer.Render(120) })
	if allocations > 100 {
		t.Fatalf("footer render allocated %.0f objects for unchanged history, want at most 100", allocations)
	}
}
