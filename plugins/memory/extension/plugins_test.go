package extension

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	memorysdk "github.com/OrdalieTech/orb/plugins/memory"
)

func strPtr(s string) *string { return &s }

type captureUI struct {
	extensions.NoopUI
	mu      sync.Mutex
	notices []string
}

func (u *captureUI) Notify(message string, _ extensions.NotificationType) {
	u.mu.Lock()
	u.notices = append(u.notices, message)
	u.mu.Unlock()
}

func (u *captureUI) lastNotice() string {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.notices) == 0 {
		return ""
	}
	return u.notices[len(u.notices)-1]
}

// blockingStore blocks EntryCount until ctx is cancelled, simulating a locked SQLite store.
type blockingStore struct {
	*memorysdk.MemoryStore
	blockEntryCount bool
}

func (s *blockingStore) EntryCount(ctx context.Context) (int, error) {
	if s.blockEntryCount {
		<-ctx.Done()
		return 0, ctx.Err()
	}
	return s.MemoryStore.EntryCount(ctx)
}

func newTestRunner(t *testing.T, factory extensions.Factory) (*extensions.Runner, *captureUI) {
	t.Helper()
	cwd := t.TempDir()
	reg := extensions.NewRegistry(cwd)
	if err := reg.Register("memory", factory); err != nil {
		t.Fatalf("Register: %v", err)
	}
	ui := &captureUI{}
	runner := extensions.NewRunner(reg, extensions.RunnerOptions{
		CWD:  cwd,
		Mode: extensions.Mode("tui"),
		UI:   ui,
	})
	return runner, ui
}

func TestExtensionRegistersSingleToolAndCommand(t *testing.T) {
	store := memorysdk.NewMemoryStore()
	// Verify 1-arg Extension(store) still compiles and works.
	runner, _ := newTestRunner(t, Extension(store))

	tools := runner.AllRegisteredTools()
	if len(tools) != 1 {
		t.Fatalf("expected 1 registered tool, got %d", len(tools))
	}
	if tools[0].Definition.Name != "memory" {
		t.Fatalf("expected tool 'memory', got %q", tools[0].Definition.Name)
	}

	commands := runner.RegisteredCommands()
	if len(commands) != 1 || commands[0].InvocationName != "memory" {
		t.Fatalf("expected /memory command to be registered, got %+v", commands)
	}

	runner.Emit(context.Background(), extensions.SessionShutdownEvent{})
}

func TestBeforeAgentStartInjectionAndTimeoutFailOpen(t *testing.T) {
	ctx := context.Background()
	baseStore := memorysdk.NewMemoryStore()
	_, _ = baseStore.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Initial memory fact"})
	store := &blockingStore{MemoryStore: baseStore}

	runner, _ := newTestRunner(t, Extension(store, Options{Peer: "test-peer"}))
	defer runner.Emit(ctx, extensions.SessionShutdownEvent{})

	runner.Emit(ctx, extensions.SessionStartEvent{})

	// Simulate another session appending an entry.
	_, _ = baseStore.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindTalk, Text: "Cross-session update"})

	res := runner.EmitBeforeAgentStart(ctx, "hello", nil, nil, "BASE_PROMPT", extensions.SystemPromptOptions{})
	if res == nil || res.SystemPrompt == nil || !strings.Contains(*res.SystemPrompt, "Initial memory fact") {
		t.Fatalf("expected frozen system prompt with Initial memory fact, got %+v", res)
	}
	if len(res.Messages) != 1 || res.Messages[0].CustomType != "orb.memory" {
		t.Fatalf("expected 1 orb.memory custom message, got %+v", res.Messages)
	}
	if content, _ := res.Messages[0].Content.(string); !strings.Contains(content, "Cross-session update") {
		t.Fatalf("expected Cross-session update in custom message, got %q", content)
	}

	// Now test fail-open on timeout.
	oldTimeout := changesTimeout
	changesTimeout = 25 * time.Millisecond
	defer func() { changesTimeout = oldTimeout }()

	store.blockEntryCount = true
	res2 := runner.EmitBeforeAgentStart(ctx, "hello again", nil, nil, "BASE_PROMPT", extensions.SystemPromptOptions{})
	if res2 == nil || res2.SystemPrompt == nil || *res2.SystemPrompt != *res.SystemPrompt {
		t.Fatalf("expected stable system prompt on timeout, got %+v", res2)
	}
	if len(res2.Messages) != 0 {
		t.Fatalf("expected no delta message on timeout, got %+v", res2.Messages)
	}
}

func TestAgentEndAsyncRecordingAndShutdown(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()

	runner, _ := newTestRunner(t, Extension(store, Options{Peer: "cli"}))
	runner.Emit(ctx, extensions.SessionStartEvent{})

	msgs := engine.AgentMessages{
		&ai.UserMessage{Content: ai.UserContent{Text: strPtr("Check memory recording")}},
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "Recorded cleanly."}}},
	}
	runner.Emit(ctx, extensions.AgentEndEvent{Messages: msgs})

	// SessionShutdown must drain the async recording goroutine before returning.
	runner.Emit(ctx, extensions.SessionShutdownEvent{})

	entries, err := store.ListEntries(ctx, 0, -1)
	if err != nil {
		t.Fatalf("ListEntries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 recorded entries, got %d: %+v", len(entries), entries)
	}
	if entries[0].Text != "Check memory recording" || entries[1].Text != "Recorded cleanly." {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestMemorySlashCommand(t *testing.T) {
	ctx := context.Background()
	store := memorysdk.NewMemoryStore()
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindUser, Text: "Alpha feature spec"})
	_, _ = store.AppendEntry(ctx, memorysdk.Entry{Kind: memorysdk.KindTalk, Text: "Implemented alpha spec"})

	runner, ui := newTestRunner(t, Extension(store, Options{Peer: "tui"}))
	defer runner.Emit(ctx, extensions.SessionShutdownEvent{})

	runner.Emit(ctx, extensions.SessionStartEvent{})

	commands := runner.RegisteredCommands()
	if len(commands) != 1 {
		t.Fatalf("/memory command missing")
	}
	cmd := commands[0]
	cmdCtx := runner.CreateCommandContext()

	// Bare /memory
	if err := cmd.Handler(ctx, "", cmdCtx); err != nil {
		t.Fatalf("bare /memory: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "Usage: /memory") {
		t.Fatalf("expected usage in bare /memory, got: %s", ui.lastNotice())
	}

	// /memory status
	if err := cmd.Handler(ctx, "status", cmdCtx); err != nil {
		t.Fatalf("/memory status: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "entries=2") {
		t.Fatalf("expected entries=2 in status, got: %s", ui.lastNotice())
	}

	// /memory view
	if err := cmd.Handler(ctx, "view", cmdCtx); err != nil {
		t.Fatalf("/memory view: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "Alpha feature spec") {
		t.Fatalf("expected Alpha feature spec in view, got: %s", ui.lastNotice())
	}

	// /memory compact
	if err := cmd.Handler(ctx, "compact", cmdCtx); err != nil {
		t.Fatalf("/memory compact: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "Compacted memory") {
		t.Fatalf("expected Compacted memory, got: %s", ui.lastNotice())
	}

	// /memory search alpha
	if err := cmd.Handler(ctx, "search alpha", cmdCtx); err != nil {
		t.Fatalf("/memory search: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "0+1") {
		t.Fatalf("expected 0+1 in search, got: %s", ui.lastNotice())
	}

	// /memory zoom 0
	if err := cmd.Handler(ctx, "zoom 0", cmdCtx); err != nil {
		t.Fatalf("/memory zoom 0: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "Alpha feature spec") {
		t.Fatalf("expected Alpha feature spec in zoom 0, got: %s", ui.lastNotice())
	}

	// /memory zoom 0+2
	if err := cmd.Handler(ctx, "zoom 0+2", cmdCtx); err != nil {
		t.Fatalf("/memory zoom 0+2: %v", err)
	}
	if !strings.Contains(ui.lastNotice(), "0+1:") || !strings.Contains(ui.lastNotice(), "1+1:") {
		t.Fatalf("expected expanded 0+2 children, got: %s", ui.lastNotice())
	}
}
