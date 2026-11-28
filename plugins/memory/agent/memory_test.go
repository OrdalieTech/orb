package agentmemory

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
)

func strPtr(s string) *string { return &s }

func resultText(t *testing.T, res engine.AgentToolResult) string {
	t.Helper()
	var parts []string
	for _, block := range res.Content {
		if tb, ok := block.(*ai.TextContent); ok && tb != nil {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func TestSingleUnifiedMemoryTool(t *testing.T) {
	store := memorysdk.NewMemoryStore()
	rt, err := New(store)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	tools := rt.Tools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	spec := tools[0].Spec()
	if spec.Name != "memory" {
		t.Fatalf("expected tool name 'memory', got %q", spec.Name)
	}
	if spec.Label != "Memory" {
		t.Fatalf("expected tool label 'Memory', got %q", spec.Label)
	}
}

func TestMemoryToolProfileSearchZoomDate(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()

	var writes atomic.Int32
	rt, err := New(store, WithPeer("local"), WithSession("sess-1"), WithOnWrite(func() {
		writes.Add(1)
	}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.Close()

	if err := rt.Load(ctx); err != nil {
		t.Fatalf("Load: %v", err)
	}
	tool := rt.Tools()[0]

	// 1. Profile add
	res, err := tool.Execute(ctx, "c1", map[string]any{
		"action":  "profile",
		"op":      "add",
		"content": "User prefers concise Go code.",
	}, nil)
	if err != nil {
		t.Fatalf("profile add: %v", err)
	}
	if !strings.Contains(resultText(t, res), "Profile entry saved") {
		t.Fatalf("unexpected profile add result: %s", resultText(t, res))
	}
	if writes.Load() != 1 {
		t.Fatalf("expected onWrite=1, got %d", writes.Load())
	}

	// Own profile edit must not re-echo in Changes.
	delta, err := rt.Changes(ctx)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if delta != "" {
		t.Fatalf("expected empty delta after own profile edit, got %q", delta)
	}

	// 2. Profile set (update by old_text)
	res, err = tool.Execute(ctx, "c2", map[string]any{
		"action":   "profile",
		"op":       "set",
		"old_text": "concise Go",
		"content":  "User prefers concise, idiomatic Go code.",
	}, nil)
	if err != nil {
		t.Fatalf("profile set: %v", err)
	}
	if !strings.Contains(resultText(t, res), "Profile entry updated") {
		t.Fatalf("unexpected profile set result: %s", resultText(t, res))
	}

	// 3. Profile remove
	res, err = tool.Execute(ctx, "c3", map[string]any{
		"action":   "profile",
		"op":       "remove",
		"old_text": "idiomatic Go",
	}, nil)
	if err != nil {
		t.Fatalf("profile remove: %v", err)
	}
	if !strings.Contains(resultText(t, res), "Profile entry removed") {
		t.Fatalf("unexpected profile remove result: %s", resultText(t, res))
	}
	prof, err := store.GetProfile(ctx)
	if err != nil || len(prof) != 0 {
		t.Fatalf("expected empty profile after remove, got %d (err=%v)", len(prof), err)
	}

	// 4. Append entries and test search, zoom, date
	ts := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Migrate auth middleware to JWT", Peer: "local", Session: "sess-1", TS: ts})
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindTalk, Text: "Completed RS256 JWT migration in internal/auth/jwt.go", Peer: "local", Session: "sess-1", TS: ts.Add(time.Minute)})

	res, err = tool.Execute(ctx, "c4", map[string]any{
		"action": "search",
		"query":  "JWT",
		"limit":  5,
	}, nil)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	txt := resultText(t, res)
	if !strings.Contains(txt, "0+1") || !strings.Contains(txt, "1+1") {
		t.Fatalf("expected hits 0+1 and 1+1, got:\n%s", txt)
	}

	res, err = tool.Execute(ctx, "c5", map[string]any{
		"action": "zoom",
		"id":     1,
	}, nil)
	if err != nil {
		t.Fatalf("zoom: %v", err)
	}
	zoomOut := resultText(t, res)
	if !strings.Contains(zoomOut, "internal/auth/jwt.go") || !strings.Contains(zoomOut, "2026-04-01T10:01:00Z") {
		t.Fatalf("unexpected zoom output: %s", zoomOut)
	}
}

func TestSystemPromptStabilityAndCrossSessionChanges(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()

	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Initial question"})

	rtA, err := New(store, WithPeer("peerA"), WithSession("sessA"))
	if err != nil {
		t.Fatalf("New A: %v", err)
	}
	defer rtA.Close()

	rtB, err := New(store, WithPeer("peerB"), WithSession("sessB"))
	if err != nil {
		t.Fatalf("New B: %v", err)
	}
	defer rtB.Close()

	if err := rtA.Load(ctx); err != nil {
		t.Fatalf("Load A: %v", err)
	}
	if err := rtB.Load(ctx); err != nil {
		t.Fatalf("Load B: %v", err)
	}

	promptBefore := rtA.SystemPrompt("BASE")
	if !strings.Contains(promptBefore, "Initial question") {
		t.Fatalf("expected Initial question in system prompt, got:\n%s", promptBefore)
	}

	// Session B records a new turn and adds a profile fact.
	if err := rtB.RecordTurn(ctx, []ai.Message{
		&ai.UserMessage{Content: ai.UserContent{Text: strPtr("From session B")}},
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "Reply from B"}}},
	}); err != nil {
		t.Fatalf("RecordTurn B: %v", err)
	}
	_, err = rtB.Tools()[0].Execute(ctx, "p1", map[string]any{
		"action":  "profile",
		"op":      "add",
		"content": "Lives in Paris",
	}, nil)
	if err != nil {
		t.Fatalf("profile add B: %v", err)
	}

	// Session A's SystemPrompt MUST remain 100% byte-identical (prompt-cache stable).
	promptAfter := rtA.SystemPrompt("BASE")
	if promptAfter != promptBefore {
		t.Fatalf("SystemPrompt changed across turns!\nbefore:\n%s\nafter:\n%s", promptBefore, promptAfter)
	}

	// Session A's Changes MUST report session B's new entries and profile edit.
	delta, err := rtA.Changes(ctx)
	if err != nil {
		t.Fatalf("Changes A: %v", err)
	}
	if !strings.Contains(delta, "From session B") || !strings.Contains(delta, "Lives in Paris") {
		t.Fatalf("expected cross-session delta, got:\n%s", delta)
	}

	// Second Changes call with no new writes MUST be empty.
	delta2, err := rtA.Changes(ctx)
	if err != nil {
		t.Fatalf("Changes A second: %v", err)
	}
	if delta2 != "" {
		t.Fatalf("expected empty second delta, got %q", delta2)
	}
}

func TestRecordTurnCompactWorkAndAsyncClose(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()

	rt, err := New(store, WithPeer("local"), WithSession("sess-work"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rt.StartCompactor(ctx)

	hugeBashOutput := strings.Repeat("RAW_BASH_DUMP_LINE_0123456789\n", 1024) // > 30 Ko
	msgs := []ai.Message{
		&ai.UserMessage{Content: ai.UserContent{Text: strPtr("Run tests and fix bug")}},
		&ai.AssistantMessage{
			Content: ai.AssistantContent{
				&ai.ToolCall{ID: "tc1", Name: "bash", Arguments: map[string]any{"command": "go test ./..."}},
				&ai.ToolCall{ID: "tc2", Name: "edit", Arguments: map[string]any{"path": "plugins/memory/agent/memory.go"}},
			},
		},
		&ai.ToolResultMessage{
			ToolCallID: "tc1",
			ToolName:   "bash",
			Content:    ai.ToolResultContent{&ai.TextContent{Text: hugeBashOutput}},
			IsError:    true,
		},
		&ai.ToolResultMessage{
			ToolCallID: "tc2",
			ToolName:   "edit",
			Content:    ai.ToolResultContent{&ai.TextContent{Text: "edited ok"}},
		},
		&ai.AssistantMessage{
			Content: ai.AssistantContent{
				&ai.TextContent{Text: "Fixed the test failure in memory.go."},
			},
		},
	}

	rt.RecordTurnAsync(msgs)
	rt.Close() // drains async goroutine and stops compactor cleanly

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries (user, work, talk), got %d: %+v", len(entries), entries)
	}
	if entries[0].Kind != memorysdk.KindUser || entries[0].Text != "Run tests and fix bug" {
		t.Fatalf("unexpected user entry: %+v", entries[0])
	}
	if entries[1].Kind != memorysdk.KindWork {
		t.Fatalf("expected work entry at index 1, got %+v", entries[1])
	}
	if len(entries[1].Text) > maxWorkSummaryBytes {
		t.Fatalf("work summary exceeded %d bytes: %d", maxWorkSummaryBytes, len(entries[1].Text))
	}
	if strings.Contains(entries[1].Text, "RAW_BASH_DUMP_LINE") {
		t.Fatalf("raw 30 Ko bash output leaked into work entry: %s", entries[1].Text)
	}
	if !strings.Contains(entries[1].Text, "bash(go test ./...)[ERR]") || !strings.Contains(entries[1].Text, "edit(plugins/memory/agent/memory.go)") {
		t.Fatalf("work summary missing tool details: %s", entries[1].Text)
	}
	if entries[2].Kind != memorysdk.KindTalk || entries[2].Text != "Fixed the test failure in memory.go." {
		t.Fatalf("unexpected talk entry: %+v", entries[2])
	}
}
