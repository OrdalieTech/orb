package harness

import (
	"reflect"
	"testing"
)

var (
	runStart = HarnessEvent{Type: HarnessEventRunStart, Lane: "main", RunID: "run-1"}
	runEnd   = HarnessEvent{Type: HarnessEventRunEnd, Lane: "main", RunID: "run-1", Outcome: RunCompleted, LeafID: "entry-1"}
)

func collect(events *[]HarnessEvent) HarnessEventListener {
	return func(event HarnessEvent) { *events = append(*events, event) }
}

func TestHarnessEventBusDeliversToListenersAndWatches(t *testing.T) {
	var bus HarnessEventBus
	var direct, watched []HarnessEvent
	off := bus.On(HarnessEventRunStart, collect(&direct))
	_, watch := WatchHarnessEvents(&bus, func() any { return nil })
	watch.Start(collect(&watched))

	bus.Emit(runStart)
	bus.Emit(runEnd)
	off()
	bus.Emit(runStart)

	if want := []HarnessEvent{runStart}; !reflect.DeepEqual(direct, want) {
		t.Fatalf("direct = %v, want %v", direct, want)
	}
	if want := []HarnessEvent{runStart, runEnd, runStart}; !reflect.DeepEqual(watched, want) {
		t.Fatalf("watched = %v, want %v", watched, want)
	}
}

func TestHarnessEventWatchSnapshotHasNoEventGap(t *testing.T) {
	var bus HarnessEventBus
	var received []HarnessEvent
	snapshot, watch := WatchHarnessEvents(&bus, func() string {
		bus.Emit(runStart)
		return "leaf"
	})
	if snapshot != "leaf" || len(received) != 0 {
		t.Fatalf("snapshot = %q, received = %v", snapshot, received)
	}

	watch.Start(collect(&received))
	if want := []HarnessEvent{runStart}; !reflect.DeepEqual(received, want) {
		t.Fatalf("after start = %v, want %v", received, want)
	}
	bus.Emit(runEnd)
	watch.Unsubscribe()
	bus.Emit(runStart)
	if want := []HarnessEvent{runStart, runEnd}; !reflect.DeepEqual(received, want) {
		t.Fatalf("after unsubscribe = %v, want %v", received, want)
	}
}

func TestHarnessEventWatchStartKeepsReentrantOrder(t *testing.T) {
	var bus HarnessEventBus
	var received []HarnessEvent
	_, watch := WatchHarnessEvents(&bus, func() any { return nil })
	bus.Emit(runStart)
	watch.Start(func(event HarnessEvent) {
		received = append(received, event)
		if event.Type == HarnessEventRunStart {
			bus.Emit(runEnd)
		}
	})
	if want := []HarnessEvent{runStart, runEnd}; !reflect.DeepEqual(received, want) {
		t.Fatalf("received = %v, want %v", received, want)
	}
}
