package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Upstream preserves the header id verbatim on load ("header?.id ??
// createSessionId()"), including an explicit empty string; orb previously
// regenerated a fresh UUID for "".
func TestOpenPreservesEmptyHeaderID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "2025-01-02T03-04-05-006Z_empty.jsonl")
	encodedCWD, err := json.Marshal(dir)
	if err != nil {
		t.Fatal(err)
	}
	header := `{"type":"session","version":3,"id":"","timestamp":"2025-01-02T03:04:05.006Z","cwd":` + string(encodedCWD) + `}` + "\n"
	if err := os.WriteFile(path, []byte(header), 0o644); err != nil {
		t.Fatal(err)
	}
	manager, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	if id := manager.GetSessionID(); id != "" {
		t.Fatalf("session id = %q, want empty header id preserved", id)
	}
}
