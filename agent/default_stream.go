package agent

import (
	aiapi "github.com/OrdalieTech/orb/ai/api"
	"github.com/OrdalieTech/orb/engine"
)

func init() {
	engine.SetDefaultStreamFn(aiapi.StreamSimple)
}
