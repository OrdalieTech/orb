package ai

import (
	"errors"
	"reflect"
	"testing"
)

func TestSessionResourceCleanupsPreserveOrderAndAggregateErrors(t *testing.T) {
	want := errors.New("cleanup failed")
	var calls []string
	unregisterFirst := RegisterSessionResourceCleanup(func(sessionID string) error {
		calls = append(calls, "first:"+sessionID)
		return want
	})
	unregisterSecond := RegisterSessionResourceCleanup(func(sessionID string) error {
		calls = append(calls, "second:"+sessionID)
		return nil
	})
	t.Cleanup(unregisterFirst)
	t.Cleanup(unregisterSecond)

	if err := CleanupSessionResources("session-1"); !errors.Is(err, want) {
		t.Fatalf("cleanup error = %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"first:session-1", "second:session-1"}) {
		t.Fatalf("calls = %v", calls)
	}

	unregisterFirst()
	unregisterFirst()
	calls = nil
	if err := CleanupSessionResources(); err != nil {
		t.Fatalf("cleanup all: %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"second:"}) {
		t.Fatalf("calls after unregister = %v", calls)
	}
}
