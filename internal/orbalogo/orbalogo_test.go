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
		if len(frame) != Height {
			t.Fatalf("frame %d has %d rows, want %d", index+1, len(frame), Height)
		}
		for row, line := range frame {
			if width := utf8.RuneCountInString(line); width > Width {
				t.Errorf("frame %d row %d is %d columns, want at most %d", index+1, row+1, width, Width)
			}
			if line != strings.TrimRight(line, " ") {
				t.Errorf("frame %d row %d has trailing whitespace: %q", index+1, row+1, line)
			}
		}
	}
}
