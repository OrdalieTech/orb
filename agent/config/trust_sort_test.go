package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Upstream sorts trust keys with Array.prototype.sort(), i.e. UTF-16 code-unit
// order: an astral character (surrogates 0xD800-0xDFFF) sorts before U+FF01,
// while Go byte order puts U+FF01 (EF BC 81) before the astral (F0 9F 98 80).
func TestWriteTrustFileSortsKeysByUTF16CodeUnits(t *testing.T) {
	if !lessUTF16("/p/\U0001F600", "/p/！") {
		t.Fatal("astral key must sort before U+FF01 in UTF-16 code-unit order")
	}
	if lessUTF16("/p/！", "/p/\U0001F600") {
		t.Fatal("U+FF01 must not sort before an astral key")
	}
	if !lessUTF16("/a", "/a/b") {
		t.Fatal("prefix must sort first")
	}
	path := filepath.Join(t.TempDir(), "trust.json")
	data := trustFile{
		"/p/！":          boolPtr(true),
		"/p/\U0001F600": boolPtr(false),
	}
	if err := writeTrustFile(path, data); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"/p/\U0001F600\": false,\n  \"/p/！\": true\n}\n"
	if string(contents) != want {
		t.Fatalf("trust.json = %q, want %q", contents, want)
	}
}
