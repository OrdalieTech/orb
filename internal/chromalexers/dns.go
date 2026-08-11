package chromalexers

import (
	"regexp"

	"github.com/alecthomas/chroma/v2"
)

func init() { // nolint: gochecknoinits
	registerSetup(dnsSetup)
}

// dnsSetup is upstream's init() body, deferred to the lazy registry build.
// The zone analyser regexp compiles here rather than as a package var so its
// cost also moves out of init.
// TODO(moorereason): can this be factored away?
func dnsSetup(reg *chroma.LexerRegistry) {
	zoneAnalyserRe := regexp.MustCompile(`(?m)^@\s+IN\s+SOA\s+`)
	reg.Get("dns").SetAnalyser(func(text string) float32 {
		if zoneAnalyserRe.FindString(text) != "" {
			return 1.0
		}
		return 0.0
	})
}
