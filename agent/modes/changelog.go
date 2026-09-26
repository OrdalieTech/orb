package modes

import (
	"sync"

	"github.com/OrdalieTech/orb"
	"github.com/OrdalieTech/orb/agent"
)

var bundledChangelog = sync.OnceValue(func() string { return agent.FormatChangelog(orb.Changelog) })
