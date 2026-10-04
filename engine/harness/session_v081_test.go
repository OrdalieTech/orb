package harness_test

import (
	"encoding/json"
	"reflect"
	"testing"

	harness "github.com/OrdalieTech/orb/engine/harness"
)

// v0.81.0 stores the retained context directly on new compaction entries. The
// ancestry walk must stop at that checkpoint so pre-compaction history cannot
// leak back into context or targeted forks.
func TestV081RetainedTailCompactionIsCheckpoint(t *testing.T) {
	old := harness.SessionTreeEntry{
		Type: "message", ID: "old", Timestamp: "2026-07-21T00:00:00.000Z",
		Message: json.RawMessage(`{"role":"user","content":"old","timestamp":1}`),
	}
	kept := harness.SessionTreeEntry{
		Type: "message", ID: "kept", ParentID: stringPointer("old"), Timestamp: "2026-07-21T00:00:01.000Z",
		Message: json.RawMessage(`{"role":"user","content":"kept","timestamp":2}`),
	}
	recent := harness.SessionTreeEntry{
		Type: "message", ID: "recent", ParentID: stringPointer("kept"), Timestamp: "2026-07-21T00:00:02.000Z",
		Message: json.RawMessage(`{"role":"assistant","content":[],"api":"x","provider":"x","model":"x","usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":10,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1}`),
	}
	checkpoint := harness.SessionTreeEntry{
		Type: "compaction", ID: "checkpoint", ParentID: stringPointer("recent"), Timestamp: "2026-07-21T00:00:03.000Z",
		Summary: "summary", FirstKeptEntryID: "kept", TokensBefore: 100,
		RetainedTail: []json.RawMessage{kept.Message, recent.Message},
	}
	post := harness.SessionTreeEntry{
		Type: "message", ID: "post", ParentID: stringPointer("checkpoint"), Timestamp: "2026-07-21T00:00:04.000Z",
		Message: json.RawMessage(`{"role":"user","content":"post","timestamp":3}`),
	}
	storage, err := harness.NewInMemorySessionStorage(
		[]harness.SessionTreeEntry{old, kept, recent, checkpoint, post},
		harness.SessionMetadata{ID: "session", CreatedAt: "2026-07-21T00:00:00.000Z"},
	)
	if err != nil {
		t.Fatal(err)
	}
	session := harness.NewSession(storage)
	branch, err := session.Branch()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(branch), []string{"checkpoint", "post"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("checkpoint branch = %v, want %v", got, want)
	}
	context, err := session.Context()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(context.Messages)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `[{"role":"compactionSummary","summary":"summary","tokensBefore":100,"timestamp":1784592003000},{"role":"user","content":"kept","timestamp":2},{"role":"assistant","content":[],"api":"x","provider":"x","model":"x","usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":10,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":1},{"role":"user","content":"post","timestamp":3}]`; got != want {
		t.Fatalf("checkpoint context = %s\nwant: %s", got, want)
	}
	forked, err := harness.EntriesToFork(storage, "post", harness.ForkAt)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(forked), []string{"checkpoint", "post"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("checkpoint fork = %v, want %v", got, want)
	}
}

func TestV081LegacyCompactionFallsBackToFirstKeptEntry(t *testing.T) {
	entries := []harness.SessionTreeEntry{
		{Type: "message", ID: "old", Timestamp: "2026-07-21T00:00:00.000Z", Message: json.RawMessage(`{"role":"user","content":"old","timestamp":1}`)},
		{Type: "message", ID: "kept", ParentID: stringPointer("old"), Timestamp: "2026-07-21T00:00:01.000Z", Message: json.RawMessage(`{"role":"user","content":"kept","timestamp":2}`)},
		{Type: "message", ID: "recent", ParentID: stringPointer("kept"), Timestamp: "2026-07-21T00:00:02.000Z", Message: json.RawMessage(`{"role":"assistant","content":[],"api":"x","provider":"x","model":"x","usage":{"input":1,"output":0,"cacheRead":0,"cacheWrite":0,"totalTokens":1,"cost":{"input":0,"output":0,"cacheRead":0,"cacheWrite":0,"total":0}},"stopReason":"stop","timestamp":3}`)},
		{Type: "compaction", ID: "legacy", ParentID: stringPointer("recent"), Timestamp: "2026-07-21T00:00:03.000Z", Summary: "summary", FirstKeptEntryID: "kept", TokensBefore: 10},
		{Type: "message", ID: "post", ParentID: stringPointer("legacy"), Timestamp: "2026-07-21T00:00:04.000Z", Message: json.RawMessage(`{"role":"user","content":"post","timestamp":4}`)},
	}
	storage, err := harness.NewInMemorySessionStorage(entries, harness.SessionMetadata{ID: "session", CreatedAt: "2026-07-21T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	branch, err := storage.PathToRootOrCompaction(stringPointer("post"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := entryIDs(branch), []string{"kept", "recent", "legacy", "post"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy branch = %v, want %v", got, want)
	}
}
