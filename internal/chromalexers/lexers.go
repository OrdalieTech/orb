// Package chromalexers is github.com/alecthomas/chroma/v2/lexers at v2.27.0
// with one deliberate change: the lexer registry is built lazily on first use
// instead of at package init. Upstream's init decodes 279 embedded XML lexer
// configs (~6ms, ~2.7MB of allocations) on every process start, including
// `orb --version`; here that cost moves to the first highlight. Keep every
// other file byte-identical to upstream apart from the package clause and the
// three init() funcs converted to registration hooks below.
package chromalexers

import (
	"embed"
	"io/fs"
	"sync"

	"github.com/alecthomas/chroma/v2"
)

//go:embed embedded
var embedded embed.FS

// pendingLexers collects the programmatic lexers registered by package-level
// Register calls, in upstream's var-initialization order. They are added to
// the registry after the embedded XML lexers, matching upstream's sequence
// (GlobalLexerRegistry XML glob first, then per-file Register vars).
var pendingLexers []chroma.Lexer

// pendingSetup holds the bodies of upstream's init() funcs (analyser wiring
// on XML-registered lexers). They run inside the lazy build and must use the
// passed registry, never the package-level Get: calling Get from inside the
// build would re-enter the sync.Once and deadlock.
var pendingSetup []func(*chroma.LexerRegistry)

var globalLexerRegistry = sync.OnceValue(func() *chroma.LexerRegistry {
	reg := chroma.NewLexerRegistry()
	paths, err := fs.Glob(embedded, "embedded/*.xml")
	if err != nil {
		panic(err)
	}
	for _, path := range paths {
		reg.Register(chroma.MustNewXMLLexer(embedded, path))
	}
	for _, lexer := range pendingLexers {
		reg.Register(lexer)
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
	pendingLexers = append(pendingLexers, lexer)
	return lexer
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
