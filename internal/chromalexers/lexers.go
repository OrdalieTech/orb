// Package chromalexers is github.com/alecthomas/chroma/v2/lexers at v2.27.0
// with two deliberate changes: the lexer registry is built lazily on first
// use instead of at package init, and the 279 embedded XML lexer configs ship
// as one gzip-compressed tar archive decoded on first use. Upstream's init
// decodes the XML configs (~6ms, ~2.7MB of allocations) on every process
// start, including `orb --version`, and the raw XML adds ~1.9MB to the
// binary; here the decode cost moves to the first highlight and the configs
// ship compressed. Keep every other file byte-identical to upstream apart
// from the package clause, the init() funcs converted to registration hooks,
// and the XML-backed package vars converted to lazy constructors (Register
// vs. registerLazy below).
package chromalexers

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"io/fs"
	"sync"
	"testing/fstest"

	"github.com/alecthomas/chroma/v2"
)

//go:generate go run ./gen

//go:embed embedded.tar.gz
var embeddedArchive []byte

// embeddedLexers decompresses the bundled lexer configs. The XML sources stay
// on disk under embedded/ for provenance; the archive is rebuilt from them by
// `go generate` and TestEmbeddedArchiveMatchesSource fails if the two drift.
var embeddedLexers = sync.OnceValue(func() fs.FS {
	compressed, err := gzip.NewReader(bytes.NewReader(embeddedArchive))
	if err != nil {
		panic(err)
	}
	files := fstest.MapFS{}
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		data := make([]byte, header.Size)
		if _, err := io.ReadFull(archive, data); err != nil {
			panic(err)
		}
		files[header.Name] = &fstest.MapFile{Data: data}
	}
	if err := compressed.Close(); err != nil {
		panic(err)
	}
	return files
})

// pendingLexers collects constructors for the lexers registered by
// package-level Register and registerLazy calls, in upstream's
// var-initialization order. They are added to the registry after the embedded
// XML lexers, matching upstream's sequence (GlobalLexerRegistry XML glob
// first, then per-file Register vars).
var pendingLexers []func() chroma.Lexer

// pendingSetup holds the bodies of upstream's init() funcs (analyser wiring
// on XML-registered lexers). They run inside the lazy build and must use the
// passed registry, never the package-level Get: calling Get from inside the
// build would re-enter the sync.Once and deadlock.
var pendingSetup []func(*chroma.LexerRegistry)

var globalLexerRegistry = sync.OnceValue(func() *chroma.LexerRegistry {
	reg := chroma.NewLexerRegistry()
	embedded := embeddedLexers()
	paths, err := fs.Glob(embedded, "embedded/*.xml")
	if err != nil {
		panic(err)
	}
	for _, path := range paths {
		reg.Register(chroma.MustNewXMLLexer(embedded, path))
	}
	for _, build := range pendingLexers {
		reg.Register(build())
	}
	for _, setup := range pendingSetup {
		setup(reg)
	}
	return reg
})

// Names of all lexers, optionally including aliases.
func Names(withAliases bool) []string {
	return globalLexerRegistry().Names(withAliases)
}

// Aliases of all the lexers, and skip those lexers who do not have any aliases,
// or show their name instead
func Aliases(skipWithoutAliases bool) []string {
	return globalLexerRegistry().Aliases(skipWithoutAliases)
}

// Get a Lexer by name, alias or file extension.
//
// Note that this if there isn't an exact match on name or alias, this will
// call Match(), so it is not efficient.
func Get(name string) chroma.Lexer {
	return globalLexerRegistry().Get(name)
}

// MatchMimeType attempts to find a lexer for the given MIME type.
func MatchMimeType(mimeType string) chroma.Lexer {
	return globalLexerRegistry().MatchMimeType(mimeType)
}

// Match returns the first lexer matching filename.
//
// Note that this iterates over all file patterns in all lexers, so it's not
// particularly efficient.
func Match(filename string) chroma.Lexer {
	return globalLexerRegistry().Match(filename)
}

// Register queues a Lexer for the global registry.
func Register(lexer chroma.Lexer) chroma.Lexer {
	pendingLexers = append(pendingLexers, func() chroma.Lexer { return lexer })
	return lexer
}

// registerLazy queues a lexer constructor whose XML config must not decode at
// package init; the registry build invokes it once. Returning the memoized
// constructor keeps the var-declaration form, so registration order still
// follows upstream's var-initialization order.
func registerLazy(build func() chroma.Lexer) func() chroma.Lexer {
	once := sync.OnceValue(build)
	pendingLexers = append(pendingLexers, once)
	return once
}

// registerSetup queues an upstream init() body to run once the registry is
// built.
func registerSetup(setup func(*chroma.LexerRegistry)) {
	pendingSetup = append(pendingSetup, setup)
}

// Analyse text content and return the "best" lexer..
func Analyse(text string) chroma.Lexer {
	return globalLexerRegistry().Analyse(text)
}

// PlaintextRules is used for the fallback lexer as well as the explicit
// plaintext lexer.
func PlaintextRules() chroma.Rules {
	return chroma.Rules{
		"root": []chroma.Rule{
			{`.+`, chroma.Text, nil},
			{`\n`, chroma.Text, nil},
		},
	}
}

// Fallback lexer if no other is found.
var Fallback chroma.Lexer = chroma.MustNewLexer(&chroma.Config{
	Name:      "fallback",
	Filenames: []string{"*"},
	Priority:  -1,
}, PlaintextRules)
