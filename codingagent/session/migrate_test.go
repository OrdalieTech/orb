package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestParseSessionEntriesSkipsOnlyBlankAndMalformedLines(t *testing.T) {
	entries := ParseSessionEntries("  \nnot-json\n42\nnull\n{\"type\":\"session\",\"id\":\"s\"}\n")
	if len(entries) != 3 {
		t.Fatalf("parsed %d valid JSON lines, want 3", len(entries))
	}
	for index, want := range []string{"42", "null"} {
		got, err := entries[index].Raw()
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("entry %d = %s, want %s", index, got, want)
		}
	}
	if entries[2].Header == nil || entries[2].Header.ID != "s" {
		t.Fatalf("header = %#v", entries[2].Header)
	}
}

func TestParseSessionEntriesDoesNotTrimUnicodeWhitespaceAroundJSON(t *testing.T) {
	input := "\u00a0{\"type\":\"session\",\"id\":\"nbsp\"}\u00a0\n" +
		"\u1680{\"type\":\"session\",\"id\":\"ogham\"}\u1680\n" +
		" \t{\"type\":\"session\",\"id\":\"json-space\"}\r "
	entries := ParseSessionEntries(input)
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want only the JSON-whitespace-wrapped record", len(entries))
	}
	if entries[0].Header == nil || entries[0].Header.ID != "json-space" {
		t.Fatalf("parsed header = %#v", entries[0].Header)
	}

	entries = ParseSessionEntries("\ufeff\u00a0{\"type\":\"session\",\"id\":\"outer-trim\"}\u1680\ufeff")
	if len(entries) != 1 || entries[0].Header == nil || entries[0].Header.ID != "outer-trim" {
		t.Fatalf("outer-trimmed entries = %#v", entries)
	}
	if entries = ParseSessionEntries("\u0085{\"type\":\"session\",\"id\":\"next-line\"}\u0085"); len(entries) != 0 {
		t.Fatalf("U+0085-wrapped JSON parsed %d entries", len(entries))
	}
}

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

func TestLoadEntriesFromFileDecodesInvalidUTF8LikeNodeStringDecoder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "invalid-utf8.jsonl")
	contents := append([]byte("{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/tmp\"}\n"+
		"{\"type\":\"message\",\"id\":\"m\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":\""),
		0xff, 0xff, 0xe2, 0x82)
	contents = append(contents, []byte("\",\"timestamp\":1}}\n")...)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Entry == nil || len(entries[1].Entry.Message) == 0 {
		t.Fatalf("entries = %#v", entries)
	}
	decodedMessage, err := ai.UnmarshalMessage(entries[1].Entry.Message)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := decodedMessage.(*ai.UserMessage)
	if !ok || message.Content.Text == nil || *message.Content.Text != "\ufffd\ufffd\ufffd" {
		t.Fatalf("decoded message = %#v", decodedMessage)
	}
}

func TestMigrateV1ToV3PreservesMemberOrderAndUnknownFields(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session","id":"legacy","timestamp":"2020-01-01T00:00:00.000Z","cwd":"/tmp","legacy":true}`,
		`{"type":"message","timestamp":"2020-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"},"unknown":7}`,
		`{"type":"compaction","timestamp":"2020-01-01T00:00:02.000Z","summary":"s","firstKeptEntryIndex":1,"tokensBefore":12,"unknown":"x"}`,
		`{"type":"message","timestamp":"2020-01-01T00:00:03.000Z","message":{"role":"hookMessage","content":"hook"}}`,
	}, "\n")
	entries := ParseSessionEntries(input)
	migrated, err := MigrateSessionEntries(entries, sequenceIDGenerator("11111111", "22222222", "33333333"))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("v1 session was not reported as migrated")
	}
	got, err := MarshalJSONL(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		`{"type":"session","id":"legacy","timestamp":"2020-01-01T00:00:00.000Z","cwd":"/tmp","legacy":true,"version":3}`,
		`{"type":"message","timestamp":"2020-01-01T00:00:01.000Z","message":{"role":"user","content":"hi"},"unknown":7,"id":"11111111","parentId":null}`,
		`{"type":"compaction","timestamp":"2020-01-01T00:00:02.000Z","summary":"s","tokensBefore":12,"unknown":"x","id":"22222222","parentId":"11111111","firstKeptEntryId":"11111111"}`,
		`{"type":"message","timestamp":"2020-01-01T00:00:03.000Z","message":{"role":"custom","content":"hook"},"id":"33333333","parentId":"22222222"}`,
	}, "\n") + "\n"
	if string(got) != want {
		t.Fatalf("migration mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestMigrateV1CompactionCanKeepItself(t *testing.T) {
	entries := ParseSessionEntries(strings.Join([]string{
		`{"type":"session","id":"legacy","timestamp":"t","cwd":"/tmp"}`,
		`{"type":"compaction","timestamp":"t","summary":"s","firstKeptEntryIndex":1,"tokensBefore":1}`,
	}, "\n"))
	if _, err := MigrateSessionEntries(entries, sequenceIDGenerator("11111111")); err != nil {
		t.Fatal(err)
	}
	if got := entries[1].Entry.FirstKeptEntryID; got != "11111111" {
		t.Fatalf("self firstKeptEntryId = %q, want generated id", got)
	}
}

func TestMigrateV2PreservesIDsAndOnlyRenamesHookRole(t *testing.T) {
	entries := ParseSessionEntries(strings.Join([]string{
		`{"type":"session","version":2,"id":"s","timestamp":"t","cwd":"/tmp"}`,
		`{"type":"message","id":"keep","parentId":null,"timestamp":"t","message":{"content":"x","role":"hookMessage"}}`,
	}, "\n"))
	migrated, err := MigrateSessionEntries(entries, failingIDGenerator("v2 migration generated an id"))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("v2 session was not reported as migrated")
	}
	got, err := MarshalJSONL(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"type\":\"session\",\"version\":3,\"id\":\"s\",\"timestamp\":\"t\",\"cwd\":\"/tmp\"}\n" +
		"{\"type\":\"message\",\"id\":\"keep\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{\"content\":\"x\",\"role\":\"custom\"}}\n"
	if string(got) != want {
		t.Fatalf("migration mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestMigrateNormalizesEveryEntryLikeJSONStringify(t *testing.T) {
	entries := ParseSessionEntries(strings.Join([]string{
		`{ "10" : "ten", "2" : "two", "type" : "session", "version" : 2e0, "id" : "normalize", "timestamp" : "t", "cwd" : "\u002flegacy" }`,
		`{"type":"message","id":"keep","parentId":null,"timestamp":"t","message": { "role" : "user", "content" : "\u0061\/b", "numbers" : [ -0, 1e+2, 1.2300, 9007199254740993 ], "keys" : { "10" : "ten", "2" : "two", "01" : "leading", "4294967294" : "last-index", "4294967295" : "not-index", "escaped\u004bey" : "\u003c" } }, "unknown" : { "3" : "three", "1" : "one", "a" : "\u0026" } }`,
	}, "\n"))
	migrated, err := MigrateSessionEntries(entries, failingIDGenerator("v2 migration generated an id"))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("v2 session was not reported as migrated")
	}
	got, err := MarshalJSONL(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"2\":\"two\",\"10\":\"ten\",\"type\":\"session\",\"version\":3,\"id\":\"normalize\",\"timestamp\":\"t\",\"cwd\":\"/legacy\"}\n" +
		"{\"type\":\"message\",\"id\":\"keep\",\"parentId\":null,\"timestamp\":\"t\",\"message\":{\"role\":\"user\",\"content\":\"a/b\",\"numbers\":[0,100,1.23,9007199254740992],\"keys\":{\"2\":\"two\",\"10\":\"ten\",\"4294967294\":\"last-index\",\"01\":\"leading\",\"4294967295\":\"not-index\",\"escapedKey\":\"<\"}},\"unknown\":{\"1\":\"one\",\"3\":\"three\",\"a\":\"&\"}}\n"
	if string(got) != want {
		t.Fatalf("normalized migration mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func TestMigratePreservesJSONStringifySurrogates(t *testing.T) {
	entries := ParseSessionEntries(strings.Join([]string{
		`{"type":"session","version":2,"id":"surrogates","timestamp":"t","cwd":"/legacy"}`,
		`{"type":"message","id":"keep","parentId":null,"timestamp":"t","\ud800":"high-key","\udc00":"low-key","\ud83d\ude00":"pair-key","message":{"role":"user","content":{"\ud800":"\ud800","\udc00":"\udc00","\ud83d\ude00":"\ud83d\ude00"}}}`,
	}, "\n"))
	migrated, err := MigrateSessionEntries(entries, failingIDGenerator("v2 migration generated an id"))
	if err != nil {
		t.Fatal(err)
	}
	if !migrated {
		t.Fatal("v2 session was not reported as migrated")
	}
	got, err := MarshalJSONL(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"session","version":3,"id":"surrogates","timestamp":"t","cwd":"/legacy"}` + "\n" +
		`{"type":"message","id":"keep","parentId":null,"timestamp":"t","\ud800":"high-key","\udc00":"low-key","😀":"pair-key","message":{"role":"user","content":{"\ud800":"\ud800","\udc00":"\udc00","😀":"😀"}}}` + "\n"
	if string(got) != want {
		t.Fatalf("surrogate migration mismatch\ngot:  %s\nwant: %s", got, want)
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

func TestCurrentVersionMigrationIsNoOp(t *testing.T) {
	entries := ParseSessionEntries("{\"type\":\"session\",\"version\":3,\"id\":\"s\"}\n")
	migrated, err := MigrateSessionEntries(entries, failingIDGenerator("current session generated an id"))
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Fatal("current session reported a migration")
	}
}
