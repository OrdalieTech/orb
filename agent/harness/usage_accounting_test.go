package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func TestSummaryUsageFlowsThroughCompactionAndBranching(t *testing.T) {
	firstCache, secondCache := int64(5), int64(7)
	firstReasoning, secondReasoning := int64(6), int64(8)
	first := ai.Usage{
		Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, CacheWrite1h: &firstCache, Reasoning: &firstReasoning, TotalTokens: 21,
		Cost: ai.Cost{Input: 1, Output: 2, CacheRead: 3, CacheWrite: 4, Total: 10},
	}
	second := ai.Usage{
		Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, CacheWrite1h: &secondCache, Reasoning: &secondReasoning, TotalTokens: 115,
		Cost: ai.Cost{Input: 5, Output: 6, CacheRead: 7, CacheWrite: 8, Total: 26},
	}
	responses := []*ai.AssistantMessage{
		{Content: ai.AssistantContent{&ai.TextContent{Text: "history"}}, Usage: first, StopReason: ai.StopReasonStop},
		{Content: ai.AssistantContent{&ai.TextContent{Text: "prefix"}}, Usage: second, StopReason: ai.StopReasonStop},
	}
	complete := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		response := responses[0]
		responses = responses[1:]
		return response, nil
	}
	result, err := Compact(context.Background(), &CompactionPreparation{
		FirstKeptEntryID: "kept", MessagesToSummarize: agent.AgentMessages{user("history")},
		TurnPrefixMessages: agent.AgentMessages{user("prefix")}, IsSplitTurn: true,
		FileOps: newFileOperations(), Settings: CompactionSettings{ReserveTokens: 100},
	}, &ai.Model{MaxTokens: 100}, complete, "", ai.ModelThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage == nil || result.Usage.Input != 11 || result.Usage.Output != 22 || result.Usage.CacheRead != 33 || result.Usage.CacheWrite != 44 ||
		result.Usage.CacheWrite1h == nil || *result.Usage.CacheWrite1h != 12 || result.Usage.Reasoning == nil || *result.Usage.Reasoning != 14 ||
		result.Usage.TotalTokens != 136 || result.Usage.Cost != (ai.Cost{Input: 6, Output: 8, CacheRead: 10, CacheWrite: 12, Total: 36}) {
		t.Fatalf("combined usage = %#v", result.Usage)
	}
	fromHook := true
	encoded, err := marshalHarnessEntry(SessionTreeEntry{
		Type: "compaction", ID: "compact", Timestamp: "2026-07-20T00:00:00Z", Summary: "summary", FirstKeptEntryID: "kept", TokensBefore: 100,
		Details: json.RawMessage(`{"source":"test"}`), Usage: result.Usage, FromHook: &fromHook,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"type":"compaction","id":"compact","parentId":null,"timestamp":"2026-07-20T00:00:00Z","summary":"summary","firstKeptEntryId":"kept","tokensBefore":100,"details":{"source":"test"},"usage":{"input":11,"output":22,"cacheRead":33,"cacheWrite":44,"cacheWrite1h":12,"reasoning":14,"totalTokens":136,"cost":{"input":6,"output":8,"cacheRead":10,"cacheWrite":12,"total":36}},"fromHook":true}`)
	if !bytes.Equal(encoded, want) {
		t.Fatalf("entry = %s\nwant  = %s", encoded, want)
	}

	branchResponse := &ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "branch"}}, Usage: first, StopReason: ai.StopReasonStop}
	branch, err := GenerateBranchSummary(context.Background(), linearEntries(user("work")), GenerateBranchSummaryOptions{
		Model: &ai.Model{ContextWindow: 1000},
		Complete: func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
			return branchResponse, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if branch.Usage == nil || branch.Usage.TotalTokens != first.TotalTokens {
		t.Fatalf("branch usage = %#v", branch.Usage)
	}
}
