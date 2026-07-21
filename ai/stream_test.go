package ai_test

import (
	"errors"
	"iter"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestCollectDoneAndErrorMessages(t *testing.T) {
	doneMessage := ai.AssistantMessage{Model: "done", Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	errorMessage := ai.AssistantMessage{Model: "error", Content: ai.AssistantContent{}, StopReason: ai.StopReasonError}
	tests := []struct {
		name  string
		event ai.AssistantMessageEvent
		want  string
	}{
		{name: "done", event: ai.DoneEvent{Reason: ai.StopReasonStop, Message: &doneMessage}, want: "done"},
		{name: "error", event: ai.ErrorEvent{Reason: ai.StopReasonError, Error: &errorMessage}, want: "error"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sequence := func(yield func(ai.AssistantMessageEvent, error) bool) {
				yield(test.event, nil)
			}
			message, err := ai.Collect(iter.Seq2[ai.AssistantMessageEvent, error](sequence))
			if err != nil {
				t.Fatal(err)
			}
			if message.Model != test.want {
				t.Fatalf("model = %q, want %q", message.Model, test.want)
			}
		})
	}
}

func TestCollectUsesFirstTerminalEvent(t *testing.T) {
	first := &ai.AssistantMessage{Model: "first", Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	second := &ai.AssistantMessage{Model: "second", Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	sequence := func(yield func(ai.AssistantMessageEvent, error) bool) {
		if !yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: first}, nil) {
			return
		}
		yield(ai.DoneEvent{Reason: ai.StopReasonStop, Message: second}, nil)
	}
	message, err := ai.Collect(iter.Seq2[ai.AssistantMessageEvent, error](sequence))
	if err != nil {
		t.Fatal(err)
	}
	if message != first {
		t.Fatalf("Collect returned %p, want first terminal %p", message, first)
	}
}

func TestEventsRetainMutablePartialReference(t *testing.T) {
	partial := &ai.AssistantMessage{Model: "before", Content: ai.AssistantContent{}, StopReason: ai.StopReasonStop}
	event := ai.StartEvent{Partial: partial}
	partial.Model = "after"
	if event.Partial.Model != "after" {
		t.Fatalf("partial model = %q, want shared mutation", event.Partial.Model)
	}
}

func TestCollectPropagatesIteratorError(t *testing.T) {
	want := errors.New("stream failed")
	sequence := func(yield func(ai.AssistantMessageEvent, error) bool) {
		yield(nil, want)
	}
	_, err := ai.Collect(iter.Seq2[ai.AssistantMessageEvent, error](sequence))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestCollectRejectsIncompleteStream(t *testing.T) {
	sequence := func(yield func(ai.AssistantMessageEvent, error) bool) {}
	_, err := ai.Collect(iter.Seq2[ai.AssistantMessageEvent, error](sequence))
	if !errors.Is(err, ai.ErrStreamIncomplete) {
		t.Fatalf("error = %v, want ErrStreamIncomplete", err)
	}
}
