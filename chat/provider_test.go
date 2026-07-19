package chat

// Construction-level tests for NewLocalProvider: path mapping, sanitization,
// and resume of an existing session file. No live prompting.

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai/providers/faux"
	"github.com/OrdalieTech/pi-go/codingagent"
)

func newTestLocalProvider(t *testing.T, opts ...LocalProviderOption) (*LocalProvider, string) {
	t.Helper()
	root := t.TempDir()
	provider, err := NewLocalProvider(root, append([]LocalProviderOption{WithAgentDir(t.TempDir())}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return provider, root
}

func TestLocalProviderSanitizesSessionDirs(t *testing.T) {
	provider, root := newTestLocalProvider(t)
	key := ConversationKey{Platform: "telegram", Account: "bot", ChatID: "we/ird ../id"}
	conversation, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conversation.Close(context.Background()) }()

	dir := provider.SessionDir(key)
	if filepath.Dir(dir) != root {
		t.Fatalf("session dir %q escaped root %q", dir, root)
	}
	base := filepath.Base(dir)
	if strings.ContainsAny(base, "/ ") {
		t.Fatalf("unsanitized session dir name %q", base)
	}
	if base != key.String() {
		t.Fatalf("dir base %q != key string %q", base, key.String())
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("session dir not created: %v", err)
	}
	if got := filepath.Dir(conversation.Manager.GetSessionFile()); got != dir {
		t.Fatalf("session file in %q, want %q", got, dir)
	}
}

func TestLocalProviderResumesMostRecentSessionFile(t *testing.T) {
	provider, _ := newTestLocalProvider(t)
	key := ConversationKey{Platform: "faux", Account: "bot", ChatID: "42"}

	first, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	sessionFile := first.Manager.GetSessionFile()
	mustAppendMarker(t, first.Manager, turnMarker{EventID: "ev-resume", Phase: phaseStarted})
	// The session store only flushes to disk once an assistant message
	// exists (upstream empty-session policy); seed one so the file persists.
	if _, err := first.Manager.AppendMessage(faux.AssistantMessage("hello")); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	if got := second.Manager.GetSessionFile(); got != sessionFile {
		t.Fatalf("resumed session file %q, want %q", got, sessionFile)
	}
	if ledger := scanTurnLedger(second.Manager, "ev-resume"); ledger.started == nil {
		t.Fatal("prior ledger marker lost on resume")
	}

	// A different key maps to a different directory and session.
	otherKey := ConversationKey{Platform: "faux", Account: "bot", ChatID: "43"}
	other, err := provider.Acquire(context.Background(), otherKey)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = other.Close(context.Background()) }()
	if other.Manager.GetSessionFile() == sessionFile {
		t.Fatal("distinct keys shared one session file")
	}
}

func TestStartNewSessionPersistsAcrossReacquire(t *testing.T) {
	// /new on a file-backed provider must write the fresh session file
	// immediately: the store's deferred flush would otherwise leave the next
	// Acquire resuming the OLD conversation. Carried delivered markers keep
	// pre-/new events deduplicated after the switch.
	provider, _ := newTestLocalProvider(t)
	key := ConversationKey{Platform: "faux", Account: "bot", ChatID: "new"}
	conversation, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	oldFile := conversation.Manager.GetSessionFile()
	mustAppendMarker(t, conversation.Manager, turnMarker{EventID: "ev-old", Phase: phaseDelivered})
	if err := startNewSession(conversation); err != nil {
		t.Fatal(err)
	}
	newFile := conversation.Manager.GetSessionFile()
	if newFile == "" || newFile == oldFile {
		t.Fatalf("session file after /new = %q (old %q)", newFile, oldFile)
	}
	if _, err := os.Stat(newFile); err != nil {
		t.Fatalf("new session file not on disk: %v", err)
	}
	if err := conversation.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	second, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close(context.Background()) }()
	if got := second.Manager.GetSessionFile(); got != newFile {
		t.Fatalf("reacquire resumed %q, want the fresh session %q", got, newFile)
	}
	if ledger := scanTurnLedger(second.Manager, "ev-old"); ledger.delivered == nil {
		t.Fatal("carried delivered marker lost across /new + reacquire")
	}
}

func TestLocalProviderDisablesToolsUnlessHookOverrides(t *testing.T) {
	provider, _ := newTestLocalProvider(t)
	key := ConversationKey{Platform: "faux", Account: "bot", ChatID: "tools"}
	conversation, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if names := conversation.Session.GetActiveToolNames(); len(names) != 0 {
		t.Fatalf("default provider exposes tools: %v", names)
	}
	if err := conversation.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	var hookKey ConversationKey
	var defaultNoTools string
	hooked, _ := newTestLocalProvider(t, WithSessionOptions(func(k ConversationKey, o *codingagent.AgentSessionOptions) {
		hookKey = k
		defaultNoTools = o.NoTools
		o.NoTools = ""
		o.Tools = []string{"read"}
	}))
	conversation, err = hooked.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conversation.Close(context.Background()) }()
	if hookKey != key {
		t.Fatalf("hook key = %+v, want %+v", hookKey, key)
	}
	if defaultNoTools != "all" {
		t.Fatalf("default NoTools = %q, want all", defaultNoTools)
	}
	if names := conversation.Session.GetActiveToolNames(); !reflect.DeepEqual(names, []string{"read"}) {
		t.Fatalf("hooked tools = %v, want [read]", names)
	}
}

func TestLocalProviderEnforcesExclusiveAcquire(t *testing.T) {
	provider, _ := newTestLocalProvider(t)
	key := ConversationKey{Platform: "faux", Account: "bot", ChatID: "excl"}
	first, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Acquire(context.Background(), key); err == nil {
		t.Fatal("double acquire succeeded")
	}
	if err := first.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, err := provider.Acquire(context.Background(), key)
	if err != nil {
		t.Fatalf("re-acquire after close failed: %v", err)
	}
	if err := second.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Acquire(context.Background(), ConversationKey{Platform: "faux"}); err == nil {
		t.Fatal("incomplete key accepted")
	}
}
