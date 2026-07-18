package harness_test

import (
	"bytes"
	"encoding/json"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/agent/harness"
	"github.com/OrdalieTech/pi-go/ai"
)

type blockingBranchSummaryStorage struct {
	harness.SessionStorage
	reached chan struct{}
	release chan struct{}
}

type blockingSiblingAppendStorage struct {
	harness.SessionStorage
	reached chan struct{}
	release chan struct{}
}

func (storage *blockingSiblingAppendStorage) AppendEntry(entry harness.SessionTreeEntry) error {
	if entry.Type == "message" && entry.ID != "root" {
		storage.reached <- struct{}{}
		<-storage.release
	}
	return storage.SessionStorage.AppendEntry(entry)
}

func TestConcurrentSessionAppendsKeepUpstreamSiblingParents(t *testing.T) {
	base, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{{
		Type: "message", ID: "root", Timestamp: "2026-07-18T00:00:00.000Z",
		Message: json.RawMessage(`{"role":"user","content":"root"}`),
	}}, harness.SessionMetadata{ID: "siblings", CreatedAt: "2026-07-18T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	storage := &blockingSiblingAppendStorage{
		SessionStorage: base, reached: make(chan struct{}, 2), release: make(chan struct{}),
	}
	start := make(chan struct{})
	errors := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, appendErr := harness.NewSession(storage).AppendMessage(map[string]any{
				"role": "user", "content": index,
			})
			errors <- appendErr
		}(index)
	}
	close(start)

	reached := 0
	completed := 0
	for reached < 2 && completed < 2 {
		select {
		case <-storage.reached:
			reached++
			if reached == 2 {
				close(storage.release)
			}
		case appendErr := <-errors:
			completed++
			if appendErr != nil {
				t.Fatal(appendErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent appends did not complete")
		}
	}
	wait.Wait()
	for completed < 2 {
		if appendErr := <-errors; appendErr != nil {
			t.Fatal(appendErr)
		}
		completed++
	}

	messages := storage.EntriesByType("message")
	if len(messages) != 3 {
		t.Fatalf("physical messages = %d, want root plus two appends", len(messages))
	}
	for _, entry := range messages[1:] {
		if entry.ParentID == nil || *entry.ParentID != "root" {
			t.Fatalf("concurrent message %q parent = %v, want sibling parent root", entry.ID, entry.ParentID)
		}
	}
}

func (storage *blockingBranchSummaryStorage) AppendEntry(entry harness.SessionTreeEntry) error {
	if entry.Type == "branch_summary" {
		close(storage.reached)
		<-storage.release
	}
	return storage.SessionStorage.AppendEntry(entry)
}

func TestSessionMoveToKeepsRequestedBranchSummaryParentAcrossConcurrentAppend(t *testing.T) {
	base, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{
		{
			Type: "message", ID: "root", Timestamp: "2026-07-18T00:00:00.000Z",
			Message: json.RawMessage(`{"role":"user","content":"root"}`),
		},
		{
			Type: "message", ID: "abandoned", ParentID: fidelityStringPointer("root"),
			Timestamp: "2026-07-18T00:00:01.000Z",
			Message:   json.RawMessage(`{"role":"assistant","content":"abandoned"}`),
		},
	}, harness.SessionMetadata{ID: "branch-parent", CreatedAt: "2026-07-18T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	storage := &blockingBranchSummaryStorage{
		SessionStorage: base,
		reached:        make(chan struct{}),
		release:        make(chan struct{}),
	}
	session := harness.NewSession(storage)

	type moveResult struct {
		id  string
		err error
	}
	done := make(chan moveResult, 1)
	go func() {
		id, moveErr := session.MoveTo(fidelityStringPointer("root"), &harness.BranchSummary{Summary: "summary"})
		done <- moveResult{id: id, err: moveErr}
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
		t.Fatal("MoveTo did not reach the branch-summary append")
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

func TestEmptySessionContextKeepsMessagesAsAnEmptyArray(t *testing.T) {
	storage, err := harness.NewInMemorySessionStorage(nil, harness.SessionMetadata{
		ID: "empty-context", CreatedAt: "2026-07-18T00:00:00.000Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	contextState, err := harness.NewSession(storage).Context()
	if err != nil {
		t.Fatal(err)
	}
	if contextState.Messages == nil {
		t.Fatal("empty context messages are nil, want an explicit empty array")
	}
	encoded, err := json.Marshal(contextState)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"thinkingLevel":"off","model":null,"activeToolNames":null,"messages":[]}`
	if string(encoded) != want {
		t.Fatalf("empty context JSON = %s, want %s", encoded, want)
	}
}

func TestSessionJSONLAppendsUseJSONStringifyForNonFiniteNumbers(t *testing.T) {
	const header = `{"type":"session","version":3,"id":"non-finite","timestamp":"2026-07-18T00:00:00.000Z","cwd":"/tmp"}` + "\n"

	t.Run("NaN tokensBefore becomes null without a panic", func(t *testing.T) {
		storage, err := harness.RehydrateJSONLSession([]byte(header), "/tmp/non-finite.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("AppendCompaction panicked for NaN tokensBefore: %v", recovered)
			}
		}()
		if _, err := harness.NewSession(storage).AppendCompaction("summary", "kept", math.NaN(), nil, nil); err != nil {
			t.Fatal(err)
		}
		content, err := storage.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(content, []byte(`"tokensBefore":null`)) {
			t.Fatalf("JSONL compaction = %s, want tokensBefore:null", content)
		}
	})

	t.Run("infinite details becomes null", func(t *testing.T) {
		storage, err := harness.RehydrateJSONLSession([]byte(header), "/tmp/non-finite.jsonl")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := harness.NewSession(storage).AppendCompaction("summary", "kept", 1, math.Inf(1), nil); err != nil {
			t.Fatal(err)
		}
		content, err := storage.Bytes()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(content, []byte(`"details":null`)) {
			t.Fatalf("JSONL compaction = %s, want details:null", content)
		}
	})
}

func TestSessionContextProjectsStandardMessagesAsAIMessages(t *testing.T) {
	contextState := harness.BuildSessionContext([]harness.SessionTreeEntry{{
		Type: "message", ID: "user", Timestamp: "2026-07-18T00:00:00.000Z",
		Message: json.RawMessage(`{"role":"user","content":"hello","timestamp":1}`),
	}})
	if len(contextState.Messages) != 1 {
		t.Fatalf("context messages = %d, want 1", len(contextState.Messages))
	}
	if _, ok := contextState.Messages[0].(ai.Message); !ok {
		t.Fatalf("context message type = %T, want ai.Message", contextState.Messages[0])
	}
}

func TestSessionContextAloneOmitsEmptyBranchSummaries(t *testing.T) {
	contextState := harness.BuildSessionContext([]harness.SessionTreeEntry{{
		Type: "branch_summary", ID: "empty-summary", Timestamp: "2026-07-18T00:00:00.000Z",
		FromID: "root", Summary: "",
	}})
	if len(contextState.Messages) != 0 {
		t.Errorf("session context messages = %d, want empty branch summary omitted", len(contextState.Messages))
	}

	compactionMessages := harness.ContextMessages([]harness.SessionEntry{{
		Type: "branch_summary", ID: "empty-summary", Timestamp: "2026-07-18T00:00:00.000Z",
		FromID: "root", Summary: "",
	}})
	if len(compactionMessages) != 1 {
		t.Fatalf("compaction context messages = %d, want empty branch summary preserved", len(compactionMessages))
	}
}

func fidelityStringPointer(value string) *string {
	return &value
}
