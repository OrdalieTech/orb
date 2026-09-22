package engine

import (
	"context"
	"testing"
)

func TestMissingStreamFnReturnsUpstreamError(t *testing.T) {
	want := missingDefaultStreamFnMessage
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("hello")}, AgentContext{}, AgentLoopConfig{Model: loopModel()}, nil, nil)
	if err == nil || err.Error() != want {
		t.Fatalf("missing stream error = %v", err)
	}
	created := NewAgent(nil, WithInitialState(AgentState{Model: loopModel()}))
	if created.StreamFn() != nil {
		t.Fatal("nil-stream Agent acquired a process-wide default")
	}
	if err := created.Prompt(context.Background(), "hello"); err == nil || err.Error() != want {
		t.Fatalf("agent missing stream error = %v", err)
	}
	if messages := created.State().Messages; len(messages) != 0 {
		t.Fatalf("missing stream started a run: %#v", messages)
	}
}
