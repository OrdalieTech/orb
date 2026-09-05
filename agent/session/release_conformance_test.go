package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReleasedSessionRepairsAndRestoration(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/F6/release.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Repairs  []struct{ Input, Expected string }
		Entries  []json.RawMessage
		Restored struct {
			SessionID string `json:"sessionId"`
			LeafID    string `json:"leafId"`
			Entries   []json.RawMessage
		}
		Fork []json.RawMessage
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, repair := range fixture.Repairs {
		file := filepath.Join(t.TempDir(), "session.jsonl")
		if err := os.WriteFile(file, []byte(repair.Input), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadEntriesFromFile(file); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != repair.Expected {
			t.Errorf("repair %q: got %q, want %q", repair.Input, got, repair.Expected)
		}
	}
	var lines []string
	for _, entry := range fixture.Entries {
		encoded, err := json.Marshal(entry)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(encoded))
	}
	manager, err := InMemory("/fixture", WithEntries(ParseSessionEntries(strings.Join(lines, "\n"))))
	if err != nil {
		t.Fatal(err)
	}
	if manager.GetSessionID() != fixture.Restored.SessionID || manager.GetLeafID() == nil || *manager.GetLeafID() != fixture.Restored.LeafID {
		t.Fatal("restored identity or leaf differs")
	}
	compare := func(got any, want any) {
		t.Helper()
		encode := func(value any) any {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var decoded any
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatal(err)
			}
			return decoded
		}
		if !reflect.DeepEqual(encode(got), encode(want)) {
			t.Fatalf("got %#v, want %#v", encode(got), encode(want))
		}
	}
	compare(manager.GetEntries(), fixture.Restored.Entries)
	if _, err := manager.CreateBranchedSession("compact"); err != nil {
		t.Fatal(err)
	}
	var retained []SessionEntry
	for _, entry := range manager.GetEntries() {
		if entry.Type != "label" {
			retained = append(retained, entry)
		}
	}
	compare(retained, fixture.Fork)
}
