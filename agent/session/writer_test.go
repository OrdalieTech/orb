package session

import (
	"encoding/json"
	"testing"
)

func TestInvalidRawJSONIsRejectedWithoutAppending(t *testing.T) {
	manager, err := InMemory(t.TempDir(), WithSessionID("s"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.AppendMessage(json.RawMessage(`{"role":`)); err == nil {
		t.Fatal("invalid raw JSON was accepted")
	}
	if entries := manager.GetEntries(); len(entries) != 0 {
		t.Fatalf("invalid message appended %d entries", len(entries))
	}
}
