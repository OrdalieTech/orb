package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestSummaryUsagePersistsInUpstreamOrderAndReloads(t *testing.T) {
	cacheWrite1h, reasoning := int64(5), int64(6)
	var usage ai.Usage
	if err := json.Unmarshal([]byte(`{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"cacheWrite1h":5,"reasoning":6,"totalTokens":21,"cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"total":10}}`), &usage); err != nil {
		t.Fatal(err)
	}
	fromHook := true
	entries := []SessionEntry{
		{Type: "compaction", ID: "compact", Timestamp: "2026-07-20T00:00:01Z", Summary: "summary", FirstKeptEntryID: "kept", TokensBefore: 100, Details: []byte(`{"kind":"compact"}`), Usage: &usage, FromHook: &fromHook},
		{Type: "branch_summary", ID: "branch", Timestamp: "2026-07-20T00:00:02Z", FromID: "root", Summary: "branch summary", Details: []byte(`{"kind":"branch"}`), Usage: &usage, FromHook: &fromHook},
	}
	want := []string{
		`{"type":"compaction","id":"compact","parentId":null,"timestamp":"2026-07-20T00:00:01Z","summary":"summary","firstKeptEntryId":"kept","tokensBefore":100,"details":{"kind":"compact"},"usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"cacheWrite1h":5,"reasoning":6,"totalTokens":21,"cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"total":10}},"fromHook":true}`,
		`{"type":"branch_summary","id":"branch","parentId":null,"timestamp":"2026-07-20T00:00:02Z","fromId":"root","summary":"branch summary","details":{"kind":"branch"},"usage":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"cacheWrite1h":5,"reasoning":6,"totalTokens":21,"cost":{"input":1,"output":2,"cacheRead":3,"cacheWrite":4,"total":10}},"fromHook":true}`,
	}

	dir := t.TempDir()
	version := CurrentVersion
	header, err := newHeaderRecord(SessionHeader{Type: "session", Version: &version, ID: "usage", Timestamp: "2026-07-20T00:00:00Z", CWD: dir}).Raw()
	if err != nil {
		t.Fatal(err)
	}
	file := append(header, '\n')
	for index, entry := range entries {
		raw, err := newEntryRecord(entry).Raw()
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != want[index] {
			t.Fatalf("entry %d = %s\nwant    = %s", index, raw, want[index])
		}
		file = append(file, raw...)
		file = append(file, '\n')
	}
	path := filepath.Join(dir, "usage.jsonl")
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.GetEntries()
	if len(got) != 2 {
		t.Fatalf("entries = %d, want 2", len(got))
	}
	for _, entry := range got {
		if entry.Usage == nil || entry.Usage.CacheWrite1h == nil || *entry.Usage.CacheWrite1h != cacheWrite1h || entry.Usage.Reasoning == nil || *entry.Usage.Reasoning != reasoning {
			t.Fatalf("reloaded usage = %#v", entry.Usage)
		}
	}
}
