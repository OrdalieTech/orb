package chromalexers

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// Zed lexer.
func init() { // nolint: gochecknoinits
	registerSetup(zedSetup)
}

// zedSetup is upstream's init() body, deferred to the lazy registry build.
func zedSetup(reg *chroma.LexerRegistry) {
	reg.Get("Zed").SetAnalyser(func(text string) float32 {
		if strings.Contains(text, "definition ") && strings.Contains(text, "relation ") && strings.Contains(text, "permission ") {
			return 0.9
		}
		if strings.Contains(text, "definition ") {
			return 0.5
		}
		if strings.Contains(text, "relation ") {
			return 0.5
		}
		if strings.Contains(text, "permission ") {
			return 0.25
		}
		return 0.0
	})
}
