package tui

import "testing"

// Guards the frameScratch double-buffer: rendering frame N+1 must never
// mutate the lines observed for frame N, even once the recycled buffer is
// being reused.
func TestRenderNowRecycledFrameDoesNotMutatePreviousFrame(t *testing.T) {
	body := &mutableLines{lines: []string{"alpha", "beta"}}
	ui := NewTUI(newFakeTerminal(20, 4))
	ui.SetViewport(body, &mutableLines{lines: []string{"input"}})
	ui.setStopped(false)

	ui.RenderNow()
	body.lines = []string{"gamma", "delta"}
	ui.RenderNow()

	observed := ui.previousLines
	snapshot := append([]string(nil), observed...)

	// The third frame renders into the recycled first-frame buffer; if it
	// aliased the second frame's lines this snapshot would change.
	body.lines = []string{"epsilon", "zeta"}
	ui.RenderNow()

	if !equalLines(observed, snapshot) {
		t.Fatalf("previous frame mutated by next render: got %q, want %q", observed, snapshot)
	}
}

// Duplicate components must dirty every occurrence through the lazily built
// window index map, identically to the old eager build.
func TestWindowedContainerDuplicateDirtyThroughLazyIndexes(t *testing.T) {
	container := NewWindowedContainer()
	shared := &mutableLines{lines: []string{"shared"}}
	container.AddChild(shared)
	container.AddChild(&mutableLines{lines: []string{"other"}})
	container.AddChild(shared)

	if got := container.LineCount(10); got != 3 {
		t.Fatalf("initial LineCount = %d, want 3", got)
	}

	shared.lines = []string{"changed", "changed too"}
	container.ChildChanged(shared)
	if got := container.LineCount(10); got != 5 {
		t.Fatalf("LineCount after duplicate dirty = %d, want 5", got)
	}
	want := []string{"changed", "changed too", "other", "changed", "changed too"}
	if got := container.RenderLines(10, 0, 5); !equalLines(got, want) {
		t.Fatalf("RenderLines = %q, want %q", got, want)
	}

	// A child added after the map was built must join it and stay dirtyable.
	container.AddChild(shared)
	shared.lines = []string{"again"}
	container.ChildChanged(shared)
	if got := container.LineCount(10); got != 4 {
		t.Fatalf("LineCount after re-dirty = %d, want 4", got)
	}
	want = []string{"again", "other", "again", "again"}
	if got := container.RenderLines(10, 0, 4); !equalLines(got, want) {
		t.Fatalf("RenderLines after re-dirty = %q, want %q", got, want)
	}
}
