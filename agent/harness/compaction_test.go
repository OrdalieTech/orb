package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
)

func TestCompactionTokenAccountingAndThreshold(t *testing.T) {
	usage := ai.Usage{Input: 1000, Output: 500, CacheRead: 200, CacheWrite: 100, TotalTokens: 1800}
	if got := CalculateContextTokens(usage); got != 1800 {
		t.Fatalf("context tokens = %d", got)
	}
	usage.TotalTokens = 0
	if got := CalculateContextTokens(usage); got != 1800 {
		t.Fatalf("fallback context tokens = %d", got)
	}
	settings := CompactionSettings{Enabled: true, ReserveTokens: 10000, KeepRecentTokens: 20000}
	if !ShouldCompact(90001, 100000, settings) {
		t.Fatal("strictly over threshold did not compact")
	}
	if ShouldCompact(90000, 100000, settings) {
		t.Fatal("threshold equality compacted")
	}
	settings.Enabled = false
	if ShouldCompact(95000, 100000, settings) {
		t.Fatal("disabled settings compacted")
	}
}

func TestEstimateContextTokensUsesLastValidUsageAndTrailingEstimate(t *testing.T) {
	errorText := "overloaded"
	messages := agent.AgentMessages{
		user("hello"),
		assistant("first", 120),
		user("😀tail"),
		&ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: &errorText, Usage: ai.Usage{}, Content: ai.AssistantContent{}},
	}
	estimate := EstimateContextTokens(messages)
	if estimate.LastUsageIndex == nil || *estimate.LastUsageIndex != 1 || estimate.UsageTokens != 120 {
		t.Fatalf("estimate anchor = %#v", estimate)
	}
	wantTrailing := EstimateTokens(messages[2]) + EstimateTokens(messages[3])
	if estimate.TrailingTokens != wantTrailing || estimate.Tokens != 120+wantTrailing {
		t.Fatalf("estimate = %#v, trailing want %d", estimate, wantTrailing)
	}
	image := &ai.ToolResultMessage{Content: ai.ToolResultContent{&ai.TextContent{Text: "text"}, &ai.ImageContent{MimeType: "image/png"}}}
	if got := EstimateTokens(image); got <= 1000 {
		t.Fatalf("image estimate = %d", got)
	}
}

func TestFindCutPointAndPrepareCompaction(t *testing.T) {
	entries := linearEntries(
		user("old request that is long enough to summarize"), assistant("old answer that is long enough to summarize", 60),
		user("recent request"), assistant("recent answer", 100),
	)
	cut := FindCutPoint(entries, 0, len(entries), 5)
	if cut.FirstKeptEntryIndex != 2 || cut.TurnStartIndex != -1 || cut.IsSplitTurn {
		t.Fatalf("cut = %#v", cut)
	}
	prepared, err := PrepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 100, KeepRecentTokens: 5})
	if err != nil {
		t.Fatal(err)
	}
	if prepared == nil || prepared.FirstKeptEntryID != entries[2].ID || len(prepared.MessagesToSummarize) != 2 {
		t.Fatalf("preparation = %#v", prepared)
	}
	if prepared.TokensBefore != 100 {
		t.Fatalf("tokens before = %d", prepared.TokensBefore)
	}
}

func TestPrepareCompactionRejectsSessionWithNoDiscardableMessages(t *testing.T) {
	entries := linearEntries(user("short request"), assistant("short answer", 10))
	prepared, err := PrepareCompaction(entries, CompactionSettings{Enabled: true, ReserveTokens: 16384, KeepRecentTokens: 20000})
	if err != nil {
		t.Fatal(err)
	}
	if prepared != nil {
		t.Fatalf("preparation = %#v, want nil", prepared)
	}
}

func TestPrepareCompactionCarriesPreviousSummaryAndFileDetails(t *testing.T) {
	call := &ai.ToolCall{ID: "call", Name: "write", Arguments: map[string]any{"path": "new.go"}}
	entries := linearEntries(user("old"), &ai.AssistantMessage{Content: ai.AssistantContent{call}, StopReason: ai.StopReasonStop, Usage: usage(20)})
	fromHook := false
	entries = append(entries, SessionEntry{
		Type: "compaction", ID: "compact", ParentID: ptr(entries[len(entries)-1].ID), Timestamp: timestamp(3),
		Summary: "old summary", FirstKeptEntryID: entries[0].ID, TokensBefore: 20,
		Details: CompactionDetails{ReadFiles: []string{"old-read.go"}, ModifiedFiles: []string{"old-edit.go"}}, FromHook: fromHook,
	})
	entries = append(entries, SessionEntry{Type: "message", ID: "entry-3", ParentID: ptr("compact"), Timestamp: timestamp(4), Message: user("latest request")})
	entries = append(entries, SessionEntry{Type: "message", ID: "entry-4", ParentID: ptr("entry-3"), Timestamp: timestamp(5), Message: assistant(strings.Repeat("answer ", 30), 80)})
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

func TestSummaryPromptStructureAndReasoning(t *testing.T) {
	model := &ai.Model{Reasoning: true, MaxTokens: 128, ContextWindow: 1000}
	previous := "old summary"
	var seen ai.Context
	var seenOptions ai.SimpleStreamOptions
	complete := func(_ context.Context, _ *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		seen = request
		seenOptions = *options
		return assistant("## Goal\nDone", 10), nil
	}
	summary, err := GenerateSummary(context.Background(), agent.AgentMessages{user("work")}, model, complete, 1000, "focus", &previous, ai.ModelThinkingMedium)
	if err != nil {
		t.Fatal(err)
	}
	if summary != "## Goal\nDone" || seen.SystemPrompt == nil || *seen.SystemPrompt != SummarizationSystemPrompt {
		t.Fatalf("summary=%q context=%#v", summary, seen)
	}
	prompt := userMessageTextForTest(seen.Messages[0])
	for _, required := range []string{"<conversation>", "<previous-summary>\nold summary", UpdateSummarizationPrompt, "Additional focus: focus"} {
		if !strings.Contains(prompt, required) {
			t.Fatalf("prompt missing %q", required)
		}
	}
	if seenOptions.MaxTokens == nil || *seenOptions.MaxTokens != 128 || seenOptions.Reasoning == nil || *seenOptions.Reasoning != ai.ThinkingMedium {
		t.Fatalf("options = %#v", seenOptions)
	}
}

func TestBranchPreparationAndPrompt(t *testing.T) {
	read := &ai.ToolCall{ID: "read", Name: "read", Arguments: map[string]any{"path": "a.go"}}
	entries := linearEntries(user("branch request"), &ai.AssistantMessage{Content: ai.AssistantContent{read}, StopReason: ai.StopReasonStop, Usage: usage(20)})
	prepared := PrepareBranchEntries(entries, 1000)
	if len(prepared.Messages) != 2 || prepared.TotalTokens == 0 {
		t.Fatalf("prepared = %#v", prepared)
	}
	model := &ai.Model{ContextWindow: 1000, MaxTokens: 100}
	var prompt string
	complete := func(_ context.Context, _ *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (*ai.AssistantMessage, error) {
		prompt = userMessageTextForTest(request.Messages[0])
		if options.MaxTokens == nil || *options.MaxTokens != 2048 {
			t.Fatalf("max tokens = %#v", options.MaxTokens)
		}
		return assistant("## Goal\nBranch", 10), nil
	}
	reserveTokens := int64(100)
	result, err := GenerateBranchSummary(context.Background(), entries, GenerateBranchSummaryOptions{Model: model, Complete: complete, ReserveTokens: &reserveTokens})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.Summary, BranchSummaryPreamble) || !strings.Contains(prompt, BranchSummaryPrompt) {
		t.Fatalf("result=%#v prompt=%q", result, prompt)
	}
	if len(result.ReadFiles) != 1 || result.ReadFiles[0] != "a.go" {
		t.Fatalf("read files = %#v", result.ReadFiles)
	}
}

func TestRetryAndOverflowClassification(t *testing.T) {
	for _, text := range []string{"overloaded_error", "Provider finish_reason: network_error", "stream ended before a terminal response event"} {
		message := assistantError(text)
		if !IsRetryableAssistantError(message) {
			t.Fatalf("not retryable: %q", text)
		}
	}
	if IsRetryableAssistantError(assistantError("429 quota exceeded")) {
		t.Fatal("quota exhaustion retried")
	}
	if !IsContextOverflow(assistantError("prompt is too long"), 200000) {
		t.Fatal("explicit overflow not detected")
	}
	if IsContextOverflow(assistantError("Rate limit exceeded: too many tokens"), 200000) {
		t.Fatal("rate limit classified as overflow")
	}
	if !IsContextOverflow(assistantError(" Throttling error: too many tokens"), 200000) {
		t.Fatal("error text was trimmed before anchored upstream patterns")
	}
	silent := assistant("", 0)
	silent.Usage.Input = 101
	silent.StopReason = ai.StopReasonStop
	if !IsContextOverflow(silent, 100) {
		t.Fatal("silent overflow not detected")
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

func assistantError(text string) *ai.AssistantMessage {
	message := assistant("", 0)
	message.StopReason = ai.StopReasonError
	message.ErrorMessage = &text
	return message
}

func usage(tokens int64) ai.Usage {
	return ai.Usage{Input: tokens, TotalTokens: tokens, Cost: ai.Cost{}}
}

func linearEntries(messages ...agent.AgentMessage) []SessionEntry {
	entries := make([]SessionEntry, 0, len(messages))
	var parent *string
	for index, message := range messages {
		id := "entry-" + string(rune('0'+index))
		entries = append(entries, SessionEntry{Type: "message", ID: id, ParentID: parent, Timestamp: timestamp(index + 1), Message: message})
		parent = ptr(id)
	}
	return entries
}

func timestamp(second int) string { return "2025-01-01T00:00:" + fmtTwoDigits(second) + ".000Z" }
func fmtTwoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}
func ptr(value string) *string { return &value }

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
