package activity

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
)

func TestActivityStartedByAnEarlierSessionStartHandler(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	if err := registry.Register("eager", func(api extensions.API) error {
		api.On(extensions.EventSessionStart, func(_ context.Context, _ extensions.Event, ctx extensions.Context) (any, error) {
			bus := api.Events()
			Publisher(func() extensions.EventBus { return bus }, ctx.SessionManager().GetSessionID(), "Orb")(Record{ID: "early", Title: "Early work", Kind: Agent, State: Running})
			return nil, nil
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("activity", Extension(func(s *Store, _ extensions.UIHost, _ extensions.Theme) View { return &testView{store: s} })); err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ui := &testUI{}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{SessionManager: manager, UI: ui, Mode: extensions.ModeTUI})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	if ui.view == nil || len(ui.view.store.Snapshot()) != 1 {
		t.Fatal("work launched during session start was lost")
	}
}

func TestSourceAndIDAreSeparateIdentityFields(t *testing.T) {
	store := NewStore("session")
	a := record("B\x00C", Running, 1)
	a.Source = "A"
	b := record("C", Running, 1)
	b.Source = "A\x00B"
	if !store.Apply(a) || !store.Apply(b) || len(store.Snapshot()) != 2 {
		t.Fatal("source and execution ID collided")
	}
}
