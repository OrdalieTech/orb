package main

import "testing"

func layoutOf(a *app) (tabs []string) {
	for _, t := range a.root.tiles(nil) {
		name := t.tab
		if t.shell {
			name += ">_"
		}
		tabs = append(tabs, name)
	}
	return tabs
}

func TestTiles(t *testing.T) {
	a := &app{root: &tile{tab: "a"}}
	a.active = a.root
	// Splitting right twice makes a line of three, shared evenly.
	a.split(a.active, "right", &tile{tab: "b"})
	a.split(a.active, "right", &tile{tab: "c"})
	if got := layoutOf(a); len(got) != 3 || got[0] != "a" || got[2] != "c" || a.tab != "c" {
		t.Fatalf("layout %v, active %q", got, a.tab)
	}
	if s := a.root.share; s < 33 || s > 34 {
		t.Fatalf("first column has %v%%, want a third", s)
	}
	// A terminal opens under its conversation and closes again.
	a.terminalOf("b")
	if got := layoutOf(a); len(got) != 4 || got[2] != "b>_" || a.active.tab != "b" || !a.active.shell {
		t.Fatalf("with b's terminal: %v", got)
	}
	a.terminalOf("b")
	if got := layoutOf(a); len(got) != 3 {
		t.Fatalf("terminal closed: %v", got)
	}
	// A conversation shows once: placing it again moves it.
	a.place(a.showing("a"), "bottom", "c", false)
	if got := layoutOf(a); len(got) != 3 || got[0] != "a" || got[1] != "c" || got[2] != "b" {
		t.Fatalf("c under a: %v", got)
	}
	// Closing tiles down to the last turns it Home.
	for !a.root.leaf() {
		a.unsplit(a.active)
	}
	a.unsplit(a.root)
	if a.root.tab != "" || a.active != a.root {
		t.Fatalf("last tile %q", a.root.tab)
	}
}
