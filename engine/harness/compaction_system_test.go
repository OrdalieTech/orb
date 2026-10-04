package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestCompactionSystemOnlyHistoryUsesOnlyTurnPrefixSummary(t *testing.T) {
	for _, system := range []struct {
		name    string
		message engine.AgentMessage
	}{
		{"pointer", &ai.SystemMessage{Content: "system instructions"}},
		{"value", ai.SystemMessage{Content: "system instructions"}},
		{"json", json.RawMessage(`{"role":"system","content":"system instructions","timestamp":0}`)},
	} {
		for _, retainTail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/retained-tail=%t", system.name, retainTail), func(t *testing.T) {
				entries := linearEntries(
					system.message, user("repair the evaluator"), system.message,
					assistant(strings.Repeat("probe findings ", 100), 1000), assistant("latest step", 10),
				)
				prepared, err := prepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 2}, retainTail)
				if err != nil || prepared == nil || !prepared.IsSplitTurn {
					t.Fatalf("preparation = %#v, %v", prepared, err)
				}
				if conversation := SerializeConversation(prepared.MessagesToSummarize); conversation != "" {
					t.Fatalf("history conversation = %q", conversation)
				}
				var prompts []string
				response := assistant("prefix checkpoint", 11)
				complete := func(_ context.Context, _ *ai.Model, request ai.Context, _ *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
					prompts = append(prompts, userMessageTextForTest(request.Messages[0]))
					return response, nil
				}
				result, err := CompactProduct(context.Background(), prepared, &ai.Model{MaxTokens: 1000}, complete, "", ai.ModelThinkingOff, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(prompts) != 1 {
					t.Fatalf("summary calls = %d, want only the turn-prefix call: %#v", len(prompts), prompts)
				}
				if !strings.HasPrefix(prompts[0], "# Conversation\n[User]: repair the evaluator") || !strings.Contains(prompts[0], "probe findings") || strings.Contains(prompts[0], "system instructions") {
					t.Fatalf("turn-prefix prompt = %q", prompts[0])
				}
				if len(prepared.MessagesToSummarize) != 0 || len(prepared.TurnPrefixMessages) != 2 {
					t.Fatalf("summary partitions = %d/%d, want 0/2", len(prepared.MessagesToSummarize), len(prepared.TurnPrefixMessages))
				}
				want := "No prior history.\n\n---\n\n**Turn Context (split turn):**\n\nprefix checkpoint"
				if result.Summary != want || result.Usage == nil || !reflect.DeepEqual(*result.Usage, response.Usage) {
					t.Fatalf("result = %#v, want %q and prefix-only usage", result, want)
				}
				if messageRole(entries[0].Message) != "system" || messageRole(entries[2].Message) != "system" {
					t.Fatal("compaction preparation mutated persisted prompt state")
				}
			})
		}
	}
}

func TestPrepareCompactionSkipsSystemOnlyDiscardedSpan(t *testing.T) {
	for _, retainTail := range []bool{false, true} {
		t.Run(fmt.Sprintf("retained-tail=%t", retainTail), func(t *testing.T) {
			entries := linearEntries(&ai.SystemMessage{Content: "system instructions"}, user("request"), assistant("answer", 10))
			prepared, err := prepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 20_000}, retainTail)
			if err != nil || prepared != nil {
				t.Fatalf("preparation = %#v, %v, want nothing to compact", prepared, err)
			}
		})
	}
}
