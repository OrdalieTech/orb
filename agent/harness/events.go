package harness

import (
	"slices"
	"sync"
)

// Harness events are the in-process run lifecycle feed. Upstream models them as
// a discriminated union; Type carries the same discriminant here, and the
// run_end-only members are omitted from run_start.

type HarnessEventType string

const (
	HarnessEventRunStart HarnessEventType = "run_start"
	HarnessEventRunEnd   HarnessEventType = "run_end"
)

// RunOutcome is how a run reached its end.
type RunOutcome string

const (
	RunCompleted RunOutcome = "completed"
	RunAborted   RunOutcome = "aborted"
	RunFailed    RunOutcome = "failed"
)

// HarnessEvent is one run lifecycle event.
type HarnessEvent struct {
	Type  HarnessEventType `json:"type"`
	Lane  string           `json:"lane"`
	RunID string           `json:"runId"`

	// Outcome and LeafID are carried by run_end only.
	Outcome RunOutcome `json:"outcome,omitempty"`
	LeafID  string     `json:"leafId,omitempty"`
}

type HarnessEventListener func(HarnessEvent)

// Events is the passive subscription seam: future events only. Earlier events
// are not replayed and no current-state snapshot is provided; a watch gives
// both.
type Events interface {
	On(eventType HarnessEventType, listener HarnessEventListener) (unsubscribe func())
}

// HarnessEventBus fans events out to per-type listeners and to watches.
// Listeners run outside the bus lock, so they may subscribe or emit reentrantly.
type HarnessEventBus struct {
	mu        sync.Mutex
	listeners map[HarnessEventType][]*harnessListener
	watches   []*HarnessEventWatch
}

var _ Events = (*HarnessEventBus)(nil)

type harnessListener struct{ deliver HarnessEventListener }

// On registers a listener for future events of one type and returns its
// unsubscribe.
func (bus *HarnessEventBus) On(eventType HarnessEventType, listener HarnessEventListener) func() {
	entry := &harnessListener{deliver: listener}
	bus.mu.Lock()
	if bus.listeners == nil {
		bus.listeners = map[HarnessEventType][]*harnessListener{}
	}
	bus.listeners[eventType] = append(bus.listeners[eventType], entry)
	bus.mu.Unlock()
	return func() {
		bus.mu.Lock()
		defer bus.mu.Unlock()
		kept := slices.DeleteFunc(bus.listeners[eventType], func(candidate *harnessListener) bool {
			return candidate == entry
		})
		if len(kept) == 0 {
			delete(bus.listeners, eventType)
		} else {
			bus.listeners[eventType] = kept
		}
	}
}

// Emit publishes to this type's listeners and to every watch.
func (bus *HarnessEventBus) Emit(event HarnessEvent) {
	bus.mu.Lock()
	listeners := slices.Clone(bus.listeners[event.Type])
	watches := slices.Clone(bus.watches)
	bus.mu.Unlock()
	for _, listener := range listeners {
		listener.deliver(event)
	}
	for _, watch := range watches {
		watch.receive(event)
	}
}

// WatchHarnessEvents captures a snapshot with no event gap: everything emitted
// from capture onward — including reentrant emissions from capture itself — is
// buffered until Start.
func WatchHarnessEvents[TSnapshot any](bus *HarnessEventBus, capture func() TSnapshot) (TSnapshot, *HarnessEventWatch) {
	watch := &HarnessEventWatch{bus: bus}
	bus.mu.Lock()
	bus.watches = append(bus.watches, watch)
	bus.mu.Unlock()
	return capture(), watch
}

// HarnessEventWatch buffers events until Start hands them to a listener.
type HarnessEventWatch struct {
	bus      *HarnessEventBus
	mu       sync.Mutex
	buffered []HarnessEvent
	listener HarnessEventListener
}

func (watch *HarnessEventWatch) receive(event HarnessEvent) {
	watch.mu.Lock()
	listener := watch.listener
	if listener == nil {
		watch.buffered = append(watch.buffered, event)
	}
	watch.mu.Unlock()
	if listener != nil {
		listener(event)
	}
}

// Start drains the buffer and then delivers live events. Buffering stays on
// while draining so reentrant emissions keep their order.
func (watch *HarnessEventWatch) Start(listener HarnessEventListener) {
	for {
		watch.mu.Lock()
		pending := watch.buffered
		watch.buffered = nil
		if len(pending) == 0 {
			watch.listener = listener
		}
		watch.mu.Unlock()
		if len(pending) == 0 {
			return
		}
		for _, event := range pending {
			listener(event)
		}
	}
}

func (watch *HarnessEventWatch) Unsubscribe() {
	watch.bus.mu.Lock()
	watch.bus.watches = slices.DeleteFunc(watch.bus.watches, func(candidate *HarnessEventWatch) bool {
		return candidate == watch
	})
	watch.bus.mu.Unlock()
	watch.mu.Lock()
	watch.buffered = nil
	watch.mu.Unlock()
}
