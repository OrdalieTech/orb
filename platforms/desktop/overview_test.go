package main

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
)

func TestPinchToOverviewAndBack(t *testing.T) {
	a := &app{
		o:          &Orb{state: State{Tabs: []Tab{{ID: "t1", Title: "First"}}}},
		pinchScale: 1,
	}
	// Pinching inward (zoom out) across threshold opens Overview.
	for range 5 {
		a.onPinch(ui.InputEvent{Kind: ui.InputZoom, Scale: 0.94})
	}
	if !a.overview {
		t.Fatalf("expected overview after inward pinch, scale=%v", a.pinchScale)
	}
	// Immediate residual momentum during cooldown must not flip Overview back.
	a.onPinch(ui.InputEvent{Kind: ui.InputZoom, Scale: 1.4})
	if !a.overview {
		t.Fatal("cooldown failed to ignore residual momentum")
	}
	// After cooldown, pinching outward (zoom in) closes Overview.
	a.pinchCool = time.Now().Add(-time.Second)
	for range 5 {
		a.onPinch(ui.InputEvent{Kind: ui.InputZoom, Scale: 1.06})
	}
	if a.overview {
		t.Fatalf("expected overview to close after outward pinch, scale=%v", a.pinchScale)
	}
}

func TestHandleInputReceivesRealMyGoPinchOverChildren(t *testing.T) {
	a := &app{
		o:          &Orb{state: State{Tabs: []Tab{{ID: "t1", Title: "First"}}}},
		pinchScale: 1,
	}
	tt := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().HandleInput(func(ev ui.InputEvent) bool {
			if ev.Kind != ui.InputZoom {
				return false
			}
			a.onPinch(ev)
			return true
		}).Children(func() {
			ui.Scroll(c).Fill().Children(func() {
				ui.Text(c, "hello world").Padding(20)
			})
		})
	}, 400, 300)
	tt.Frame()
	for range 5 {
		tt.Pinch(200, 150, 0.94)
		tt.Frame()
	}
	if !a.overview {
		t.Fatalf("expected MyGo HandleInput to open overview on trackpad pinch, scale=%v", a.pinchScale)
	}
}

func TestOverviewTailFormatsRows(t *testing.T) {
	rows := []Row{
		{Kind: "you", Text: "check the build\nsecond line"},
		{Kind: "run", What: "2 reads · 1 command"},
		{Kind: "md", Block: &Block{Type: "p", Spans: []Span{{Text: "All "}, {Text: "green", Bold: true}}}},
	}
	got := overviewTail(rows)
	want := "› check the build\nworked · 2 reads · 1 command\nAll **green**"
	if got != want {
		t.Fatalf("overviewTail = %q, want %q", got, want)
	}
}

func TestReorderTabWithinAndAcrossTiles(t *testing.T) {
	left := &tile{tabs: []string{"a", "b", "c"}, tab: "a"}
	right := &tile{tabs: []string{"d"}, tab: "d"}
	root := &tile{a: left, b: right, share: 50}
	a := &app{root: root, active: left}

	a.reorderTab(left, "a", 2)
	if got := left.tabs; len(got) != 3 || got[0] != "b" || got[1] != "c" || got[2] != "a" || left.tab != "a" {
		t.Fatalf("reorder within tile = %v (active %q)", got, left.tab)
	}
	a.reorderTab(left, "c", 0)
	if got := left.tabs; len(got) != 3 || got[0] != "c" || got[1] != "b" || got[2] != "a" {
		t.Fatalf("reorder to front = %v", got)
	}
	a.reorderTab(left, "d", 1)
	if got := left.tabs; len(got) != 4 || got[0] != "c" || got[1] != "d" || got[2] != "b" || got[3] != "a" {
		t.Fatalf("reorder across tiles = %v", got)
	}
}
