package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
)

func newTestRunnerWithSession(t *testing.T, factory extensions.Factory, cwd string) (*extensions.Runner, *captureUI, *session.SessionManager) {
	t.Helper()
	if cwd == "" {
		cwd = t.TempDir()
	}
	reg := extensions.NewRegistry(cwd)
	if err := reg.Register("memory", factory); err != nil {
		t.Fatalf("Register: %v", err)
	}
	sm, err := session.InMemory(cwd)
	if err != nil {
		t.Fatalf("session.InMemory: %v", err)
	}
	ui := &captureUI{}
	runner := extensions.NewRunner(reg, extensions.RunnerOptions{
		CWD:            cwd,
		Mode:           extensions.Mode("tui"),
		UI:             ui,
		SessionManager: sm,
	})
	return runner, ui, sm
}

// TestStressUnifiedToolAndRejectsDate verifies that the memory extension registers
// exactly 1 tool ("memory") exposing only search/zoom/profile and rejecting action="date".
func TestStressUnifiedToolAndRejectsDate(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Project codename is Helios"})

	runner, _ := newTestRunner(t, Extension(store, Options{Peer: "stress"}))
	defer runner.Emit(ctx, extensions.SessionShutdownEvent{})
	runner.Emit(ctx, extensions.SessionStartEvent{})

	tools := runner.AllRegisteredTools()
	if len(tools) != 1 {
		t.Fatalf("expected total tool count = 1, got %d", len(tools))
	}
	tool := tools[0].Definition
	if tool.Name != "memory" {
		t.Fatalf("expected tool name 'memory', got %q", tool.Name)
	}

	// Verify schema exposes ONLY search, zoom, profile.
	rawParams, err := json.Marshal(tool.Parameters)
	if err != nil {
		t.Fatalf("marshal parameters: %v", err)
	}
	var schema struct {
		Properties struct {
			Action struct {
				Enum []string `json:"enum"`
			} `json:"action"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(rawParams, &schema); err != nil {
		t.Fatalf("unmarshal parameters schema: %v", err)
	}
	wantActions := map[string]bool{"search": true, "zoom": true, "profile": true}
	if len(schema.Properties.Action.Enum) != len(wantActions) {
		t.Fatalf("expected exactly 3 actions (search, zoom, profile), got %v", schema.Properties.Action.Enum)
	}
	for _, a := range schema.Properties.Action.Enum {
		if !wantActions[a] {
			t.Fatalf("unexpected action in schema enum: %q (full: %v)", a, schema.Properties.Action.Enum)
		}
	}

	// Reject action: "date" with a clear error.
	_, err = tool.Execute(ctx, "call-date", map[string]any{"action": "date"}, nil, nil)
	if err == nil {
		t.Fatalf("expected error for action='date', got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unknown") && !strings.Contains(strings.ToLower(err.Error()), "date") {
		t.Fatalf("expected clear error mentioning unknown action or 'date', got: %v", err)
	}

	// Verify search, zoom, profile succeed.
	res, err := tool.Execute(ctx, "call-search", map[string]any{"action": "search", "query": "Helios"}, nil, nil)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("search returned empty content")
	}

	res, err = tool.Execute(ctx, "call-zoom", map[string]any{"action": "zoom", "entry_id": float64(0)}, nil, nil)
	if err != nil {
		t.Fatalf("zoom failed: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("zoom returned empty content")
	}

	res, err = tool.Execute(ctx, "call-profile", map[string]any{"action": "profile", "op": "add", "content": "Uses Go 1.24"}, nil, nil)
	if err != nil {
		t.Fatalf("profile add failed: %v", err)
	}
	if len(res.Content) == 0 {
		t.Fatalf("profile returned empty content")
	}
}

// TestStress35TurnsPrefixStabilityAndNoisyDump simulates 35 consecutive turns through
// extensions.Runner including a 60 KiB noisy bash dump, verifying:
//   - Prompt-cache prefix stability across all 35 turns (100% byte-identical)
//   - Noisy tool output protection (every work entry <= 480 bytes, never contains raw dump)
func TestStress35TurnsPrefixStabilityAndNoisyDump(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Seeded fact before session start"})

	runner, _ := newTestRunner(t, Extension(store, Options{Peer: "stress-35"}))
	runner.Emit(ctx, extensions.SessionStartEvent{})

	const turns = 35
	const dumpMarker = "NOISY_BASH_DUMP_60K_PAYLOAD_"
	noisy60K := dumpMarker + strings.Repeat("0123456789abcdef", 60*1024/16)

	var firstPrompt string
	for i := 0; i < turns; i++ {
		res := runner.EmitBeforeAgentStart(ctx, fmt.Sprintf("turn %d prompt", i), nil, nil, "BASE_SYSTEM_PROMPT", extensions.SystemPromptOptions{})
		if res == nil || res.SystemPrompt == nil {
			t.Fatalf("turn %d: expected non-nil SystemPrompt", i)
		}
		if i == 0 {
			firstPrompt = *res.SystemPrompt
			if !strings.Contains(firstPrompt, "Seeded fact before session start") {
				t.Fatalf("turn 0: expected seeded fact in system prompt, got %q", firstPrompt)
			}
		} else if *res.SystemPrompt != firstPrompt {
			t.Fatalf("turn %d: prompt-cache prefix mutated mid-session!\nlen(turn0)=%d len(turn%d)=%d", i, len(firstPrompt), i, len(*res.SystemPrompt))
		}

		// Build turn messages including a 60 KiB noisy bash dump on every turn.
		msgs := engine.AgentMessages{
			&ai.UserMessage{Content: ai.UserContent{Text: strPtr(fmt.Sprintf("Run noisy build step %d", i))}},
			&ai.AssistantMessage{Content: ai.AssistantContent{
				&ai.ToolCall{ID: fmt.Sprintf("tc-%d", i), Name: "bash", Arguments: map[string]any{"command": fmt.Sprintf("make build-step-%d", i)}},
			}},
			&ai.ToolResultMessage{
				ToolCallID: fmt.Sprintf("tc-%d", i),
				ToolName:   "bash",
				Content:    ai.ToolResultContent{&ai.TextContent{Text: noisy60K}},
			},
			&ai.AssistantMessage{Content: ai.AssistantContent{
				&ai.TextContent{Text: fmt.Sprintf("Completed build step %d cleanly.", i)},
			}},
		}
		runner.Emit(ctx, extensions.AgentEndEvent{Messages: msgs})
	}

	// Drain all async recordings cleanly.
	runner.Emit(ctx, extensions.SessionShutdownEvent{})

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	// 1 seeded + 35 turns * (user + work + talk) = at least 1 + 35*2 entries.
	if len(entries) < 1+turns*2 {
		t.Fatalf("expected at least %d entries after %d turns, got %d", 1+turns*2, turns, len(entries))
	}

	workCount := 0
	maxWorkBytes := 0
	for _, e := range entries {
		if strings.Contains(e.Text, dumpMarker) {
			t.Fatalf("entry %d (%s) leaked raw 60 KiB bash dump marker!", e.ID, e.Kind)
		}
		if e.Kind == memorysdk.KindWork {
			workCount++
			if len(e.Text) > maxWorkBytes {
				maxWorkBytes = len(e.Text)
			}
			if len(e.Text) > 480 {
				t.Fatalf("work entry %d exceeded 480 bytes: len=%d text=%q", e.ID, len(e.Text), e.Text)
			}
		}
	}
	if workCount != turns {
		t.Fatalf("expected %d work entries, got %d", turns, workCount)
	}
	t.Logf("35-turn stress: total_entries=%d work_entries=%d max_work_bytes=%d prefix_bytes=%d (100%% stable)",
		len(entries), workCount, maxWorkBytes, len(firstPrompt))
}

// TestStressShutdownFlushRace fires AgentEnd immediately followed by SessionShutdown
// 20 times in a row under -race to prove every turn is durably flushed with zero dropped entries.
func TestStressShutdownFlushRace(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()

	const iterations = 20
	for i := 0; i < iterations; i++ {
		runner, _ := newTestRunner(t, Extension(store, Options{Peer: fmt.Sprintf("peer-%d", i)}))
		runner.Emit(ctx, extensions.SessionStartEvent{})

		msgs := engine.AgentMessages{
			&ai.UserMessage{Content: ai.UserContent{Text: strPtr(fmt.Sprintf("Flush race user turn %d", i))}},
			&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: fmt.Sprintf("Flush race reply %d", i)}}},
		}
		runner.Emit(ctx, extensions.AgentEndEvent{Messages: msgs})
		// Immediately fire SessionShutdown without sleeping.
		runner.Emit(ctx, extensions.SessionShutdownEvent{})
	}

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	want := iterations * 2
	if len(entries) != want {
		t.Fatalf("dropped entries during shutdown flush race: got %d, want %d", len(entries), want)
	}
	for i := 0; i < iterations; i++ {
		u := entries[i*2]
		a := entries[i*2+1]
		wantU := fmt.Sprintf("Flush race user turn %d", i)
		wantA := fmt.Sprintf("Flush race reply %d", i)
		if u.Text != wantU || a.Text != wantA {
			t.Fatalf("iteration %d mismatch: got (%q, %q), want (%q, %q)", i, u.Text, a.Text, wantU, wantA)
		}
	}
	t.Logf("shutdown flush race: %d/%d turns (%d entries) durably flushed with 0 drops", iterations, iterations, len(entries))
}

// TestStressMultiSessionCrossTalkAndTimeout runs two concurrent extension runtimes
// (Session A and Session B) sharing one store and verifies:
//   - Session A profile edit + turn -> Session B receives orb.memory delta ONCE (not twice)
//   - Session A does NOT receive an echo of its own profile edits or own recorded turns
//   - Slow/stalled store beyond changesTimeout fails open cleanly without stalling or error
func TestStressMultiSessionCrossTalkAndTimeout(t *testing.T) {
	ctx := context.Background()
	baseStore := memorysdk.NewMemoryStore()
	store := &blockingStore{MemoryStore: baseStore}

	cwd := t.TempDir()
	runnerA, _, _ := newTestRunnerWithSession(t, Extension(store, Options{Peer: "peer-A"}), cwd)
	runnerB, _, _ := newTestRunnerWithSession(t, Extension(store, Options{Peer: "peer-B"}), cwd)
	defer runnerA.Emit(ctx, extensions.SessionShutdownEvent{})
	defer runnerB.Emit(ctx, extensions.SessionShutdownEvent{})

	runnerA.Emit(ctx, extensions.SessionStartEvent{})
	runnerB.Emit(ctx, extensions.SessionStartEvent{})

	// Prime both sessions so watermarks are initialized.
	_ = runnerA.EmitBeforeAgentStart(ctx, "init A", nil, nil, "BASE", extensions.SystemPromptOptions{})
	_ = runnerB.EmitBeforeAgentStart(ctx, "init B", nil, nil, "BASE", extensions.SystemPromptOptions{})

	// Session A adds a profile fact via the unified memory tool.
	toolA := runnerA.AllRegisteredTools()[0].Definition
	if _, err := toolA.Execute(ctx, "prof-A", map[string]any{
		"action":  "profile",
		"op":      "add",
		"content": "User prefers dark mode and Go tables",
	}, nil, nil); err != nil {
		t.Fatalf("Session A profile add: %v", err)
	}

	// Session A completes a turn.
	runnerA.Emit(ctx, extensions.AgentEndEvent{Messages: engine.AgentMessages{
		&ai.UserMessage{Content: ai.UserContent{Text: strPtr("Session A question about caching")}},
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "Session A answer about caching"}}},
	}})

	// Wait briefly for Session A's async turn recording to land in store.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		cnt, _ := store.EntryCount(ctx)
		if cnt >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if cnt, _ := store.EntryCount(ctx); cnt < 2 {
		t.Fatalf("timed out waiting for Session A async recording, count=%d", cnt)
	}

	// Session A must NOT receive an echo of its own profile edits or own recorded turns.
	resA := runnerA.EmitBeforeAgentStart(ctx, "next A", nil, nil, "BASE", extensions.SystemPromptOptions{})
	if resA != nil && len(resA.Messages) > 0 {
		t.Fatalf("Session A echoed its own changes: %+v", resA.Messages)
	}

	// Session B MUST receive the orb.memory delta message on its next BeforeAgentStart.
	resB1 := runnerB.EmitBeforeAgentStart(ctx, "next B 1", nil, nil, "BASE", extensions.SystemPromptOptions{})
	if resB1 == nil || len(resB1.Messages) != 1 || resB1.Messages[0].CustomType != "orb.memory" {
		t.Fatalf("Session B expected 1 orb.memory delta message, got %+v", resB1)
	}
	deltaContent, _ := resB1.Messages[0].Content.(string)
	if !strings.Contains(deltaContent, "Session A question about caching") || !strings.Contains(deltaContent, "User prefers dark mode and Go tables") {
		t.Fatalf("Session B delta missing expected content: %q", deltaContent)
	}

	// Session B must NOT receive the same delta twice on the following turn.
	resB2 := runnerB.EmitBeforeAgentStart(ctx, "next B 2", nil, nil, "BASE", extensions.SystemPromptOptions{})
	if resB2 != nil && len(resB2.Messages) > 0 {
		t.Fatalf("Session B received duplicate delta on following turn: %+v", resB2.Messages)
	}

	// Store slow/stalled beyond changesTimeout: BeforeAgentStart fails open cleanly without stalling.
	oldTimeout := changesTimeout
	changesTimeout = 30 * time.Millisecond
	defer func() { changesTimeout = oldTimeout }()

	store.blockEntryCount = true
	start := time.Now()
	resStall := runnerB.EmitBeforeAgentStart(ctx, "stalled B", nil, nil, "BASE", extensions.SystemPromptOptions{})
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Fatalf("BeforeAgentStart stalled too long on blocked store: %v", elapsed)
	}
	if resStall != nil && len(resStall.Messages) != 0 {
		t.Fatalf("expected no delta messages on timeout fail-open, got %+v", resStall.Messages)
	}
	t.Logf("multi-session cross-talk & fail-open passed (timeout elapsed=%v)", elapsed)
}

type stubSummarizer struct {
	mu    sync.Mutex
	calls int
}

func (s *stubSummarizer) summarize(_ context.Context, _, userPrompt string) (string, error) {
	s.mu.Lock()
	s.calls++
	n := s.calls
	s.mu.Unlock()
	return fmt.Sprintf("stub-summary-%d (%d bytes)", n, len(userPrompt)), nil
}

// TestStressSlashCommandEdgeCases tests /memory on an empty store, malformed zoom args,
// empty search query, and compact without summarizer vs with summarizer.
func TestStressSlashCommandEdgeCases(t *testing.T) {
	ctx := context.Background()

	// 1. Empty store edge cases + malformed args + compact without summarizer.
	emptyStore := memorysdk.NewMemoryStore()
	runner, ui := newTestRunner(t, Extension(emptyStore, Options{Peer: "slash"}))
	defer runner.Emit(ctx, extensions.SessionShutdownEvent{})
	runner.Emit(ctx, extensions.SessionStartEvent{})

	cmd := runner.RegisteredCommands()[0]
	cmdCtx := runner.CreateCommandContext()

	// Bare /memory on empty store.
	if err := cmd.Handler(ctx, "", cmdCtx); err != nil {
		t.Fatalf("bare /memory on empty store: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "entries=0") {
		t.Fatalf("expected entries=0 on empty store, got: %s", ui.lastNotice())
	}

	// /memory view on empty store.
	if err := cmd.Handler(ctx, "view", cmdCtx); err != nil {
		t.Fatalf("/memory view on empty store: %v", err)
	}

	// Empty search query.
	for _, q := range []string{"search", "search   "} {
		if err := cmd.Handler(ctx, q, cmdCtx); err == nil {
			t.Fatalf("expected error for %q, got nil", q)
		}
	}

	// Malformed zoom args.
	for _, bad := range []string{"zoom", "zoom abc", "zoom -1", "zoom 0+3", "zoom 999"} {
		err := cmd.Handler(ctx, bad, cmdCtx)
		if err == nil {
			t.Fatalf("expected error for malformed/out-of-range %q, got nil (notice=%q)", bad, ui.lastNotice())
		}
	}

	// Compact without summarizer on empty store (should succeed with 0 nodes or deterministic fallback).
	if err := cmd.Handler(ctx, "compact", cmdCtx); err != nil {
		t.Fatalf("/memory compact on empty store without summarizer: %v", err)
	}

	// 2. Compact without summarizer vs with summarizer on populated store with
	// entries large enough (~360 bytes each) that parent nodes exceed 512 bytes.
	longPad := strings.Repeat("abcdefghij", 34)
	popStore1 := memorysdk.NewMemoryStore()
	for i := 0; i < 6; i++ {
		_, _ = popStore1.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: fmt.Sprintf("Entry %d for compaction: %s", i, longPad)})
	}
	rNoSum, uiNoSum := newTestRunner(t, Extension(popStore1, Options{Peer: "nosum"}))
	defer rNoSum.Emit(ctx, extensions.SessionShutdownEvent{})
	rNoSum.Emit(ctx, extensions.SessionStartEvent{})
	if err := rNoSum.RegisteredCommands()[0].Handler(ctx, "compact", rNoSum.CreateCommandContext()); err != nil {
		t.Fatalf("compact without summarizer failed: %v", err)
	}
	if !strings.Contains(uiNoSum.lastNotice(), "Compacted memory") {
		t.Fatalf("unexpected compact notice without summarizer: %s", uiNoSum.lastNotice())
	}

	popStore2 := memorysdk.NewMemoryStore()
	for i := 0; i < 6; i++ {
		_, _ = popStore2.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: fmt.Sprintf("Entry %d for summarizer compaction: %s", i, longPad)})
	}
	sum := &stubSummarizer{}
	rWithSum, uiWithSum := newTestRunner(t, Extension(popStore2, Options{Peer: "withsum", Summarizer: sum.summarize}))
	defer rWithSum.Emit(ctx, extensions.SessionShutdownEvent{})
	rWithSum.Emit(ctx, extensions.SessionStartEvent{})
	if err := rWithSum.RegisteredCommands()[0].Handler(ctx, "compact", rWithSum.CreateCommandContext()); err != nil {
		t.Fatalf("compact with summarizer failed: %v", err)
	}
	if !strings.Contains(uiWithSum.lastNotice(), "Compacted memory") {
		t.Fatalf("unexpected compact notice with summarizer: %s", uiWithSum.lastNotice())
	}
	sum.mu.Lock()
	calls := sum.calls
	sum.mu.Unlock()
	if calls == 0 {
		t.Fatalf("expected custom summarizer to be invoked during compact, got 0 calls")
	}
	t.Logf("slash command edge cases passed (summarizer_calls=%d)", calls)
}
