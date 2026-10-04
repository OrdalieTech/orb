package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEntriesFromFileValidatesFirstParsedRecordAndStreamsLongLines(t *testing.T) {
	dir := t.TempDir()
	invalid := filepath.Join(dir, "invalid.jsonl")
	if err := os.WriteFile(invalid, []byte("broken\n42\n{\"type\":\"session\",\"id\":\"s\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadEntriesFromFile(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatalf("invalid leading parsed record returned %d entries", len(entries))
	}

	unicodeWrapped := filepath.Join(dir, "unicode-wrapped.jsonl")
	if err := os.WriteFile(unicodeWrapped, []byte("\u00a0{\"type\":\"session\",\"id\":\"s\"}\u1680\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = LoadEntriesFromFile(unicodeWrapped)
	if err != nil {
		t.Fatal(err)
	}
	if entries != nil {
		t.Fatalf("Unicode-wrapped header returned %d entries", len(entries))
	}

	long := filepath.Join(dir, "long.jsonl")
	content := "{\"type\":\"session\",\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/tmp\"}\n" +
		"{\"type\":\"custom\",\"payload\":\"" + strings.Repeat("x", sessionReadBufferSize+17) + "\"}\n"
	if err := os.WriteFile(long, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err = LoadEntriesFromFile(long)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("long-line load returned %d entries, want 2", len(entries))
	}
}

func TestOpenRewritesMigratedJSONLWithJSONStringifyNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v2.jsonl")
	input := `{ "type" : "session", "version" : 2e0, "id" : "normalize", "timestamp" : "t", "cwd" : "\u002flegacy" }` + "\n" +
		`{"type":"message","id":"keep","parentId":null,"timestamp":"t","message": { "role" : "user", "content" : "\u0061\/b", "number" : 1e+2 } }` + "\n"
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"type\":\"session\",\"version\":3,\"id\":\"normalize\",\"timestamp\":\"t\",\"cwd\":\"/legacy\"}\n" +
		"{\"type\":\"message\",\"id\":\"keep\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":\"a/b\",\"number\":100}}\n"
	if string(got) != want {
		t.Fatalf("rewritten migration mismatch\ngot:  %s\nwant: %s", got, want)
	}
}
