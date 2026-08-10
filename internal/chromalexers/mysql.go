package chromalexers

import (
	"regexp"

	"github.com/alecthomas/chroma/v2"
)

var (
	mysqlAnalyserNameBetweenBacktickRe = regexp.MustCompile("`[a-zA-Z_]\\w*`")
	mysqlAnalyserNameBetweenBracketRe  = regexp.MustCompile(`\[[a-zA-Z_]\w*\]`)
)

func init() { // nolint: gochecknoinits
	registerSetup(mysqlSetup)
}

// mysqlSetup is upstream's init() body, deferred to the lazy registry build.
func mysqlSetup(reg *chroma.LexerRegistry) {
	reg.Get("mysql").
		SetAnalyser(func(text string) float32 {
			nameBetweenBacktickCount := len(mysqlAnalyserNameBetweenBacktickRe.FindAllString(text, -1))
			nameBetweenBracketCount := len(mysqlAnalyserNameBetweenBracketRe.FindAllString(text, -1))

			var result float32

			// Same logic as above in the TSQL analysis.
			dialectNameCount := nameBetweenBacktickCount + nameBetweenBracketCount
			if dialectNameCount >= 1 && nameBetweenBacktickCount >= (2*nameBetweenBracketCount) {
				// Found at least twice as many `name` as [name].
				result += 0.5
			} else if nameBetweenBacktickCount > nameBetweenBracketCount {
				result += 0.2
			} else if nameBetweenBacktickCount > 0 {
				result += 0.1
			}

			return result
		})
}
