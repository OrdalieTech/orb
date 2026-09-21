package engine

import (
	"context"
	"testing"
)

func TestAtomicObservationLifecycle(t *testing.T) {
	a := NewAgent(nil)
	initialized := false
	count := 0
	stop := a.Observe(func(s AgentState) { initialized = true }, func(AgentEvent) {
		if !initialized {
			t.Fatal("event before snapshot")
		}
		count++
	})
	if err := a.processEvent(context.Background(), AgentStartEvent{}); err != nil {
		t.Fatal(err)
	}
	stop()
	if err := a.processEvent(context.Background(), AgentEndEvent{}); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal(count)
	}
}

func TestAtomicObservationResetsWithTranscript(t *testing.T) {
	a := NewAgent(nil)
	resets := 0
	stop := a.Observe(func(AgentState) { resets++ }, nil)
	defer stop()
	a.SetMessages(nil)
	a.Reset()
	if resets != 3 {
		t.Fatal(resets)
	}
}
