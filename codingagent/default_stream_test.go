package codingagent

import (
	"testing"

	"github.com/OrdalieTech/orb/agent"
)

func TestCodingAgentRegistersDefaultAgentStream(t *testing.T) {
	created := agent.NewAgent(nil)
	if created.StreamFn() == nil {
		t.Fatal("legacy nil-stream Agent has no coding-agent default")
	}
}
