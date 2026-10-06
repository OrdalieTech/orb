package activity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
)

func record(id string, state State, seq uint64) Record {
	return Record{SessionID: "session", ID: id, Source: "Orb", Kind: Agent, Title: "Review the viewer", State: state, Sequence: seq, Updated: time.Unix(int64(seq), 0)}
}

func TestStoreOrdersIsolatesAndDoesNotResurrect(t *testing.T) {
	s := NewStore("session")
	r := record("child", Running, 2)
	if !s.Apply(r) || s.Apply(r) {
		t.Fatal("initial or duplicate event")
	}
	if s.Apply(record("child", Queued, 1)) {
		t.Fatal("late event")
	}
	other := r
	other.SessionID = "another"
	if s.Apply(other) {
		t.Fatal("session leaked")
	}
	other = r
	other.Source = "Claude"
	if !s.Apply(other) || len(s.Snapshot()) != 2 {
		t.Fatal("source IDs collided")
	}
	for _, state := range []State{Unknown, Running, Completed} {
		r.Sequence++
		r.State = state
		if !s.Apply(r) {
			t.Fatalf("transition to %s rejected", state)
		}
	}
	r.Sequence++
	r.State = Running
	if s.Apply(r) {
		t.Fatal("completed child resurrected")
	}
	s.ClearCompleted()
	if s.Apply(r) {
		t.Fatal("dismissed child resurrected")
	}
	snapshot := s.Snapshot()
	if len(snapshot) != 1 || snapshot[0].Source != "Claude" {
		t.Fatal(snapshot)
	}
	snapshot[0].Title = "mutated"
	if s.Snapshot()[0].Title == "mutated" {
		t.Fatal("snapshot aliases store")
	}
}

func TestTerminalAndParentIndependence(t *testing.T) {
	for _, state := range []State{Completed, Failed, Cancelled} {
		s := NewStore("session")
		s.Apply(record("parent", Running, 1))
		s.Apply(record("child", Running, 1))
		s.Apply(record("parent", state, 2))
		if got := s.Snapshot()[0]; got.ID != "child" || got.State != Running {
			t.Fatal(got)
		}
		if s.Apply(record("parent", Unknown, 3)) {
			t.Fatal("terminal outcome changed")
		}
	}
}

func TestStoreBoundsHistoryAndLabels(t *testing.T) {
	s := NewStore("session")
	s.Apply(record("live", Running, 1))
	for i := range 100 {
		s.Apply(record(fmt.Sprint(i), Completed, uint64(i+2)))
	}
	if got := s.Snapshot(); len(got) != 17 || got[0].ID != "live" {
		t.Fatal(got)
	}
	r := record("huge", Running, 1)
	r.Title, r.Detail = strings.Repeat("界", 10000), strings.Repeat("界", 10000)
	s.Apply(r)
	for _, got := range s.Snapshot() {
		if got.ID == "huge" && (len([]rune(got.Title)) != 160 || len([]rune(got.Detail)) != 512) {
			t.Fatal("unbounded text")
		}
	}
	for _, bad := range []Record{{}, record("bad", "invalid", 1), record("bad", Running, 0)} {
		if s.Apply(bad) {
			t.Fatal("accepted malformed record", bad)
		}
	}
}

func TestPublisherConcurrentNamespacesAndReload(t *testing.T) {
	bus := extensions.NewEventBus()
	store := NewStore("session")
	bus.On(Channel, func(_ context.Context, data any) error { store.Apply(data.(Record)); return nil })
	publish := Publisher(func() extensions.EventBus { return bus }, "session", "Orb")
	var group sync.WaitGroup
	for i := range 100 {
		group.Go(func() {
			publish(Record{ID: fmt.Sprint(i), Title: "Child", Kind: Agent, State: Running})
			_ = store.Snapshot()
		})
	}
	group.Wait()
	if len(store.Snapshot()) != 100 {
		t.Fatal("lost concurrent starts")
	}
	for range 2 {
		Publisher(func() extensions.EventBus { return bus }, "session", "Orb")(Record{ID: "same", Title: "Child", Kind: Agent, State: Running})
	}
	if len(store.Snapshot()) != 102 {
		t.Fatal("publisher namespaces collided")
	}
	// The resolver follows the attachment, but never changes the originating session ID.
	bus = extensions.NewEventBus()
	other := NewStore("other")
	bus.On(Channel, func(_ context.Context, data any) error { other.Apply(data.(Record)); return nil })
	publish(Record{ID: "late", Title: "Child", Kind: Agent, State: Running})
	if len(other.Snapshot()) != 0 {
		t.Fatal("late completion leaked into replacement")
	}
	Publisher(func() extensions.EventBus { panic("disposed") }, "session", "Orb")(Record{ID: "late"})
}
