package chromalexers

import (
	"bytes"
	"io/fs"
	"os"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// TestEmbeddedArchiveMatchesSource guards embedded.tar.gz against drift from
// the on-disk XML sources: every file must be present in both places with
// identical bytes. Regenerate with `go generate ./internal/chromalexers`.
func TestEmbeddedArchiveMatchesSource(t *testing.T) {
	archived := map[string][]byte{}
	if err := fs.WalkDir(embeddedLexers(), "embedded", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := fs.ReadFile(embeddedLexers(), path)
		if err != nil {
			return err
		}
		archived[path] = data
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("embedded")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(archived) {
		t.Fatalf("archive has %d files, embedded/ has %d; run go generate ./internal/chromalexers", len(archived), len(entries))
	}
	for _, entry := range entries {
		path := "embedded/" + entry.Name()
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got, ok := archived[path]
		if !ok {
			t.Fatalf("%s missing from embedded.tar.gz; run go generate ./internal/chromalexers", path)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("%s differs between embedded.tar.gz and embedded/; run go generate ./internal/chromalexers", path)
		}
	}
}

// TestRegistryContainsLazyLexers exercises the lazy registrations that
// replaced upstream's init-time package vars: the XML glob, the registerLazy
// constructors (including the delegating lexers composed over the shared HTML
// root), and the deferred analyser setup from dns.go.
func TestRegistryContainsLazyLexers(t *testing.T) {
	for _, name := range []string{
		"HTML", "Common Lisp", "EmacsLisp", "ERB", "YAML+Jinja",
		"Go HTML Template", "Go Text Template", "Svelte", "PHTML",
	} {
		if got := Get(name); got == nil {
			t.Fatalf("Get(%q) = nil", name)
		} else if got.Config().Name != name {
			t.Fatalf("Get(%q).Config().Name = %q", name, got.Config().Name)
		}
	}
	if got := len(Names(false)); got < 290 {
		t.Fatalf("registry has %d lexers, want at least 290", got)
	}
	if lexer := Analyse("@   IN  SOA " + "ns hostmaster 1 2 3 4 5\n"); lexer == nil || lexer.Config().Name != "dns" {
		t.Fatalf("zone file analysis = %v, want dns", lexer)
	}
	if HTML() != HTML() {
		t.Fatal("HTML root lexer is not memoized")
	}
}

// TestLazyLexersTokenise decodes rules through the in-memory FS: chroma
// compiles XML rules on first use, re-reading from the fs passed to
// MustNewXMLLexer, so tokenising proves the decompressed FS stays usable
// after the registry build.
func TestLazyLexersTokenise(t *testing.T) {
	for name, source := range map[string]string{
		"go":     "package main\nfunc main() {}\n",
		"erb":    "<p><%= @title %></p>\n",
		"svelte": "<script>let count = 0;</script>\n",
	} {
		lexer := Get(name)
		if lexer == nil {
			t.Fatalf("Get(%q) = nil", name)
		}
		iterator, err := chroma.Coalesce(lexer).Tokenise(nil, source)
		if err != nil {
			t.Fatalf("tokenise %s: %v", name, err)
		}
		if tokens := iterator.Tokens(); len(tokens) == 0 {
			t.Fatalf("tokenise %s: no tokens", name)
		}
	}
}
