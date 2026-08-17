package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/agent/tools"
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

func TestEditDiffViewUnifiedLayoutAtNarrowWidths(t *testing.T) {
	initTestTheme(t)
	view := NewEditDiffView(editDiffFixture(t), "", "toolPendingBg")
	for _, width := range []int{52, 88, editDiffSplitWidth} {
		lines := view.Render(width)
		plain := make([]string, len(lines))
		for index, line := range lines {
			plain[index] = f12TerminalCSI.ReplaceAllString(line, "")
		}
		if !strings.HasPrefix(plain[0], "+2 -1") {
			t.Fatalf("unified header = %q, want +2 -1 stats", plain[0])
		}
		joined := strings.Join(plain, "\n")
		// Rows re-lay the wire diff as gutter number, sign, then content.
		for _, want := range []string{"2 - beta", "2 + beta prime", "7 + omega", "1   alpha"} {
			if !strings.Contains(joined, want) {
				t.Fatalf("unified layout at width %d missing %q:\n%s", width, want, joined)
			}
		}
		for index, line := range lines[1:] {
			if visible := tui.VisibleWidth(line); visible > width {
				t.Fatalf("unified row %d width = %d, exceeds %d", index, visible, width)
			}
		}
	}
}

// Long content wraps onto continuation lines that keep the gutter band and
// the row tint instead of being truncated.
func TestEditDiffViewUnifiedWrapsLongRows(t *testing.T) {
	initTestTheme(t)
	long := strings.Repeat("wrapped words ", 12)
	view := NewEditDiffView(" 1 alpha\n+2 "+long, "", "toolPendingBg")
	width := 40
	lines := view.Render(width)
	joined := ""
	for _, line := range lines {
		joined += f12TerminalCSI.ReplaceAllString(line, "") + "\n"
	}
	if strings.Contains(joined, "...") {
		t.Fatalf("unified view truncated instead of wrapping:\n%s", joined)
	}
	if got := strings.Count(joined, "wrapped"); got != 12 {
		t.Fatalf("wrapped content lost words: %d of 12 survive:\n%s", got, joined)
	}
	addedBG := theme.BGANSI("diffAddedBg")
	tinted := 0
	for _, line := range lines {
		if strings.Contains(line, addedBG) {
			tinted++
		}
	}
	if tinted < 2 {
		t.Fatalf("continuation lines lost the added tint: %d tinted lines", tinted)
	}
}

func TestEditDiffViewSplitsPastBreakpoint(t *testing.T) {
	initTestTheme(t)
	view := NewEditDiffView(editDiffFixture(t), "", "toolPendingBg")
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
	view := NewEditDiffView(" 1 alpha\n-2 beta\n 3 gamma", "", "toolPendingBg")
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

// Content is syntax highlighted by the file's language, with token foreground
// colors layered over the row tints.
func TestEditDiffViewHighlightsContentByPath(t *testing.T) {
	initTestTheme(t)
	view := NewEditDiffView(" 1 package main\n+2 func main() {}", "main.go", "toolSuccessBg")
	lines := view.Render(80)
	joined := strings.Join(lines, "\n")
	if keyword := theme.FGANSI("syntaxKeyword"); !strings.Contains(joined, keyword) {
		t.Fatalf("no syntax token colors in highlighted diff:\n%q", joined)
	}
	addedBG := theme.BGANSI("diffAddedBg")
	var addedRow string
	for _, line := range lines {
		if strings.Contains(line, addedBG) {
			addedRow = line
		}
	}
	if addedRow == "" {
		t.Fatalf("no added tint row:\n%q", joined)
	}
	if keyword := theme.FGANSI("syntaxKeyword"); strings.Index(addedRow, keyword) < strings.Index(addedRow, addedBG) {
		t.Fatalf("token color not layered over the tint: %q", addedRow)
	}
}

// The focused ANSI layering contract for tinted rows:
//
//	(a) the tint spans the full row width even for short lines,
//	(b) interior SGR full resets (TruncateToWidth brackets its ellipsis with
//	    \x1b[0m) immediately re-open the background,
//	(c) the row never leaves the tint alive at end of line — inside a band it
//	    hands back to the band background and the frame's outer wrap emits
//	    the final \x1b[49m; without a band it closes with \x1b[49m itself.
func TestEditDiffViewTintLayering(t *testing.T) {
	initTestTheme(t)
	addedBG := theme.BGANSI("diffAddedBg")
	gutterBG := theme.BGANSI("diffGutterBg")
	bandBG := theme.BGANSI("toolSuccessBg")
	if addedBG == "" || gutterBG == "" || bandBG == "" {
		t.Fatal("theme did not resolve diff backgrounds")
	}

	// (a) short added line, unified layout: gutter band then tint to full width.
	width := 60
	view := NewEditDiffView(" 1 alpha\n+2 hi", "", "toolSuccessBg")
	var addedRow string
	for _, line := range view.Render(width) {
		if strings.Contains(line, addedBG) {
			addedRow = line
		}
	}
	if addedRow == "" {
		t.Fatal("no tinted row rendered")
	}
	if !strings.HasPrefix(addedRow, gutterBG) {
		t.Fatalf("row does not open with the gutter band: %q", addedRow)
	}
	if got := tui.VisibleWidth(addedRow); got != width {
		t.Fatalf("tinted row visible width = %d, want full width %d", got, width)
	}
	if strings.Contains(addedRow, "\x1b[49m") {
		t.Fatalf("in-band row resets the background mid-line: %q", addedRow)
	}
	if !strings.HasSuffix(addedRow, bandBG) {
		t.Fatalf("row does not hand back to the band background: %q", addedRow)
	}
	if strings.LastIndex(addedRow, addedBG) > strings.LastIndex(addedRow, bandBG) {
		t.Fatalf("tint still active at end of row: %q", addedRow)
	}
	// Composed the way tui.Box wraps band children, the frame line ends with
	// a background reset, so the tint cannot bleed into the next line.
	composed := tui.ApplyBackgroundToLine(addedRow, width+4, func(s string) string { return bandBG + s + "\x1b[49m" })
	if !strings.HasSuffix(composed, "\x1b[49m") {
		t.Fatalf("composed frame line does not end with a background reset: %q", composed)
	}

	// (b) split layout truncation: interior \x1b[0m resets re-open the tint.
	long := strings.Repeat("overflowing content ", 20)
	view = NewEditDiffView(" 1 alpha\n+2 "+long, "", "toolSuccessBg")
	addedRow = ""
	for _, line := range view.Render(editDiffSplitWidth + 20) {
		if strings.Contains(line, addedBG) {
			addedRow = line
		}
	}
	if addedRow == "" {
		t.Fatal("no tinted split row rendered")
	}
	resets := strings.Count(addedRow, "\x1b[0m")
	if resets == 0 {
		t.Fatalf("expected TruncateToWidth to insert full resets: %q", addedRow)
	}
	if reopened := strings.Count(addedRow, "\x1b[0m"+addedBG); reopened != resets {
		t.Fatalf("%d of %d interior full resets re-open the tint: %q", reopened, resets, addedRow)
	}

	// (c) without a surrounding band the row closes the tint itself.
	view = NewEditDiffView(" 1 alpha\n+2 hi", "", "")
	for _, line := range view.Render(width)[1:] {
		if !strings.HasSuffix(line, "\x1b[49m") {
			t.Fatalf("bandless row does not end with a background reset: %q", line)
		}
	}
}
