package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
)

func TestPrepareTreeCompaction(t *testing.T) {
	entries := []SessionTreeEntry{
		{Type: "message", ID: "old-user", Timestamp: timestamp(1), Message: json.RawMessage(`{"role":"user","content":"old request that is long enough to summarize","timestamp":1}`)},
		{Type: "message", ID: "old-assistant", ParentID: new("old-user"), Timestamp: timestamp(2), Message: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"old answer that is long enough to summarize"}],"api":"x","provider":"x","model":"x","usage":{"input":30,"output":30,"cacheRead":0,"cacheWrite":0,"totalTokens":60,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":2}`)},
		{Type: "message", ID: "recent-user", ParentID: new("old-assistant"), Timestamp: timestamp(3), Message: json.RawMessage(`{"role":"user","content":"recent request","timestamp":3}`)},
		{Type: "message", ID: "recent-assistant", ParentID: new("recent-user"), Timestamp: timestamp(4), Message: json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"recent answer"}],"api":"x","provider":"x","model":"x","usage":{"input":50,"output":50,"cacheRead":0,"cacheWrite":0,"totalTokens":100,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":4}`)},
	}

	prepared, err := PrepareTreeCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || len(prepared.MessagesToSummarize) != 2 || len(prepared.RetainedTail) != 2 {
		t.Fatalf("preparation = %#v", prepared)
	}
}

// On a second compaction the previous compaction entry sits inside the scanned
// range (firstKeptEntryId points before it), and upstream weighs it through
// sessionEntryToContextMessages. Expectations come from running upstream
// findCutPoint (coding-agent compaction.ts:403) on the same entries.
func TestFindCutPointWeighsPreviousCompactionEntry(t *testing.T) {
	entries := linearEntries(
		user("old request "+strings.Repeat("o", 60)),
		assistant("old answer "+strings.Repeat("p", 60), 0),
		user("kept request "+strings.Repeat("k", 60)),
		assistant("kept answer "+strings.Repeat("q", 60), 0),
	)
	entries = append(entries,
		SessionEntry{
			Type: "compaction", ID: "compact-1", ParentID: new("entry-3"), Timestamp: timestamp(5),
			Summary: strings.Repeat("S", 400), FirstKeptEntryID: "entry-2", TokensBefore: 900,
		},
		SessionEntry{Type: "message", ID: "entry-5", ParentID: new("compact-1"), Timestamp: timestamp(6), Message: user("new request " + strings.Repeat("n", 60))},
		SessionEntry{Type: "message", ID: "entry-6", ParentID: new("entry-5"), Timestamp: timestamp(7), Message: assistant("new answer "+strings.Repeat("m", 60), 0)},
	)
	for _, testCase := range []struct {
		keepRecentTokens int64
		want             CutPointResult
	}{
		{keepRecentTokens: 40, want: CutPointResult{FirstKeptEntryIndex: 5, TurnStartIndex: -1}},
		{keepRecentTokens: 140, want: CutPointResult{FirstKeptEntryIndex: 3, TurnStartIndex: 2, IsSplitTurn: true}},
	} {
		if got := FindCutPoint(entries, 2, len(entries), testCase.keepRecentTokens); got != testCase.want {
			t.Fatalf("keepRecentTokens=%d cut = %#v, want %#v", testCase.keepRecentTokens, got, testCase.want)
		}
	}
}

func TestV081CompactPropagatesRetainedTail(t *testing.T) {
	tail := engine.AgentMessages{user("retained")}
	preparation := &CompactionPreparation{
		FirstKeptEntryID: "kept", MessagesToSummarize: engine.AgentMessages{user("old")}, RetainedTail: tail,
		TokensBefore: 20, FileOps: newFileOperations(), Settings: CompactionSettings{ReserveTokens: 100},
	}
	complete := func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		return assistant("summary", 3), nil
	}
	result, err := Compact(context.Background(), preparation, &ai.Model{MaxTokens: 100}, complete, "", ai.ModelThinkingOff)
	if err != nil {
		t.Fatal(err)
	}
	if result.FirstKeptEntryID != "kept" || len(result.RetainedTail) != 1 || MessageRole(result.RetainedTail[0]) != "user" {
		t.Fatalf("compaction result = %#v", result)
	}
	tail[0] = assistant("mutated", 1)
	if MessageRole(result.RetainedTail[0]) != "assistant" {
		t.Fatal("result did not retain the upstream preparation slice")
	}
}

func TestPrepareCompactionCarriesPreviousSummaryAndFileDetails(t *testing.T) {
	call := &ai.ToolCall{ID: "call", Name: "write", Arguments: map[string]any{"path": "new.go"}}
	entries := linearEntries(user("old"), &ai.AssistantMessage{Content: ai.AssistantContent{call}, StopReason: ai.StopReasonStop, Usage: usage(20)})
	fromHook := false
	entries = append(entries, SessionEntry{
		Type: "compaction", ID: "compact", ParentID: new(entries[len(entries)-1].ID), Timestamp: timestamp(3),
		Summary: "old summary", TokensBefore: 20,
		RetainedTail: engine.AgentMessages{&ai.AssistantMessage{Content: ai.AssistantContent{call}, StopReason: ai.StopReasonStop, Usage: usage(20)}},
		Details:      CompactionDetails{ReadFiles: []string{"old-read.go"}, ModifiedFiles: []string{"old-edit.go"}}, FromHook: fromHook,
	})
	entries = append(entries, SessionEntry{Type: "message", ID: "entry-3", ParentID: new("compact"), Timestamp: timestamp(4), Message: user("latest request")})
	entries = append(entries, SessionEntry{Type: "message", ID: "entry-4", ParentID: new("entry-3"), Timestamp: timestamp(5), Message: assistant(strings.Repeat("answer ", 30), 80)})
	prepared, err := PrepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1})
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || prepared.PreviousSummary == nil || *prepared.PreviousSummary != "old summary" {
		t.Fatalf("preparation = %#v", prepared)
	}
	if _, ok := prepared.FileOps.Read["old-read.go"]; !ok {
		t.Fatal("previous read details missing")
	}
	if _, ok := prepared.FileOps.Edited["old-edit.go"]; !ok {
		t.Fatal("previous edit details missing")
	}
	if _, ok := prepared.FileOps.Written["new.go"]; !ok {
		t.Fatal("tool operation missing")
	}
}

func TestPrepareLegacyCompactionReportsNothingToCompactForUnmigratedEntry(t *testing.T) {
	entries := linearEntries(user("old request"), assistant(strings.Repeat("old answer ", 30), 80), user("recent"))
	entries[2].ID = ""
	prepared, err := PrepareLegacyCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1})
	if err != nil || prepared != nil {
		t.Fatalf("prepared = %#v, err = %v, want nil, nil", prepared, err)
	}
	// Harness >=0.84 no longer resolves entry ids and accepts unmigrated entries.
	if harnessPrepared, err := PrepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 1}); err != nil || harnessPrepared == nil {
		t.Fatalf("harness prepared = %#v, err = %v", harnessPrepared, err)
	}
}

func TestSummaryRequestsUseFreshSessionsWithoutCacheRetention(t *testing.T) {
	model := &ai.Model{MaxTokens: 128, ContextWindow: 1000}
	var seen []ai.SimpleStreamOptions
	complete := func(_ context.Context, _ *ai.Model, _ ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		seen = append(seen, *options)
		return assistant("summary", 10), nil
	}
	for range 2 {
		if _, err := GenerateSummary(context.Background(), engine.AgentMessages{user("work")}, model, complete, 1000, "", nil, ai.ModelThinkingOff); err != nil {
			t.Fatal(err)
		}
	}
	reserveTokens := int64(100)
	if _, err := GenerateBranchSummary(context.Background(), linearEntries(user("branch")), GenerateBranchSummaryOptions{
		Model: model, Complete: complete, ReserveTokens: &reserveTokens,
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 {
		t.Fatalf("summary requests = %d, want 3", len(seen))
	}
	sessionIDs := make(map[string]struct{}, len(seen))
	for _, options := range seen {
		if options.CacheRetention == nil || *options.CacheRetention != ai.CacheRetentionNone {
			t.Fatalf("cache retention = %#v", options.CacheRetention)
		}
		if options.SessionID == nil || len(*options.SessionID) != 36 || (*options.SessionID)[14] != '7' {
			t.Fatalf("session ID = %#v, want UUIDv7", options.SessionID)
		}
		sessionIDs[*options.SessionID] = struct{}{}
	}
	if len(sessionIDs) != len(seen) {
		t.Fatalf("summary session IDs were reused: %#v", seen)
	}
}

// Regression: both upstream branch-summarization getMessageFromEntry variants
// project a branch_summary unconditionally, so an empty summary still renders
// its wrapper text instead of being dropped from the summarization prompt.
func TestPrepareBranchEntriesProjectsEmptySummaryBranchSummary(t *testing.T) {
	entries := linearEntries(user("branch request"))
	entries = append(entries, SessionEntry{
		Type: "branch_summary", ID: "entry-branch", ParentID: new(entries[0].ID),
		Timestamp: timestamp(2), Summary: "", FromID: "entry-0",
	})
	prepared := PrepareBranchEntries(entries, 0)
	if len(prepared.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (empty-summary branch_summary must be projected)", len(prepared.Messages))
	}
	serialized := SerializeConversation(prepared.Messages)
	want := "[User]: branch request\n\n[User]: " + branchSummaryPrefix + branchSummarySuffix
	if serialized != want {
		t.Fatalf("serialized = %q, want %q", serialized, want)
	}
}

func user(text string) *ai.UserMessage {
	return &ai.UserMessage{Content: ai.NewUserContent(&ai.TextContent{Text: text}), Timestamp: 1}
}

func assistant(text string, tokens int64) *ai.AssistantMessage {
	return &ai.AssistantMessage{
		Content: ai.AssistantContent{&ai.TextContent{Text: text}}, API: "faux", Provider: "faux", Model: "faux-1",
		Usage: usage(tokens), StopReason: ai.StopReasonStop, Timestamp: 2,
	}
}

func usage(tokens int64) ai.Usage {
	return ai.Usage{Input: tokens, TotalTokens: tokens, Cost: ai.Cost{}}
}

func linearEntries(messages ...engine.AgentMessage) []SessionEntry {
	entries := make([]SessionEntry, 0, len(messages))
	var parent *string
	for index, message := range messages {
		id := "entry-" + string(rune('0'+index))
		entries = append(entries, SessionEntry{Type: "message", ID: id, ParentID: parent, Timestamp: timestamp(index + 1), Message: message})
		parent = new(id)
	}
	return entries
}

func timestamp(second int) string { return "2025-01-01T00:00:" + fmtTwoDigits(second) + ".000Z" }
func fmtTwoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

func userMessageTextForTest(message ai.Message) string {
	userMessage, _ := message.(*ai.UserMessage)
	if userMessage == nil {
		return ""
	}
	if userMessage.Content.Text != nil {
		return *userMessage.Content.Text
	}
	for _, block := range userMessage.Content.Blocks {
		if text, ok := block.(*ai.TextContent); ok {
			return text.Text
		}
	}
	return ""
}
