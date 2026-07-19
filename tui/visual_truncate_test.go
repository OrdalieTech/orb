package tui

import (
	"reflect"
	"testing"
)

func TestTruncateToVisualLinesKeepsWrappedTail(t *testing.T) {
	result := TruncateToVisualLines("abcdefgh\nijklmnop\nqrstuvwx", 3, 6, 1)
	want := []string{" mnop ", " qrst ", " uvwx "}
	if !reflect.DeepEqual(result.VisualLines, want) {
		t.Fatalf("visual lines = %#v, want %#v", result.VisualLines, want)
	}
	if result.SkippedCount != 3 {
		t.Fatalf("skipped count = %d, want 3", result.SkippedCount)
	}
}

func TestTruncateToVisualLinesReturnsShortAndEmptyInput(t *testing.T) {
	short := TruncateToVisualLines("alpha\nbeta", 5, 8, 0)
	if short.SkippedCount != 0 || !reflect.DeepEqual(short.VisualLines, []string{"alpha   ", "beta    "}) {
		t.Fatalf("short result = %#v", short)
	}
	if empty := TruncateToVisualLines("", 5, 8, 0); empty.SkippedCount != 0 || len(empty.VisualLines) != 0 {
		t.Fatalf("empty result = %#v", empty)
	}
}
