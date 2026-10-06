package harness

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

// Verdict repro: upstream coding-agent treats an empty-summary branch_summary
// as invisible metadata — the walk-back pulls the cut back onto it and it is
// neither a cut point nor a turn start. orb previously returned {3 2 true}.
func TestFindCutPointTreatsEmptySummaryBranchSummaryAsInvisible(t *testing.T) {
	entries := []SessionEntry{
		{Type: "message", ID: "u", Timestamp: timestamp(1), Message: user("hello")},
		{Type: "message", ID: "a1", ParentID: new("u"), Timestamp: timestamp(2), Message: assistant("answer answer", 0)},
		{Type: "branch_summary", ID: "bs", ParentID: new("a1"), Timestamp: timestamp(3), FromID: "u", Summary: ""},
		{Type: "message", ID: "a2", ParentID: new("bs"), Timestamp: timestamp(4), Message: assistant("okokokok", 0)},
	}
	cut := FindCutPoint(entries, 0, len(entries), 2)
	if cut.FirstKeptEntryIndex != 2 || cut.TurnStartIndex != 0 || !cut.IsSplitTurn {
		t.Fatalf("cut = %#v, want {2 0 true}", cut)
	}
}

func hugeToolResult(chars int) *ai.ToolResultMessage {
	return &ai.ToolResultMessage{
		ToolCallID: "call", ToolName: "exec_code",
		Content: ai.ToolResultContent{&ai.TextContent{Text: strings.Repeat("x", chars)}}, Timestamp: 3,
	}
}

// A tool result larger than the recent budget used to push the cut back to the
// first boundary of the path: the compaction then summarized nothing and retained
// everything, and the caller re-ran it on every step. The cut now lands on the
// latest boundary before the oversized tail so the turn actually folds.
func TestHarnessCutPointFoldsTurnWhenNewestToolResultExceedsBudget(t *testing.T) {
	entries := linearEntries(
		user("request"), assistant("first step", 10), hugeToolResult(40_000),
		assistant("second step", 10), hugeToolResult(40_000),
	)
	cut := harnessFindCutPoint(entries, 0, len(entries), 5_000)
	if cut.FirstKeptEntryIndex != 3 || !cut.IsSplitTurn || cut.TurnStartIndex != 0 {
		t.Fatalf("cut = %#v", cut)
	}
	prepared, err := prepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 5_000}, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || len(prepared.TurnPrefixMessages) != 3 || len(prepared.RetainedTail) != 2 {
		t.Fatalf("preparation = %#v", prepared)
	}
}

func TestPrepareCompactionSkipsWhenNothingFolds(t *testing.T) {
	entries := linearEntries(user("request"), hugeToolResult(40_000))
	prepared, err := prepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 5_000}, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		t.Fatalf("expected no preparation, got %#v", prepared)
	}
}

// A custom status entry written between a tool call and its result is not a place to cut:
// the retained tail would start with a tool result whose call was summarized away.
func TestHarnessCutPointNeverSplitsToolCallFromResult(t *testing.T) {
	call := assistant("reading", 10)
	call.Content = append(call.Content, &ai.ToolCall{ID: "call", Name: "exec_code", Arguments: map[string]any{}})
	call.StopReason = ai.StopReasonToolUse
	entries := linearEntries(user("request"), call)
	entries = append(entries,
		SessionEntry{Type: "custom_message", ID: "status", ParentID: new("entry-1"), Timestamp: timestamp(3), CustomType: "status", Content: "reading the file", Display: true},
		SessionEntry{Type: "message", ID: "entry-3", ParentID: new("status"), Timestamp: timestamp(4), Message: hugeToolResult(40_000)},
	)
	cut := harnessFindCutPoint(entries, 0, len(entries), 5_000)
	if cut.FirstKeptEntryIndex != 1 {
		t.Fatalf("cut = %#v, want the assistant tool call kept with its result", cut)
	}
	prepared, err := prepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 5_000}, true)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || len(prepared.RetainedTail) != 3 || MessageRole(prepared.RetainedTail[0]) != "assistant" {
		t.Fatalf("preparation = %#v", prepared)
	}
}
