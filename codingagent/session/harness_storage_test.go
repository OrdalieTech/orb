package session_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pigo/agent/harness"
	sessionstore "github.com/OrdalieTech/pigo/codingagent/session"
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
	if leaf := manager.GetLeafID(); leaf == nil || *leaf != "tools-empty" {
		t.Fatalf("leaf = %v, want tools-empty", leaf)
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

	nameID, err := manager.AppendSessionInfo("  live\nname  ")
	if err != nil {
		t.Fatal(err)
	}
	nameEntry, ok := storage.Entry(nameID)
	if !ok || nameEntry.Type != "session_info" || nameEntry.Name != "live name" {
		t.Fatalf("storage did not observe manager append: %+v, exists=%v", nameEntry, ok)
	}
	if nameEntry.ParentID == nil || *nameEntry.ParentID != "external-user" {
		t.Fatalf("manager append parent = %v, want external-user", nameEntry.ParentID)
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
