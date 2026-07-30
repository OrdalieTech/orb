package codingagent

import (
	"github.com/OrdalieTech/orb/agent"
	aiapi "github.com/OrdalieTech/orb/ai/api"
)

func init() {
	agent.SetDefaultStreamFn(aiapi.StreamSimple)
}
