package activity

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/session"
)

type testHost struct{}

func (testHost) Width() int  { return 80 }
func (testHost) Height() int { return 24 }
func (testHost) Invalidate() {}

type testView struct {
	store    *Store
	toggles  int
	disposed bool
}

func (v *testView) Render(int) []string  { return []string{"activities"} }
func (v *testView) Control(string) error { v.toggles++; return nil }
func (v *testView) Dispose()             { v.disposed = true }

type testUI struct {
	extensions.NoopUI
	view *testView
}

func (ui *testUI) SetWidget(key string, widget *extensions.Widget, opts *extensions.WidgetOptions) {
	if ui.view != nil {
		ui.view.Dispose()
	}
	ui.view = nil
	if widget != nil {
		ui.view = widget.Factory(testHost{}, nil).(*testView)
	}
}

func TestExtensionLifecycleAndSharedView(t *testing.T) {
	registry := extensions.NewRegistry(t.TempDir())
	err := registry.Register("activity", Extension(func(store *Store, _ extensions.UIHost, _ extensions.Theme) View { return &testView{store: store} }))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := session.InMemory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ui := &testUI{}
	runner := extensions.NewRunner(registry, extensions.RunnerOptions{SessionManager: manager, UI: ui, Mode: extensions.ModeTUI})
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	if ui.view != nil {
		t.Fatal("empty activity widget")
	}
	publish := Publisher(func() extensions.EventBus { return registry.Events() }, manager.GetSessionID(), "Orb")
	publish(Record{ID: "child", Title: "Review", Kind: Agent, State: Running})
	view := ui.view
	if view == nil || len(view.store.Snapshot()) != 1 {
		t.Fatal("no activity view")
	}
	if !runner.ExecuteCommand(t.Context(), "activity", "") || view.toggles != 1 {
		t.Fatal("command did not expand")
	}
	// Rebinding the same session neither loses work nor recreates the renderer.
	runner.Emit(t.Context(), extensions.SessionStartEvent{})
	if ui.view != view {
		t.Fatal("rebind replaced live view")
	}
	publish(Record{ID: "child", Title: "Review", Kind: Agent, State: Completed})
	if ui.view != view || view.store.Snapshot()[0].State != Completed {
		t.Fatal("lost completion")
	}
	runner.Emit(t.Context(), extensions.InputEvent{Text: "next"})
	if ui.view != nil || !view.disposed {
		t.Fatal("recent activity did not clear")
	}
	publish(Record{ID: "child", Title: "Review", Kind: Agent, State: Running})
	if ui.view != nil {
		t.Fatal("late progress resurrected completion")
	}
	publish(Record{ID: "other", Title: "Tests", Kind: Process, State: Running})
	view = ui.view
	extensions.EmitSessionShutdown(context.Background(), runner, extensions.SessionShutdownEvent{})
	if ui.view != nil || !view.disposed {
		t.Fatal("shutdown leaked widget")
	}
	publish(Record{ID: "late", Title: "Late", Kind: Agent, State: Running})
	if ui.view != nil {
		t.Fatal("shutdown accepted stale activity")
	}
}
