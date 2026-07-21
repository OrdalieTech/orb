package jsbridge

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

func TestV081CodingCompactionEventDoesNotExposeHarnessRetainedTail(t *testing.T) {
	_, deferred, err := eventWireValue(extensions.SessionBeforeCompactEvent{
		Preparation: harness.CompactionPreparation{
			FirstKeptEntryID: "kept",
			RetainedTail:     agent.AgentMessages{map[string]any{"role": "user", "content": "tail"}},
		},
		Signal: context.Background(),
	})
	if err != nil {
		t.Fatal(err)
	}
	preparation, ok := deferred["preparation"].(map[string]any)
	if !ok {
		t.Fatalf("deferred preparation = %T, want wire object", deferred["preparation"])
	}
	if _, exists := preparation["retainedTail"]; exists {
		t.Fatalf("coding-agent preparation leaked retainedTail: %#v", preparation)
	}
}
