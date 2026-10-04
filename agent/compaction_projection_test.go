package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
)

// Ported from pi 1.0's session-context-edit compaction tests.

func projectionSession(t *testing.T) *sessionstore.SessionManager {
	t.Helper()
	manager, err := sessionstore.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

// mustOf fails the test on an append error and returns the entry id.
func mustOf(t *testing.T) func(string, error) string {
	return func(id string, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
}

func usageAssistant(text string, usage ai.Usage) *ai.AssistantMessage {
	return &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: text}}, API: "faux", Provider: "faux", Model: "faux-1", Usage: usage, StopReason: ai.StopReasonStop, Timestamp: 1}
}

func prepareProjected(t *testing.T, manager *sessionstore.SessionManager) *harness.CompactionPreparation {
	t.Helper()
	preparation, err := harness.PrepareLegacyCompaction(projectSessionEntries(manager.GetBranch()), harness.CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	return preparation
}

func projectedEstimate(manager *sessionstore.SessionManager) harness.ContextUsageEstimate {
	return harness.EstimateProjectedContextTokens(projectSessionEntries(manager.GetBranch()))
}

func TestCompactionExcludesSystemHistoryAndPreservesPromptState(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	must(manager.AppendMessage(&ai.SystemMessage{Content: "initial instructions", ToolsAdded: []ai.Tool{{Name: "read"}}}))
	must(manager.AppendMessage(userMessage("repair the evaluator")))
	must(manager.AppendMessage(&ai.SystemMessage{Content: "additional instructions", ToolsRemoved: []ai.ToolReference{{Name: "read"}}, ToolsAdded: []ai.Tool{{Name: "bash"}}}))
	must(manager.AppendMessage(usageAssistant(strings.Repeat("probe findings ", 100), ai.Usage{TotalTokens: 1000})))
	latest := must(manager.AppendMessage(usageAssistant("latest step", ai.Usage{TotalTokens: 10})))
	preparation := prepareProjected(t, manager)
	if preparation == nil || !preparation.IsSplitTurn || preparation.FirstKeptEntryID != latest ||
		len(preparation.MessagesToSummarize) != 0 || len(preparation.TurnPrefixMessages) != 2 {
		t.Fatalf("preparation = %#v", preparation)
	}
	must(manager.AppendCompaction("checkpoint", preparation.FirstKeptEntryID, preparation.TokensBefore))
	var messages engine.AgentMessages
	for _, raw := range manager.BuildSessionContext().Messages {
		messages = append(messages, decodeSessionMessage(raw))
	}
	replay, err := ConvertToLLM(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}
	if prompt := ai.CurrentSystemPrompt(replay); prompt != "initial instructions\n\nadditional instructions" {
		t.Fatalf("replayed prompt = %q", prompt)
	}
	if tools := ai.CurrentTools(replay); len(tools) != 1 || tools[0].Name != "bash" {
		t.Fatalf("replayed tools = %#v", tools)
	}
}

func TestProjectedEstimateDoesNotTrustPreEditUsage(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	large := must(manager.AppendMessage(userMessage(strings.Repeat("large ", 2000))))
	answer := must(manager.AppendMessage(usageAssistant("small answer", ai.Usage{Input: 10_000, TotalTokens: 10_001})))
	must(manager.AppendContextEdit(large, nil))
	if estimate := projectedEstimate(manager); estimate.UsageTokens != 0 || estimate.Tokens >= 100 {
		t.Fatalf("edited estimate = %#v", estimate)
	}
	must(manager.AppendContextEdit(answer, nil))
	if estimate := projectedEstimate(manager); estimate.Tokens != 0 {
		t.Fatalf("empty estimate = %#v", estimate)
	}
}

func TestProjectedEstimateUsesUsageAfterTheLatestEdit(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	user := must(manager.AppendMessage(userMessage("original")))
	must(manager.AppendContextEdit(user, json.RawMessage(`{"content":"edited"}`)))
	must(manager.AppendMessage(usageAssistant("answer", ai.Usage{Input: 4_000, Output: 100, TotalTokens: 4_100})))
	must(manager.AppendMessage(userMessage("next")))
	if estimate := projectedEstimate(manager); estimate.UsageTokens != 4_100 || estimate.TrailingTokens != 1 || estimate.Tokens != 4_101 {
		t.Fatalf("estimate = %#v", estimate)
	}
	must(manager.AppendCompaction("small summary", user, 50_001))
	if estimate := projectedEstimate(manager); estimate.UsageTokens != 0 || estimate.Tokens >= 100 {
		t.Fatalf("post-compaction estimate = %#v", estimate)
	}
}

func TestCompactionDoesNotAdvancePastAReplacedBoundaryInput(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	must(manager.AppendMessage(userMessage("old request")))
	must(manager.AppendMessage(usageAssistant("old answer", ai.Usage{})))
	replaced := must(manager.AppendMessage(userMessage("original input")))
	answer := must(manager.AppendMessage(usageAssistant("answered original input", ai.Usage{})))
	must(manager.AppendContextEdit(replaced, json.RawMessage(`{"content":"`+strings.Repeat("NEW-INSTRUCTION ", 100)+`"}`)))
	must(manager.AppendContextEdit(answer, nil))
	must(manager.AppendCustomEntry("bookkeeping", map[string]any{"source": "test"}))
	preparation := prepareProjected(t, manager)
	if preparation == nil || preparation.FirstKeptEntryID != replaced {
		t.Fatalf("preparation = %#v", preparation)
	}
	if summarized, _ := json.Marshal(append(preparation.MessagesToSummarize, preparation.TurnPrefixMessages...)); strings.Contains(string(summarized), "NEW-INSTRUCTION") {
		t.Fatal("the replacement was summarized")
	}
}

func TestCompactionMetadataDoesNotMoveTheCutPastUnsentInput(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	must(manager.AppendMessage(userMessage("old request")))
	must(manager.AppendMessage(usageAssistant("old answer", ai.Usage{})))
	instruction := must(manager.AppendCustomMessageEntry("next-work", strings.Repeat("UNSENT-INSTRUCTION ", 100), false))
	must(manager.AppendCustomEntry("bookkeeping", map[string]any{"source": "test"}))
	if preparation := prepareProjected(t, manager); preparation == nil || preparation.FirstKeptEntryID != instruction {
		t.Fatalf("preparation = %#v", preparation)
	}
}

func TestCompactionDoesNotTreatAnOmittedCustomMessageAsARecoveryAttempt(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	must(manager.AppendMessage(userMessage(strings.Repeat("unanswered input ", 100))))
	custom := must(manager.AppendCustomMessageEntry("temporary", "temporary context", false))
	must(manager.AppendContextEdit(custom, nil))
	if preparation := prepareProjected(t, manager); preparation != nil {
		t.Fatalf("preparation = %#v", preparation)
	}
}

func TestCompactionAdvancesPastInputForAnOmittedRecoveryAttempt(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	must(manager.AppendMessage(userMessage(strings.Repeat("recovery input ", 100))))
	attempt := must(manager.AppendMessage(usageAssistant("failed attempt", ai.Usage{})))
	must(manager.AppendContextEdit(attempt, nil))
	must(manager.AppendCustomEntry("bookkeeping", map[string]any{"source": "test"}))
	preparation := prepareProjected(t, manager)
	if preparation == nil || preparation.FirstKeptEntryID != attempt || len(preparation.TurnPrefixMessages) != 1 || len(preparation.MessagesToSummarize) != 0 {
		t.Fatalf("preparation = %#v", preparation)
	}
	if prefix, _ := json.Marshal(preparation.TurnPrefixMessages); !strings.Contains(string(prefix), `"role":"user"`) || !strings.Contains(string(prefix), "recovery input") {
		t.Fatalf("turn prefix = %s", prefix)
	}
}

func TestCompactionSummarizesEditedContent(t *testing.T) {
	manager, must := projectionSession(t), mustOf(t)
	omitted := must(manager.AppendMessage(userMessage(strings.Repeat("OMIT-ME ", 100))))
	must(manager.AppendMessage(usageAssistant(strings.Repeat("old answer ", 100), ai.Usage{})))
	must(manager.AppendContextEdit(omitted, nil))
	must(manager.AppendMessage(userMessage("keep")))
	must(manager.AppendMessage(usageAssistant("suffix", ai.Usage{})))
	preparation := prepareProjected(t, manager)
	if preparation == nil {
		t.Fatal("nothing to compact")
	}
	if summarized, _ := json.Marshal(append(preparation.MessagesToSummarize, preparation.TurnPrefixMessages...)); strings.Contains(string(summarized), "OMIT-ME") {
		t.Fatal("omitted content was summarized")
	}
}
