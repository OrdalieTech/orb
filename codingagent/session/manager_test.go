package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestPersistedSessionFlushesOnFirstAssistant(t *testing.T) {
	dir := t.TempDir()
	now := fixedTestTime(t)
	manager, err := Create(
		dir,
		filepath.Join(dir, "sessions"),
		WithSessionID("session-fixed"),
		WithClock(func() time.Time { return now }),
		WithEntryIDGenerator(sequenceIDGenerator("00000001", "00000002", "00000003")),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := manager.GetSessionFile()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new session file exists before messages: %v", err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"hello"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session file exists before assistant: %v", err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"assistant","content":[]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("session file missing after assistant: %v", err)
	}
	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("lock sidecar missing: %v", err)
	}
	entries, err := LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("persisted entries = %d, want header + 2 messages", len(entries))
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"again"}`)); err != nil {
		t.Fatal(err)
	}
	entries, err = LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("post-flush entries = %d, want 4", len(entries))
	}
}

func TestOpenHandlesEmptyInvalidAndMissingFiles(t *testing.T) {
	dir := t.TempDir()
	now := fixedTestTime(t)
	options := []Option{
		WithClock(func() time.Time { return now }),
		WithSessionIDGenerator(func(time.Time) (string, error) { return "opened", nil }),
		WithEntryIDGenerator(sequenceIDGenerator("00000001")),
	}

	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := Open(empty, "", options...)
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionFile() != empty || manager.GetSessionID() != "opened" {
		t.Fatalf("opened empty file as path %q id %q", manager.GetSessionFile(), manager.GetSessionID())
	}
	entries, err := LoadEntriesFromFile(empty)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Header == nil {
		t.Fatalf("empty file was not initialized: %#v", entries)
	}

	invalid := filepath.Join(dir, "invalid.jsonl")
	original := []byte("this is not a session\n")
	if err := os.WriteFile(invalid, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(invalid, "", options...); err == nil || !strings.Contains(err.Error(), "Session file is not a valid pi session") {
		t.Fatalf("invalid file error = %v", err)
	}
	unchanged, err := os.ReadFile(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if string(unchanged) != string(original) {
		t.Fatalf("invalid file was modified: %q", unchanged)
	}

	missing := filepath.Join(dir, "missing.jsonl")
	missingManager, err := Open(missing, "", options...)
	if err != nil {
		t.Fatal(err)
	}
	if missingManager.GetSessionFile() != missing {
		t.Fatalf("missing explicit path = %q, want %q", missingManager.GetSessionFile(), missing)
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing session was eagerly created: %v", err)
	}
}

func TestOpenHandlesSessionsBeyondHeaderDiscoveryLimit(t *testing.T) {
	dir := t.TempDir()
	storedCWD := filepath.Join(dir, "stored")
	overrideCWD := filepath.Join(dir, "override")
	for _, test := range []struct {
		name, id, prefix string
	}{
		{name: "large-header", id: strings.Repeat("a", maxSessionHeaderScanBytes+1)},
		{name: "large-prefix", id: "large-prefix", prefix: strings.Repeat("x", maxSessionHeaderScanBytes+1) + "\n"},
	} {
		path := filepath.Join(dir, test.name+".jsonl")
		header := fmt.Sprintf(`{"type":"session","version":3,"id":%q,"timestamp":"2026-07-20T00:00:00Z","cwd":%q}`, test.id, storedCWD)
		if err := os.WriteFile(path, []byte(test.prefix+header+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		manager, err := Open(path, dir)
		if err != nil {
			t.Fatal(err)
		}
		if manager.GetSessionID() != test.id || manager.GetCwd() != storedCWD {
			t.Fatalf("%s opened id=%q cwd=%q", test.name, manager.GetSessionID(), manager.GetCwd())
		}
		manager, err = Open(path, dir, WithCwdOverride(overrideCWD))
		if err != nil {
			t.Fatal(err)
		}
		if manager.GetSessionID() != test.id || manager.GetCwd() != overrideCWD {
			t.Fatalf("%s override id=%q cwd=%q", test.name, manager.GetSessionID(), manager.GetCwd())
		}
	}
}

func TestBranchTraversalLabelsAndLeafSurviveOpen(t *testing.T) {
	dir := t.TempDir()
	now := fixedTestTime(t)
	manager, err := Create(
		dir,
		filepath.Join(dir, "sessions"),
		WithSessionID("tree"),
		WithClock(func() time.Time { return now }),
		WithEntryIDGenerator(sequenceIDGenerator(
			"00000001", "00000002", "00000003", "00000004", "00000005", "00000006",
		)),
	)
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"root"}`))
	if err != nil {
		t.Fatal(err)
	}
	originalChild, err := manager.AppendMessage(json.RawMessage(`{"role":"assistant","content":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Branch(root); err != nil {
		t.Fatal(err)
	}
	branchChild, err := manager.AppendModelChange("openai", "branch")
	if err != nil {
		t.Fatal(err)
	}
	manager.ResetLeaf()
	secondRoot, err := manager.AppendThinkingLevelChange("low")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := manager.BranchWithSummary(&root, "alternate")
	if err != nil {
		t.Fatal(err)
	}
	label := "checkpoint"
	labelEntry, err := manager.AppendLabelChange(root, &label)
	if err != nil {
		t.Fatal(err)
	}

	children := manager.GetChildren(root)
	if got := entryIDs(children); got != strings.Join([]string{originalChild, branchChild, summary}, ",") {
		t.Fatalf("children = %s", got)
	}
	if got := entryIDs(manager.GetBranch()); got != strings.Join([]string{root, summary, labelEntry}, ",") {
		t.Fatalf("active branch = %s", got)
	}
	if got := manager.GetLabel(root); got == nil || *got != label {
		t.Fatalf("label = %v", got)
	}
	tree := manager.GetTree()
	if len(tree) != 2 || tree[0].Entry.ID != root || tree[1].Entry.ID != secondRoot {
		t.Fatalf("roots = %#v", tree)
	}
	if tree[0].Label == nil || *tree[0].Label != label {
		t.Fatalf("tree label = %v", tree[0].Label)
	}

	reopened, err := Open(manager.GetSessionFile(), "")
	if err != nil {
		t.Fatal(err)
	}
	if leaf := reopened.GetLeafID(); leaf == nil || *leaf != labelEntry {
		t.Fatalf("reopened leaf = %v, want %s", leaf, labelEntry)
	}
	if got := reopened.GetLabel(root); got == nil || *got != label {
		t.Fatalf("reopened label = %v", got)
	}
}

func entryIDs(entries []SessionEntry) string {
	ids := make([]string, len(entries))
	for index := range entries {
		ids[index] = entries[index].ID
	}
	return strings.Join(ids, ",")
}

func TestLatestCompactionTimestampFollowsActiveBranch(t *testing.T) {
	now := fixedTestTime(t)
	manager, err := InMemory(t.TempDir(), WithClock(func() time.Time {
		now = now.Add(time.Second)
		return now
	}))
	if err != nil {
		t.Fatal(err)
	}
	root, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":"root"}`))
	if err != nil {
		t.Fatal(err)
	}
	compaction, err := manager.AppendCompaction("summary", root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"assistant","content":[]}`)); err != nil {
		t.Fatal(err)
	}
	want := manager.GetEntry(compaction).Timestamp
	if got, ok := manager.GetLatestCompactionTimestamp(); !ok || got != want {
		t.Fatalf("latest compaction = %q, %v; want %q", got, ok, want)
	}
	if err := manager.Branch(root); err != nil {
		t.Fatal(err)
	}
	if got, ok := manager.GetLatestCompactionTimestamp(); ok {
		t.Fatalf("root branch latest compaction = %q", got)
	}
}

func BenchmarkLatestCompactionLookup(b *testing.B) {
	const historyEntries = 20_000
	now := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	manager, err := InMemory(b.TempDir(),
		WithClock(func() time.Time {
			now = now.Add(time.Millisecond)
			return now
		}),
		WithEntryIDGenerator(prefixedIDGenerator("bench")),
	)
	if err != nil {
		b.Fatal(err)
	}
	messages := [...]json.RawMessage{
		json.RawMessage(`{"role":"user","content":"Inspect the current implementation and report the smallest correct change."}`),
		json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"The implementation is correct and the focused checks pass."}]}`),
	}
	root, err := manager.AppendMessage(messages[0])
	if err != nil {
		b.Fatal(err)
	}
	latestID := ""
	for index := 1; index < historyEntries; index++ {
		if index%5_000 == 0 {
			latestID, err = manager.AppendCompaction("Compacted session history.", root, int64(index*128))
		} else {
			_, err = manager.AppendMessage(messages[index%len(messages)])
		}
		if err != nil {
			b.Fatal(err)
		}
	}
	want := manager.GetEntry(latestID).Timestamp

	b.Run("FormerGetBranchReverseScan", func(b *testing.B) {
		b.ReportAllocs()
		var got string
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			latest := GetLatestCompactionEntry(manager.GetBranch())
			if latest != nil {
				got = latest.Timestamp
			}
		}
		if got != want {
			b.Fatalf("latest compaction = %q, want %q", got, want)
		}
	})

	b.Run("GetLatestCompactionTimestamp", func(b *testing.B) {
		b.ReportAllocs()
		var got string
		var ok bool
		b.ResetTimer()
		for index := 0; index < b.N; index++ {
			got, ok = manager.GetLatestCompactionTimestamp()
		}
		if !ok || got != want {
			b.Fatalf("latest compaction = %q, %v; want %q", got, ok, want)
		}
	})
}

func TestNewSessionResetsStateAndValidatesExplicitID(t *testing.T) {
	manager, err := InMemory(t.TempDir(), WithSessionID("first"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendCustomEntry("state"); err != nil {
		t.Fatal(err)
	}
	bad := "-bad"
	if _, err := manager.NewSession(NewSessionOptions{ID: &bad}); err == nil {
		t.Fatal("invalid explicit session id was accepted")
	}
	valid := "second.session"
	parent := "/parent.jsonl"
	if _, err := manager.NewSession(NewSessionOptions{ID: &valid, ParentSession: &parent}); err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionID() != valid || len(manager.GetEntries()) != 0 || manager.GetLeafID() != nil {
		t.Fatalf("new session did not reset state: id=%q entries=%d leaf=%v", manager.GetSessionID(), len(manager.GetEntries()), manager.GetLeafID())
	}
	header := manager.GetHeader()
	if header == nil || header.ParentSession == nil || *header.ParentSession != parent {
		t.Fatalf("new header = %#v", header)
	}
}

func TestConcurrentManagersAppendValidLockedJSONL(t *testing.T) {
	dir := t.TempDir()
	manager, err := Create(
		dir,
		filepath.Join(dir, "sessions"),
		WithSessionID("concurrent"),
		WithEntryIDGenerator(sequenceIDGenerator("00000001")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"assistant","content":[]}`)); err != nil {
		t.Fatal(err)
	}
	path := manager.GetSessionFile()
	left, err := Open(path, "", WithEntryIDGenerator(prefixedIDGenerator("a")))
	if err != nil {
		t.Fatal(err)
	}
	right, err := Open(path, "", WithEntryIDGenerator(prefixedIDGenerator("b")))
	if err != nil {
		t.Fatal(err)
	}

	const perManager = 40
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	appendMany := func(manager *SessionManager, prefix string) {
		defer wait.Done()
		for index := 0; index < perManager; index++ {
			if _, err := manager.AppendCustomEntry(prefix, index); err != nil {
				errors <- err
				return
			}
		}
	}
	wait.Add(2)
	go appendMany(left, "left")
	go appendMany(right, "right")
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	entries, err := LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := 2 + 2*perManager; len(entries) != want {
		t.Fatalf("concurrent file has %d entries, want %d", len(entries), want)
	}
}

func prefixedIDGenerator(prefix string) IDGenerator {
	index := 0
	return func() (string, error) {
		index++
		return fmt.Sprintf("%s%07d", prefix, index), nil
	}
}

func TestFileLockSerializesCriticalSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- withFileLock(path, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	started := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(started)
		secondDone <- withFileLock(path, func() error { return nil })
	}()
	<-started
	select {
	case err := <-secondDone:
		t.Fatalf("second lock entered before first released: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second lock did not enter after release")
	}
}
