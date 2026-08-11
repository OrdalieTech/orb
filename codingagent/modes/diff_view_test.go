package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/codingagent/modes/theme"
	"github.com/OrdalieTech/orb/codingagent/tools"
	"github.com/OrdalieTech/orb/tui"
)

func editDiffFixture(t *testing.T) string {
	t.Helper()
	oldContent := "alpha\nbeta\ngamma\ndelta\nepsilon\nzeta\n"
	newContent := "alpha\nbeta prime\ngamma\ndelta\nepsilon\nzeta\nomega\n"
	result := tools.GenerateDiffString(oldContent, newContent, 3)
	if result.Diff == "" {
		t.Fatal("empty display diff")
	}
	return result.Diff
}

func TestEditDiffViewKeepsUnifiedRenderingAtNarrowWidths(t *testing.T) {
	initTestTheme(t)
	diff := editDiffFixture(t)
	view := NewEditDiffView(diff)
	for _, width := range []int{52, 88, editDiffSplitWidth} {
		got := view.Render(width)
		want := tui.NewText(strings.Join(theme.Highlight(diff, "diff", theme.Current()), "\n"), 0, 0, nil).Render(width)
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			t.Fatalf("unified rendering drifted at width %d\ngot:  %q\nwant: %q", width, got, want)
		}
	}
}

func TestEditDiffViewSplitsPastBreakpoint(t *testing.T) {
	initTestTheme(t)
	view := NewEditDiffView(editDiffFixture(t))
	width := editDiffSplitWidth + 20
	lines := view.Render(width)
	if len(lines) == 0 {
		t.Fatal("split view rendered nothing")
	}
	plain := make([]string, len(lines))
	for index, line := range lines {
		plain[index] = f12TerminalCSI.ReplaceAllString(line, "")
	}
	if !strings.HasPrefix(plain[0], "+2 -1") {
		t.Fatalf("split header = %q, want +2 -1 stats", plain[0])
	}
	joined := strings.Join(plain, "\n")
	// The change block pairs the removal with the first addition: old line 2
	// on the left, new line 2 on the right of the divider.
	var changeRow, tailRow string
	for _, line := range plain[1:] {
		if strings.Contains(line, "beta") {
			changeRow = line
		}
		if strings.Contains(line, "omega") {
			tailRow = line
		}
	}
	if changeRow == "" || tailRow == "" {
		t.Fatalf("split rows missing change or tail:\n%s", joined)
	}
	left, right, found := strings.Cut(changeRow, "│")
	if !found {
		t.Fatalf("change row has no divider: %q", changeRow)
	}
	if !strings.Contains(left, "2 - beta") || !strings.Contains(right, "2 + beta prime") {
		t.Fatalf("change row panes = %q | %q, want old 2 -beta and new 2 +beta prime", left, right)
	}
	// The trailing pure addition leaves the old pane blank.
	left, right, _ = strings.Cut(tailRow, "│")
	if strings.TrimSpace(left) != "" || !strings.Contains(right, "7 + omega") {
		t.Fatalf("tail row panes = %q | %q, want blank old pane and new line 7", left, right)
	}
	// This change is balanced, so context rows keep matching numbers.
	for _, line := range plain[1:] {
		if strings.Contains(line, "zeta") {
			left, right, _ = strings.Cut(line, "│")
			if !strings.Contains(left, "6") || !strings.Contains(right, "6") {
				t.Fatalf("context row numbers = %q | %q, want 6 on both sides", left, right)
			}
		}
	}
	for index, line := range lines[1:] {
		if visible := tui.VisibleWidth(line); visible > width {
			t.Fatalf("split row %d width = %d, exceeds %d", index, visible, width)
		}
	}
	// Width-keyed cache returns consistent frames.
	if again := strings.Join(view.Render(width), "\n"); again != strings.Join(lines, "\n") {
		t.Fatal("split rendering drifted between frames")
	}
}

// A pure removal offsets the new-side numbering of later context rows.
func TestEditDiffViewSplitRenumbersAfterRemoval(t *testing.T) {
	initTestTheme(t)
	view := NewEditDiffView(" 1 alpha\n-2 beta\n 3 gamma")
	lines := view.Render(editDiffSplitWidth + 20)
	var contextRow string
	for _, line := range lines {
		plain := f12TerminalCSI.ReplaceAllString(line, "")
		if strings.Contains(plain, "gamma") {
			contextRow = plain
		}
	}
	left, right, found := strings.Cut(contextRow, "│")
	if !found || !strings.Contains(left, "3") || !strings.Contains(right, "2") {
		t.Fatalf("context row after removal = %q | %q, want old 3 and new 2", left, right)
	}
}
