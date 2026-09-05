package runner_test

import (
	"context"
	"encoding/json"
	"github.com/OrdalieTech/orb/conformance/runner"
	agentharness "github.com/OrdalieTech/orb/engine/harness"
	"os"
	"path/filepath"
	"testing"
)

func TestF6HarnessReleasedTransactionsUsePrimaryStorage(t *testing.T) {
	var fixture struct {
		Header                agentharness.SessionV4TransactionHeader
		InitialWrites, Writes []json.RawMessage
		Timestamp             int64
		Content               string
		Entries               []json.RawMessage
	}
	runner.LoadJSON(t, "F6HarnessTransactions", "files.json", &fixture)
	root := t.TempDir()
	env := &agentharness.NodeExecutionEnv{CWD: root}
	ctx := context.Background()
	path := filepath.Join(root, "session.jsonl")
	storage, err := agentharness.CreateJSONLSessionV4Storage(ctx, env, path, agentharness.SessionV4Header{ID: fixture.Header.ID, CreatedAt: fixture.Header.CreatedAt, CWD: fixture.Header.CWD})
	if err != nil {
		t.Fatal(err)
	}
	storage.Now = func() int64 { return fixture.Timestamp }
	for _, writes := range [][]json.RawMessage{fixture.InitialWrites, fixture.Writes} {
		if _, err = storage.Commit(ctx, writes); err != nil {
			t.Fatal(err)
		}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := runner.ByteDiff([]byte(fixture.Content), content); diff != "" {
		t.Fatal(diff)
	}
	if _, err = storage.AppendRecord(json.RawMessage(`{"type":"operation_started"}`)); err == nil {
		t.Fatal("removed record API accepted")
	}
	if _, err = storage.AppendEntry(json.RawMessage(`{"type":"model_change","id":"removed","parentId":null}`), "main"); err == nil {
		t.Fatal("removed config entry accepted")
	}
	if err = storage.Close(ctx); err != nil {
		t.Fatal(err)
	}
	reopened, err := agentharness.LoadJSONLSessionV4Storage(ctx, env, path)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.ScanEntries(agentharness.SessionV4Scan{})
	if err != nil {
		t.Fatal(err)
	}
	runner.AssertCanonicalJSONEqual(t, fixture.Entries, entries, "transaction entries")
	if err = reopened.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
