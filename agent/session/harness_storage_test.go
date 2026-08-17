package session_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/engine/harness"
)

func TestHarnessStorageBecomesAByteExactSessionManager(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "F6Harness", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := harness.RehydrateJSONLSession(input, filepath.Join(t.TempDir(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(cwd))
	if err != nil {
		t.Fatal(err)
	}

	if got := manager.GetSessionID(); got != "session-fixed" {
		t.Fatalf("session id = %q", got)
	}
	if got := manager.GetCWD(); got != cwd {
		t.Fatalf("cwd = %q, want %q", got, cwd)
	}
	if leaf := manager.GetLeafID(); leaf == nil || *leaf != "branch-summary" {
		t.Fatalf("leaf = %v, want branch-summary", leaf)
	}
	got, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, input) {
		t.Fatal("binding changed the rehydrated JSONL bytes")
	}
}

func TestHarnessStorageAndSessionManagerShareLiveWrites(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "F6Harness", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := harness.RehydrateJSONLSession(input, filepath.Join(t.TempDir(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	leaf, err := storage.LeafID()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "message", ID: "external-user", ParentID: leaf, Timestamp: "2026-02-03T04:05:22.000Z",
		Message: json.RawMessage(`{"role":"user","content":[{"type":"text","text":"external"}],"timestamp":5}`),
	}); err != nil {
		t.Fatal(err)
	}
	entries := manager.GetEntries()
	if got := entries[len(entries)-1].ID; got != "external-user" {
		t.Fatalf("manager did not observe storage append: last id = %q", got)
	}

	if _, err := manager.AppendSessionInfo("  live\nname  "); err != nil {
		t.Fatal(err)
	}
	if name, ok := storage.SessionName(); !ok || name != "live name" {
		t.Fatalf("storage did not observe manager rename: %q, ok=%v", name, ok)
	}
	if name := manager.GetSessionName(); name == nil || *name != "live name" {
		t.Fatalf("manager session name = %v, want live name", name)
	}

	if err := manager.Branch("root-user"); err != nil {
		t.Fatal(err)
	}
	storageLeaf, err := storage.LeafID()
	if err != nil {
		t.Fatal(err)
	}
	if storageLeaf == nil || *storageLeaf != "root-user" {
		t.Fatalf("storage leaf = %v, want root-user", storageLeaf)
	}
	managerBytes, err := manager.JSONL()
	if err != nil {
		t.Fatal(err)
	}
	storageBytes, err := storage.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(managerBytes, storageBytes) {
		t.Fatal("manager and storage returned different live JSONL bytes")
	}
}

func TestHarnessStorageRestoresActiveToolsIntoSessionContext(t *testing.T) {
	input, err := os.ReadFile(filepath.Join("..", "..", "conformance", "fixtures", "F6Harness", "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	storage, err := harness.RehydrateJSONLSession(input, filepath.Join(t.TempDir(), "session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage, sessionstore.WithCwdOverride(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}

	contextState := manager.BuildSessionContext()
	if got := contextState.ActiveToolNames; got == nil || len(got) != 0 {
		t.Fatalf("active tools = %v, want explicit empty state", got)
	}
}

func TestV081CodingManagerKeepsItsFullBranchWithoutLegacyStorageAPI(t *testing.T) {
	rootID := "root"
	keptID := "kept"
	storage, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
		{Type: "message", ID: rootID, Timestamp: "2026-07-21T00:00:00.000Z", Message: json.RawMessage(`{"role":"user","content":"old"}`)},
		{Type: "message", ID: keptID, ParentID: &rootID, Timestamp: "2026-07-21T00:00:01.000Z", Message: json.RawMessage(`{"role":"user","content":"kept"}`)},
		{
			Type: "compaction", ID: "checkpoint", ParentID: &keptID, Timestamp: "2026-07-21T00:00:02.000Z",
			Summary: "summary", FirstKeptEntryID: keptID, TokensBefore: 10, RetainedTail: []json.RawMessage{},
		},
	}, harness.SessionMetadata{ID: "session", CreatedAt: "2026-07-21T00:00:00.000Z", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := sessionstore.FromHarnessStorage(storage)
	if err != nil {
		t.Fatal(err)
	}
	branch := manager.GetBranch("checkpoint")
	if len(branch) != 3 || branch[0].ID != rootID || branch[1].ID != keptID || branch[2].ID != "checkpoint" {
		t.Fatalf("coding branch = %#v", branch)
	}
}
