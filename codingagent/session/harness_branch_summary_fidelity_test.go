package session_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent/harness"
	sessionstore "github.com/OrdalieTech/pi-go/codingagent/session"
)

type blockingManagerBranchSummaryStorage struct {
	harness.SessionStorage
	reached chan struct{}
	release chan struct{}
}

func (storage *blockingManagerBranchSummaryStorage) AppendEntry(entry harness.SessionTreeEntry) error {
	if entry.Type == "branch_summary" {
		close(storage.reached)
		<-storage.release
	}
	return storage.SessionStorage.AppendEntry(entry)
}

func TestHarnessBackedBranchWithSummaryKeepsRequestedParentAcrossConcurrentAppend(t *testing.T) {
	cwd := t.TempDir()
	base, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
		{
			Type: "message", ID: "root", Timestamp: "2026-07-18T00:00:00.000Z",
			Message: json.RawMessage(`{"role":"user","content":"root"}`),
		},
		{
			Type: "message", ID: "abandoned", ParentID: managerFidelityStringPointer("root"),
			Timestamp: "2026-07-18T00:00:01.000Z",
			Message:   json.RawMessage(`{"role":"assistant","content":"abandoned"}`),
		},
	}, harness.SessionMetadata{
		ID: "manager-branch-parent", CreatedAt: "2026-07-18T00:00:00.000Z", CWD: cwd,
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := &blockingManagerBranchSummaryStorage{
		SessionStorage: base,
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	manager, err := sessionstore.FromHarnessStorage(storage)
	if err != nil {
		t.Fatal(err)
	}

	type branchResult struct {
		id  string
		err error
	}
	done := make(chan branchResult, 1)
	go func() {
		id, branchErr := manager.BranchWithSummary(managerFidelityStringPointer("root"), "summary")
		done <- branchResult{id: id, err: branchErr}
	}()

	released := false
	defer func() {
		if !released {
			close(storage.release)
		}
	}()
	select {
	case <-storage.reached:
	case <-time.After(5 * time.Second):
		t.Fatal("BranchWithSummary did not reach the branch-summary append")
	}
	if _, err := harness.NewSession(storage).AppendMessage(map[string]any{"role": "user", "content": "concurrent"}); err != nil {
		t.Fatal(err)
	}
	close(storage.release)
	released = true

	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	entry, ok := storage.Entry(result.id)
	if !ok {
		t.Fatalf("branch summary %q was not stored", result.id)
	}
	parentID := "<nil>"
	if entry.ParentID != nil {
		parentID = *entry.ParentID
	}
	if parentID != "root" {
		t.Fatalf("branch summary parent = %q, want requested entry root", parentID)
	}
}

func managerFidelityStringPointer(value string) *string {
	return &value
}
