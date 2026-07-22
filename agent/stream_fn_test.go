package agent

import (
	"context"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestDefaultStreamFnCompatibility(t *testing.T) {
	SetDefaultStreamFn(nil)
	t.Cleanup(func() { SetDefaultStreamFn(nil) })

	calls := 0
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "fallback"}),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "agent fallback"}),
	}}
	SetDefaultStreamFn(func(ctx context.Context, model *ai.Model, request ai.Context, options *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		calls++
		return responses.stream(ctx, model, request, options)
	})

	if _, err := RunLoop(context.Background(), AgentMessages{loopUser("hello")}, AgentContext{}, AgentLoopConfig{Model: loopModel()}, nil, nil); err != nil {
		t.Fatal(err)
	}
	agent := NewAgent(nil, WithInitialState(AgentState{Model: loopModel()}))
	if agent.StreamFn() == nil {
		t.Fatal("agent did not snapshot the configured default stream function")
	}
	if err := agent.Prompt(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("default stream calls = %d, want 2", calls)
	}
}

func TestMissingDefaultStreamFnReturnsUpstreamError(t *testing.T) {
	SetDefaultStreamFn(nil)
	t.Cleanup(func() { SetDefaultStreamFn(nil) })

	want := missingDefaultStreamFnMessage
	_, err := RunLoop(context.Background(), AgentMessages{loopUser("hello")}, AgentContext{}, AgentLoopConfig{Model: loopModel()}, nil, nil)
	if err == nil || err.Error() != want {
		t.Fatalf("missing default error = %v", err)
	}
	created := NewAgent(nil, WithInitialState(AgentState{Model: loopModel()}))
	if err := created.Prompt(context.Background(), "hello"); err == nil || err.Error() != want {
		t.Fatalf("agent missing default error = %v", err)
	}
	if messages := created.State().Messages; len(messages) != 0 {
		t.Fatalf("missing default stream started a run: %#v", messages)
	}
}
