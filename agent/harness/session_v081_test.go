package harness_test

import (
	"encoding/json"
	"reflect"
	"testing"

	harness "github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
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

func TestV081StorageCursorAndStats(t *testing.T) {
	entries := []harness.SessionTreeEntry{
		{Type: "message", ID: "user", Timestamp: "2026-07-21T00:00:00.000Z", Message: json.RawMessage(`{"role":"user","content":"hi"}`)},
		{Type: "message", ID: "assistant", ParentID: stringPointer("user"), Timestamp: "2026-07-21T00:00:01.000Z", Message: json.RawMessage(`{"role":"assistant","content":[],"api":"x","provider":"x","model":"x","usage":{"input":10,"output":2,"cacheRead":3,"cacheWrite":4,"totalTokens":19,"cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"total":10}},"stopReason":"stop","timestamp":1}`)},
		{Type: "compaction", ID: "compaction", ParentID: stringPointer("assistant"), Timestamp: "2026-07-21T00:00:02.000Z", Summary: "s", TokensBefore: 19, Usage: usagePointer(5, 6, 7, 8, 26, 11)},
		{Type: "branch_summary", ID: "branch", ParentID: stringPointer("compaction"), Timestamp: "2026-07-21T00:00:03.000Z", Summary: "b", Usage: usagePointer(1, 2, 3, 4, 10, 5)},
	}
	storage, err := harness.NewInMemorySessionStorage(entries, harness.SessionMetadata{ID: "session", CreatedAt: "2026-07-21T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	content, err := harness.MarshalSessionJSONL(storage, "/tmp")
	if err != nil {
		t.Fatal(err)
	}
	jsonl, err := harness.RehydrateJSONLSession(content, "/tmp/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	for name, candidate := range map[string]harness.SessionStorage{"memory": storage, "jsonl": jsonl} {
		if got, want := entryIDs(candidate.Entries(harness.SessionEntryCursorOptions{AfterEntrySeq: 1, Limit: intPointer(2)})), []string{"assistant", "compaction"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("%s cursor entries = %v, want %v", name, got, want)
		}
		stats := candidate.SessionStats()
		if stats.MessageCount != 2 || stats.CachedTokens != 13 || stats.UncachedTokens != 32 || stats.TotalTokens != 55 || stats.CostTotal != 26 {
			t.Fatalf("%s session stats = %#v", name, stats)
		}
	}
}

func TestV081SessionStatsSkipPartialUsageObjects(t *testing.T) {
	content := []byte("{\"type\":\"session\",\"version\":3,\"id\":\"session\",\"timestamp\":\"2026-07-21T00:00:00.000Z\",\"cwd\":\"/tmp\"}\n" +
		"{\"type\":\"message\",\"id\":\"assistant\",\"parentId\":null,\"timestamp\":\"2026-07-21T00:00:01.000Z\",\"message\":{\"role\":\"assistant\",\"usage\":{\"input\":10,\"output\":2,\"cacheRead\":3,\"cost\":{\"total\":99}}}}\n" +
		"{\"type\":\"compaction\",\"id\":\"compaction\",\"parentId\":\"assistant\",\"timestamp\":\"2026-07-21T00:00:02.000Z\",\"summary\":\"s\",\"tokensBefore\":10,\"usage\":{\"input\":1,\"cacheRead\":2,\"cacheWrite\":3,\"cost\":{\"total\":9}}}\n")
	storage, err := harness.RehydrateJSONLSession(content, "/tmp/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	stats := storage.SessionStats()
	if stats != (harness.SessionStats{MessageCount: 1}) {
		t.Fatalf("partial usage was counted: %#v", stats)
	}
}

func TestV081JSONLRetainedTailByteOrderAndEmptyCheckpoint(t *testing.T) {
	initial := []byte("{\"type\":\"session\",\"version\":3,\"id\":\"session\",\"timestamp\":\"2026-07-21T00:00:00.000Z\",\"cwd\":\"/tmp\"}\n" +
		"{\"type\":\"message\",\"id\":\"old\",\"parentId\":null,\"timestamp\":\"2026-07-21T00:00:01.000Z\",\"message\":{\"role\":\"user\",\"content\":\"old\",\"timestamp\":1}}\n")
	storage, err := harness.RehydrateJSONLSession(initial, "/tmp/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AppendEntry(harness.SessionTreeEntry{
		Type: "compaction", ID: "checkpoint", ParentID: stringPointer("old"), Timestamp: "2026-07-21T00:00:02.000Z",
		Summary: "summary", TokensBefore: 12, RetainedTail: []json.RawMessage{},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := storage.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	want := string(initial) + `{"type":"compaction","id":"checkpoint","parentId":"old","timestamp":"2026-07-21T00:00:02.000Z","summary":"summary","tokensBefore":12,"retainedTail":[]}` + "\n"
	if string(got) != want {
		t.Fatalf("checkpoint JSONL\ngot:  %s\nwant: %s", got, want)
	}
	rehydrated, err := harness.RehydrateJSONLSession(got, "/tmp/session.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, ok := rehydrated.Entry("checkpoint")
	if !ok || checkpoint.RetainedTail == nil || len(checkpoint.RetainedTail) != 0 {
		t.Fatalf("empty retained tail lost: %#v", checkpoint)
	}
	branch, err := rehydrated.PathToRootOrCompaction(stringPointer("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(branch); !reflect.DeepEqual(got, []string{"checkpoint"}) {
		t.Fatalf("empty-tail checkpoint branch = %v", got)
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

func TestV081SessionStorageContractDoesNotRequireLegacyFullPath(t *testing.T) {
	base, err := harness.NewInMemorySessionStorage([]harness.SessionTreeEntry{{
		Type: "message", ID: "root", Timestamp: "2026-07-21T00:00:00.000Z",
		Message: json.RawMessage(`{"role":"user","content":"root"}`),
	}}, harness.SessionMetadata{ID: "session", CreatedAt: "2026-07-21T00:00:00.000Z"})
	if err != nil {
		t.Fatal(err)
	}
	storage := &v081Storage{delegate: base}
	session := harness.NewSession(storage)
	branch, err := session.Branch("root")
	if err != nil {
		t.Fatal(err)
	}
	if got := entryIDs(branch); !reflect.DeepEqual(got, []string{"root"}) {
		t.Fatalf("branch = %v", got)
	}
}

type v081Storage struct{ delegate harness.SessionStorage }

var _ harness.SessionStorage = (*v081Storage)(nil)

func (storage *v081Storage) Metadata() harness.SessionMetadata { return storage.delegate.Metadata() }
func (storage *v081Storage) LeafID() (*string, error)          { return storage.delegate.LeafID() }
func (storage *v081Storage) SetLeafID(id *string) error        { return storage.delegate.SetLeafID(id) }
func (storage *v081Storage) CreateEntryID() (string, error)    { return storage.delegate.CreateEntryID() }
func (storage *v081Storage) AppendEntry(entry harness.SessionTreeEntry) error {
	return storage.delegate.AppendEntry(entry)
}
func (storage *v081Storage) Entry(id string) (*harness.SessionTreeEntry, bool) {
	return storage.delegate.Entry(id)
}
func (storage *v081Storage) EntriesByType(entryType string) []harness.SessionTreeEntry {
	return storage.delegate.EntriesByType(entryType)
}
func (storage *v081Storage) Label(id string) (string, bool) { return storage.delegate.Label(id) }
func (storage *v081Storage) SessionName() (string, bool)    { return storage.delegate.SessionName() }
func (storage *v081Storage) SessionStats() harness.SessionStats {
	return storage.delegate.SessionStats()
}
func (storage *v081Storage) PathToRootOrCompaction(id *string) ([]harness.SessionTreeEntry, error) {
	return storage.delegate.PathToRootOrCompaction(id)
}
func (storage *v081Storage) Entries(options ...harness.SessionEntryCursorOptions) []harness.SessionTreeEntry {
	return storage.delegate.Entries(options...)
}

func intPointer(value int) *int { return &value }

func usagePointer(input, output, cacheRead, cacheWrite, total int64, cost float64) *ai.Usage {
	return &ai.Usage{
		Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, TotalTokens: total,
		Cost: ai.Cost{Total: cost},
	}
}
