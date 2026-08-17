package orbalogo

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFramesFitTheCanvas(t *testing.T) {
	seen := map[[Height]string]int{}
	for index, frame := range frames {
		if first, repeated := seen[frame]; repeated {
			t.Errorf("frame %d repeats frame %d", index+1, first+1)
		}
		seen[frame] = index
		assertCanvas(t, frame[:], index, Width, Height)
	}
}

func TestCompactFramesFitTheCanvas(t *testing.T) {
	seen := map[[CompactHeight]string]int{}
	for index := range frames {
		frame := CompactFrame(index)
		assertCanvas(t, frame[:], index, CompactWidth, CompactHeight)
		seen[frame] = index
	}
	if len(seen) < 8 {
		t.Fatalf("compact unfold collapsed to %d distinct frames", len(seen))
	}
}

func assertCanvas(t *testing.T, frame []string, index, width, height int) {
	t.Helper()
	if len(frame) != height {
		t.Fatalf("frame %d has %d rows, want %d", index+1, len(frame), height)
	}
	for row, line := range frame {
		if got := utf8.RuneCountInString(line); got > width {
			t.Errorf("frame %d row %d is %d columns, want at most %d", index+1, row+1, got, width)
		}
		if line != strings.TrimRight(line, " ") {
			t.Errorf("frame %d row %d has trailing whitespace: %q", index+1, row+1, line)
		}
	}
}
