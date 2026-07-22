package codingagent

import (
	"github.com/OrdalieTech/pigo/agent"
	aiapi "github.com/OrdalieTech/pigo/ai/api"
)

func init() {
	agent.SetDefaultStreamFn(aiapi.StreamSimple)
}
