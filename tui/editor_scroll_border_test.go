package tui

import "testing"

// Upstream #6962 (37bedb7f): the border indicator is sliced on narrow
// terminals instead of overflowing the width.
func TestCreateScrollBorderNarrowWidths(t *testing.T) {
	if got := createScrollBorder("↑", 3, 20); VisibleWidth(got) != 20 {
		t.Fatalf("wide border width = %d, want 20 (%q)", VisibleWidth(got), got)
	}
	for width := 0; width <= 12; width++ {
		got := createScrollBorder("↓", 1234, width)
		if VisibleWidth(got) > width {
			t.Fatalf("width %d: border %q is %d columns wide", width, got, VisibleWidth(got))
		}
	}
}
