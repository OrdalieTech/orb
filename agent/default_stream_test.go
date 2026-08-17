package agent

import (
	"testing"

	"github.com/OrdalieTech/orb/engine"
)

func TestCodingAgentRegistersDefaultAgentStream(t *testing.T) {
	created := engine.NewAgent(nil)
	if created.StreamFn() == nil {
		t.Fatal("legacy nil-stream Agent has no coding-agent default")
	}
}
