package chromalexers

import (
	"regexp"

	"github.com/alecthomas/chroma/v2"
)

// TODO(moorereason): can this be factored away?
var zoneAnalyserRe = regexp.MustCompile(`(?m)^@\s+IN\s+SOA\s+`)

func init() { // nolint: gochecknoinits
	registerSetup(dnsSetup)
}

// dnsSetup is upstream's init() body, deferred to the lazy registry build.
func dnsSetup(reg *chroma.LexerRegistry) {
	reg.Get("dns").SetAnalyser(func(text string) float32 {
		if zoneAnalyserRe.FindString(text) != "" {
			return 1.0
		}
		return 0.0
	})
}
