package modes

import (
	"math"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/config"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/tui"
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

func cacheStatsEntry(t *testing.T, id string, message *ai.AssistantMessage) sessionstore.SessionEntry {
	t.Helper()
	encoded, err := ai.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return sessionstore.SessionEntry{Type: "message", ID: id, Message: encoded}
}

func newCacheStatsRuntime(t *testing.T, manager *sessionstore.SessionManager) *codingagent.SessionRuntime {
	t.Helper()
	settings, err := config.NewSettingsManager(manager.GetCWD(), config.WithAgentDir(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := codingagent.NewSessionRuntime(codingagent.SessionRuntimeConfig{
		Agent: agent.NewAgent(), SessionManager: manager, Settings: settings,
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

// Turn 1: fresh 100k cache write at $3.75/M.
func cacheStatsTurn1(t *testing.T) sessionstore.SessionEntry {
	return cacheStatsEntry(t, "turn1", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 100_000, cost: ai.Cost{CacheWrite: 0.375}, timestamp: 0,
	}))
}

// Turn 2: healthy, everything read back at $0.30/M.
func cacheStatsTurn2(t *testing.T) sessionstore.SessionEntry {
	return cacheStatsEntry(t, "turn2", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheRead: 100_000, cacheWrite: 5_000,
		cost: ai.Cost{CacheRead: 0.03, CacheWrite: 0.019}, timestamp: 60_000,
	}))
}

func TestComputeCacheWasteAccumulatesMissedTokensAndCost(t *testing.T) {
	// Turn 3: full miss, previous 105k prompt re-billed at $3.75/M write.
	turn3 := cacheStatsEntry(t, "turn3", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 110_000, cost: ai.Cost{CacheWrite: 0.4125}, timestamp: 120_000,
	}))
	totals := computeCacheWaste([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t), turn3}, cacheStatsModels)
	if totals.missedTokens != 105_000 {
		t.Fatalf("missedTokens = %d", totals.missedTokens)
	}
	// 105k at ($3.75 - $0.30)/M.
	if math.Abs(totals.missedCost-0.36225) > 1e-5 {
		t.Fatalf("missedCost = %v", totals.missedCost)
	}
}

func TestComputeCacheWasteCountsNothingForHealthySessions(t *testing.T) {
	totals := computeCacheWaste([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t)}, cacheStatsModels)
	if totals.missedTokens != 0 || totals.missedCost != 0 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestComputeCacheWasteSkipsTurnAfterCompactionReset(t *testing.T) {
	reset := sessionstore.SessionEntry{Type: "compaction", ID: "c"}
	afterReset := cacheStatsEntry(t, "after", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 20_000, cost: ai.Cost{CacheWrite: 0.075},
	}))
	totals := computeCacheWaste([]sessionstore.SessionEntry{cacheStatsTurn1(t), reset, afterReset}, cacheStatsModels)
	if totals.missedTokens != 0 {
		t.Fatalf("missedTokens = %d", totals.missedTokens)
	}
}

func TestComputeCacheWasteCountsModelSwitchMisses(t *testing.T) {
	otherModel := cacheStatsEntry(t, "other", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 100_000, cost: ai.Cost{CacheWrite: 0.375}, model: "other-model",
	}))
	totals := computeCacheWaste([]sessionstore.SessionEntry{cacheStatsTurn1(t), otherModel}, cacheStatsModels)
	if totals.missedTokens != 100_000 || totals.missCount != 1 {
		t.Fatalf("totals = %+v", totals)
	}
}

func TestComputeCacheWasteSkipsProvidersWithoutCacheActivity(t *testing.T) {
	a := cacheStatsEntry(t, "a", cacheStatsAssistant(t, cacheStatsAssistantOptions{input: 100_000}))
	b := cacheStatsEntry(t, "b", cacheStatsAssistant(t, cacheStatsAssistantOptions{input: 110_000}))
	totals := computeCacheWaste([]sessionstore.SessionEntry{a, b}, cacheStatsModels)
	if totals.missedTokens != 0 {
		t.Fatalf("missedTokens = %d", totals.missedTokens)
	}
}

func TestCollectCacheMissesMapsCountedMissesToEntries(t *testing.T) {
	missTurn := cacheStatsEntry(t, "miss-turn", cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 110_000, cost: ai.Cost{CacheWrite: 0.4125}, timestamp: 120_000,
	}))
	misses := collectCacheMisses([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t), missTurn}, cacheStatsModels)
	if len(misses) != 1 {
		t.Fatalf("misses = %#v", misses)
	}
	if miss := misses["miss-turn"]; miss == nil || miss.tokens != 105_000 {
		t.Fatalf("miss = %+v", miss)
	}
}

// detectCacheMissFromEntries mirrors upstream detectCacheMiss for a message
// not yet contained in entries (message_end fires before persistence).
func detectCacheMissFromEntries(entries []sessionstore.SessionEntry, message *ai.AssistantMessage, models []ai.Model) *cacheMiss {
	return computeCacheMiss(scanCacheEntries(entries, nil), message, models)
}

func TestDetectCacheMissOnJustCompletedMessageWithIdleTime(t *testing.T) {
	missMessage := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 110_000, cost: ai.Cost{CacheWrite: 0.4125}, timestamp: 600_000,
	})
	miss := detectCacheMissFromEntries([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t)}, missMessage, cacheStatsModels)
	if miss == nil {
		t.Fatal("expected a miss")
	}
	if miss.tokens != 105_000 {
		t.Fatalf("missedTokens = %d", miss.tokens)
	}
	if math.Abs(miss.cost-0.36225) > 1e-5 {
		t.Fatalf("missedCost = %v", miss.cost)
	}
	// 600s - 60s since the previous request.
	if miss.idle != 540_000 {
		t.Fatalf("idleMs = %d", miss.idle)
	}
	if miss.modelChanged {
		t.Fatal("modelChanged = true")
	}
}

func TestDetectCacheMissFlagsModelSwitches(t *testing.T) {
	otherModel := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheWrite: 110_000, cost: ai.Cost{CacheWrite: 0.4125}, model: "other-model", timestamp: 120_000,
	})
	miss := detectCacheMissFromEntries([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t)}, otherModel, cacheStatsModels)
	if miss == nil || miss.tokens != 105_000 || !miss.modelChanged {
		t.Fatalf("miss = %+v", miss)
	}
}

func TestDetectCacheMissReturnsNilForHealthyTurns(t *testing.T) {
	healthy := cacheStatsAssistant(t, cacheStatsAssistantOptions{
		cacheRead: 105_000, cacheWrite: 2_000,
		cost: ai.Cost{CacheRead: 0.0315, CacheWrite: 0.0075}, timestamp: 120_000,
	})
	if miss := detectCacheMissFromEntries([]sessionstore.SessionEntry{cacheStatsTurn1(t), cacheStatsTurn2(t)}, healthy, cacheStatsModels); miss != nil {
		t.Fatalf("miss = %+v", miss)
	}
}

func TestDetectCacheMissReturnsNilForFirstTurn(t *testing.T) {
	first := cacheStatsAssistant(t, cacheStatsAssistantOptions{cacheWrite: 100_000, cost: ai.Cost{CacheWrite: 0.375}})
	if miss := detectCacheMissFromEntries(nil, first, cacheStatsModels); miss != nil {
		t.Fatalf("miss = %+v", miss)
	}
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

	footer := NewFooterComponent(newCacheStatsRuntime(t, manager), &fakeFooterDataProvider{})
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

func TestHandleSessionCommandOmitsSingleUsageBreakdown(t *testing.T) {
	initTestTheme(t)
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(cacheStatsAssistant(t, cacheStatsAssistantOptions{
		input: 100, cost: ai.Cost{Total: 0.5},
	})); err != nil {
		t.Fatal(err)
	}
	rendered := renderSessionInfo(t, manager)
	if strings.Contains(rendered, "test/test-model:") {
		t.Fatalf("single-entry breakdown should stay hidden:\n%s", rendered)
	}
}
