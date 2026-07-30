package modes

import (
	_ "embed"
	"sync"

	"github.com/OrdalieTech/orb/codingagent"
)

//go:embed assets/CHANGELOG.md
var bundledChangelogSource string

var bundledChangelog = sync.OnceValue(func() string {
	return codingagent.FormatChangelog(bundledChangelogSource)
})
