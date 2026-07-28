package harness

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

// Upstream ships two divergent findCutPoint algorithms at the pinned commit:
// packages/agent/src/harness/compaction/compaction.ts (drives the harness
// prepareCompaction mirrored by PrepareCompaction/PrepareTreeCompaction) and
// packages/coding-agent/src/core/compaction/compaction.ts (mirrored by the
// exported FindCutPoint and PrepareLegacyCompaction). The branch_summary
// weighting below is a case where the two disagree.
func TestHarnessCutPointDivergesFromCodingAgentOnBranchSummaryWeight(t *testing.T) {
	entries := []SessionEntry{
		{Type: "message", ID: "u", Timestamp: timestamp(1), Message: user("hello")},
		{Type: "message", ID: "a1", ParentID: ptr("u"), Timestamp: timestamp(2), Message: assistant("answer", 0)},
		{Type: "branch_summary", ID: "bs", ParentID: ptr("a1"), Timestamp: timestamp(3), FromID: "u", Summary: strings.Repeat("s", 4000)},
		{Type: "message", ID: "a2", ParentID: ptr("bs"), Timestamp: timestamp(4), Message: assistant("ok", 0)},
	}
	codingCut := FindCutPoint(entries, 0, len(entries), 2)
	if codingCut.FirstKeptEntryIndex != 2 || codingCut.TurnStartIndex != -1 || codingCut.IsSplitTurn {
		t.Fatalf("coding-agent cut = %#v, want {2 -1 false}", codingCut)
	}
	harnessCut := harnessFindCutPoint(entries, 0, len(entries), 2)
	if harnessCut.FirstKeptEntryIndex != 1 || harnessCut.TurnStartIndex != 0 || !harnessCut.IsSplitTurn {
		t.Fatalf("harness cut = %#v, want {1 0 true}", harnessCut)
	}
	settings := CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 2}
	prepared, err := PrepareCompaction(entries, settings)
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || prepared.FirstKeptEntryID != "a1" || !prepared.IsSplitTurn {
		t.Fatalf("harness preparation = %#v, want firstKept a1 split turn", prepared)
	}
	if len(prepared.MessagesToSummarize) != 0 || len(prepared.TurnPrefixMessages) != 1 || len(prepared.RetainedTail) != 3 {
		t.Fatalf("harness partitions = %d/%d/%d, want 0/1/3",
			len(prepared.MessagesToSummarize), len(prepared.TurnPrefixMessages), len(prepared.RetainedTail))
	}
	legacy, err := PrepareLegacyCompaction(entries, settings)
	if err != nil {
		t.Fatal(err)
	}
	if legacy == nil || legacy.FirstKeptEntryID != "bs" || legacy.IsSplitTurn {
		t.Fatalf("legacy preparation = %#v, want firstKept bs whole turn", legacy)
	}
}

// Verdict repro: upstream coding-agent treats an empty-summary branch_summary
// as invisible metadata — the walk-back pulls the cut back onto it and it is
// neither a cut point nor a turn start. pigo previously returned {3 2 true}.
func TestFindCutPointTreatsEmptySummaryBranchSummaryAsInvisible(t *testing.T) {
	entries := []SessionEntry{
		{Type: "message", ID: "u", Timestamp: timestamp(1), Message: user("hello")},
		{Type: "message", ID: "a1", ParentID: ptr("u"), Timestamp: timestamp(2), Message: assistant("answer answer", 0)},
		{Type: "branch_summary", ID: "bs", ParentID: ptr("a1"), Timestamp: timestamp(3), FromID: "u", Summary: ""},
		{Type: "message", ID: "a2", ParentID: ptr("bs"), Timestamp: timestamp(4), Message: assistant("okokokok", 0)},
	}
	cut := FindCutPoint(entries, 0, len(entries), 2)
	if cut.FirstKeptEntryIndex != 2 || cut.TurnStartIndex != 0 || !cut.IsSplitTurn {
		t.Fatalf("cut = %#v, want {2 0 true}", cut)
	}
}

// Wire emission goes through ai.Marshal; MarshalJSON must not HTML-escape the
// summary the way a nested plain json.Marshal did (JSON.stringify parity).
func TestCompactionResultMarshalDoesNotHTMLEscape(t *testing.T) {
	result := CompactionResult{Summary: "a<b&c>d", TokensBefore: 1}
	want := `{"summary":"a<b&c>d","tokensBefore":1}`
	wire, err := ai.Marshal(result)
	if err != nil || string(wire) != want {
		t.Fatalf("ai.Marshal = %s, %v, want %s", wire, err, want)
	}
	wrapped, err := ai.Marshal(struct {
		Result CompactionResult `json:"result"`
	}{Result: result})
	if err != nil || string(wrapped) != `{"result":`+want+`}` {
		t.Fatalf("nested ai.Marshal = %s, %v", wrapped, err)
	}
}
