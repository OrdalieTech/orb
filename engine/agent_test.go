package engine

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

func TestSessionLoopOwnsExecutionAndSharesCancellation(t *testing.T) {
	entered := make(chan struct{})
	a := NewAgent(nil, WithSessionLoop(func(ctx context.Context, prompts AgentMessages, _ AgentContext, _ AgentLoopConfig, emit EventSink) error {
		if len(prompts) != 1 {
			t.Errorf("prompts = %d", len(prompts))
		}
		if err := emit(ctx, MessageEndEvent{Message: prompts[0]}); err != nil {
			return err
		}
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}))
	done := make(chan error, 1)
	go func() { done <- a.Prompt(context.Background(), "native session") }()
	<-entered
	if a.IsIdle() {
		t.Fatal("session loop was not reserved")
	}
	if err := a.Prompt(context.Background(), "overlap"); err == nil {
		t.Fatal("concurrent prompt accepted")
	}
	a.Abort()
	<-done
	state := a.State()
	if !a.IsIdle() || len(state.Messages) != 2 {
		t.Fatalf("settled state: %#v", state)
	}
	if state.Messages[1].(*ai.AssistantMessage).StopReason != ai.StopReasonAborted {
		t.Fatal("cancellation lost")
	}
}

func TestAgentAwaitsOrderedSubscribersBeforeIdle(t *testing.T) {
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "done"})}}
	agent := NewAgent(
		responses.stream, WithInitialState(AgentState{Model: loopModel()}),
		WithClock(func() int64 { return 77 }),
	)
	entered := make(chan struct{})
	release := make(chan struct{})
	var orderMu sync.Mutex
	var order []int
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		if _, ok := event.(AgentEndEvent); ok {
			orderMu.Lock()
			order = append(order, 1)
			orderMu.Unlock()
			close(entered)
			<-release
		}
		return nil
	})
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		if _, ok := event.(AgentEndEvent); ok {
			orderMu.Lock()
			order = append(order, 2)
			orderMu.Unlock()
		}
		return nil
	})

	promptDone := make(chan error, 1)
	go func() { promptDone <- agent.Prompt(context.Background(), "hello") }()
	<-entered
	if !agent.State().IsStreaming {
		t.Fatal("agent became idle before agent_end subscribers settled")
	}
	waitContext, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := agent.WaitForIdle(waitContext); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitForIdle while listener blocked = %v", err)
	}
	close(release)
	if err := <-promptDone; err != nil {
		t.Fatal(err)
	}
	if err := agent.WaitForIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := joinInts(order); got != "1,2" {
		t.Fatalf("subscriber order = %s", got)
	}
	state := agent.State()
	if state.IsStreaming || len(state.Messages) != 2 {
		t.Fatalf("settled state = %#v", state)
	}
	user := state.Messages[0].(*ai.UserMessage)
	if user.Timestamp != 77 || user.Content.Text != nil || len(user.Content.Blocks) != 1 {
		t.Fatalf("normalized user message = %#v", user)
	}
}

func TestAgentConvertsThrownRunFailureToLifecycle(t *testing.T) {
	agent := NewAgent(
		func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
			return nil, errors.New("provider exploded")
		}, WithInitialState(AgentState{Model: loopModel()}),
		WithClock(func() int64 { return 88 }),
	)
	var eventTypes []AgentEventType
	var ended AgentEndEvent
	agent.Subscribe(func(_ context.Context, event AgentEvent) error {
		eventTypes = append(eventTypes, event.Type())
		if value, ok := event.(AgentEndEvent); ok {
			ended = value
		}
		return nil
	})
	if err := agent.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("failure should be represented as events: %v", err)
	}
	want := "agent_start,turn_start,message_start,message_end,message_start,message_end,turn_end,agent_end"
	if got := joinEventTypes(eventTypes); got != want {
		t.Fatalf("event sequence = %s, want %s", got, want)
	}
	if len(ended.Messages) != 1 {
		t.Fatalf("agent_end messages = %#v", ended.Messages)
	}
	failure := ended.Messages[0].(*ai.AssistantMessage)
	if failure.StopReason != ai.StopReasonError || failure.ErrorMessage == nil || *failure.ErrorMessage != "provider exploded" || failure.Timestamp != 88 {
		t.Fatalf("failure message = %#v", failure)
	}
	state := agent.State()
	if len(state.Messages) != 2 || state.ErrorMessage == nil || *state.ErrorMessage != "provider exploded" {
		t.Fatalf("failure state = %#v", state)
	}
}

func TestAgentContinueDrainsAssistantTailSteeringOneAtATime(t *testing.T) {
	initial := loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "waiting"})
	responses := &loopResponseQueue{messages: []*ai.AssistantMessage{
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "first response"}),
		loopAssistant(ai.StopReasonStop, &ai.TextContent{Text: "second response"}),
	}}
	agent := NewAgent(
		responses.stream, WithInitialState(AgentState{Model: loopModel(), Messages: AgentMessages{initial}}),
	)
	agent.Steer(loopUser("first steering"))
	agent.Steer(loopUser("second steering"))
	if err := agent.Continue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(responses.contexts) != 2 {
		t.Fatalf("provider calls = %d", len(responses.contexts))
	}
	if got := len(responses.contexts[0].Messages); got != 2 {
		t.Fatalf("first request messages = %d, want 2", got)
	}
	if got := len(responses.contexts[1].Messages); got != 4 {
		t.Fatalf("second request messages = %d, want 4", got)
	}
	if agent.HasQueuedMessages() {
		t.Fatal("steering queue was not drained")
	}
}

func joinEventTypes(values []AgentEventType) string {
	strings := make([]string, len(values))
	for index, value := range values {
		strings[index] = string(value)
	}
	return joinStrings(strings)
}

func joinInts(values []int) string {
	strings := make([]string, len(values))
	for index, value := range values {
		if value == 1 {
			strings[index] = "1"
		} else {
			strings[index] = "2"
		}
	}
	return joinStrings(strings)
}
