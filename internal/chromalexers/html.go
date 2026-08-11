package chromalexers

import (
	"sync"

	"github.com/alecthomas/chroma/v2"
)

// HTML lexer. Shared root for the delegating lexers below; memoized so its
// XML config decodes once, inside the lazy registry build rather than at
// package init.
var HTML = sync.OnceValue(func() chroma.Lexer {
	return chroma.MustNewXMLLexer(embeddedLexers(), "embedded/html.xml")
})
