package claudesessions

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	work "github.com/OrdalieTech/orb/plugins/activity"
)

func TestNativeActivityOutlivesToolAndParentTurn(t *testing.T) {
	_, driver := fixture(t)
	bus := extensions.NewEventBus()
	store := work.NewStore(driver.options.Manager.GetSessionID())
	bus.On(work.Channel, func(_ context.Context, data any) error { store.Apply(data.(work.Record)); return nil })
	driver.options.Activity = work.Publisher(func() extensions.EventBus { return bus }, driver.options.Manager.GetSessionID(), "Claude")
	tr := translation{driver: driver, ctx: t.Context(), tools: map[string]string{"tool": "Agent"}, emit: func(context.Context, engine.AgentEvent) error { return nil }}
	send := func(raw string) {
		t.Helper()
		if err := tr.event([]byte(raw)); err != nil {
			t.Fatal(err)
		}
	}
	start := `{"type":"system","subtype":"task_started","task_id":"child","task_type":"local_agent","description":"Trace viewer"}`
	send(start)
	send(start)
	if err := tr.toolResult("tool", nil, false, nil); err != nil {
		t.Fatal(err)
	}
	tr.last = &ai.AssistantMessage{}
	if err := tr.endTurn(); err != nil {
		t.Fatal(err)
	}
	if got := store.Snapshot(); len(got) != 1 || got[0].State != work.Running || got[0].Kind != work.Agent {
		t.Fatal(got)
	}
	tr.taskActivity(tr.tasks["child"], work.Unknown, "connection lost")
	send(`{"type":"system","subtype":"task_progress","task_id":"child","summary":"Reading the viewer"}`)
	if got := store.Snapshot()[0]; got.State != work.Running || got.Detail != "Reading the viewer" {
		t.Fatal(got)
	}
	send(`{"type":"system","subtype":"task_notification","task_id":"child","status":"completed","summary":"Found the issue"}`)
	if got := store.Snapshot()[0]; got.State != work.Completed {
		t.Fatal(got)
	}
	send(`{"type":"system","subtype":"task_progress","task_id":"child","summary":"late"}`)
	if got := store.Snapshot()[0]; got.State != work.Completed {
		t.Fatal(got)
	}
	send(`{"type":"system","subtype":"task_started","task_id":"fg","task_type":"local_bash","description":"Foreground"}`)
	if len(store.Snapshot()) != 1 {
		t.Fatal("foreground tool duplicated")
	}
	send(`{"type":"system","subtype":"task_updated","task_id":"fg","patch":{"is_backgrounded":true}}`)
	if got := store.Snapshot()[0]; got.Kind != work.Process || got.State != work.Running {
		t.Fatal(got)
	}
	send(`{"type":"system","subtype":"task_notification","task_id":"fg","status":"failed"}`)
	// Both tasks are finished now, ordered by when they were last updated,
	// which a coarse clock (Windows) can stamp alike: find the process.
	got := store.Snapshot()
	at := slices.IndexFunc(got, func(r work.Record) bool { return r.Kind == work.Process })
	if at < 0 || got[at].State != work.Failed {
		t.Fatal(got)
	}
}

func TestNativeRuntimePublishesActivitiesOnItsCurrentBus(t *testing.T) {
	host, _ := fixture(t)
	s := host.Session()
	events := make(chan work.Record, 8)
	unsubscribe := s.ExtensionRunner().Events().On(work.Channel, func(_ context.Context, data any) error { events <- data.(work.Record); return nil })
	defer unsubscribe()
	done := make(chan error, 1)
	go func() { done <- s.Prompt(t.Context(), "task-fixture") }()
	select {
	case r := <-events:
		if r.Source != "Claude" || r.SessionID != s.Manager().GetSessionID() || r.State != work.Running {
			t.Fatal(r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native start missing")
	}
	if err := s.Steer("meanwhile"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("native task did not settle")
	}
	select {
	case r := <-events:
		// This SDK fixture omits the final status; absence must not mean success.
		if r.State != work.Unknown {
			t.Fatal(r)
		}
	default:
		t.Fatal("native terminal observation missing")
	}
}
