package tui

import (
	"strings"
	"testing"
)

func gridRows() []GridRow {
	return []GridRow{
		{Header: true, Cells: []string{"Section"}},
		{Cells: []string{"on", "alpha", "first"}, Value: "alpha", Detail: []string{"detail line"}},
		{Cells: []string{"off", "bravo-long", "second"}, Value: "bravo"},
	}
}

func TestGridListAlignsColumnsAndSkipsHeaders(t *testing.T) {
	list := NewGridList(gridRows(), 10, GridListTheme{Cursor: "> "})
	list.DetailHeight = 2
	if list.SelectedValue() != "alpha" {
		t.Fatalf("initial selection = %q, want alpha (headers unselectable)", list.SelectedValue())
	}
	lines := list.Render(60)
	// Fixed geometry: header, alpha, bravo, separator, then the reserved
	// two-line detail area.
	if len(lines) != 6 {
		t.Fatalf("lines = %d: %q", len(lines), lines)
	}
	alpha, bravo := lines[1], lines[2]
	if !strings.HasPrefix(alpha, "> on") || !strings.HasPrefix(bravo, "  off") {
		t.Fatalf("cursor/cells wrong:\n%q\n%q", alpha, bravo)
	}
	// The name column starts at the same screen column in both rows.
	if strings.Index(alpha, "alpha") != strings.Index(bravo, "bravo-long") {
		t.Fatalf("misaligned columns:\n%q\n%q", alpha, bravo)
	}
	if !strings.Contains(lines[3], "─") || !strings.Contains(lines[4], "detail line") {
		t.Fatalf("detail area wrong: %q / %q", lines[3], lines[4])
	}
	// The height must not change with the selection: that is what keeps the
	// overlay window from growing or moving while hovering.
	list.move(1)
	moved := list.Render(60)
	if len(moved) != len(lines) {
		t.Fatalf("height changed with selection: %d != %d", len(moved), len(lines))
	}
	// bravo has no detail: the area stays reserved but blank.
	if strings.Contains(strings.Join(moved, "\n"), "detail line") {
		t.Fatalf("detail leaked across selections: %q", moved)
	}
	list.move(-1)
	list.move(-1)
	if list.SelectedValue() != "alpha" {
		t.Fatalf("moving up from the first item crossed the header")
	}
}

func TestGridListSetRowsKeepsSelectionByValue(t *testing.T) {
	list := NewGridList(gridRows(), 10, GridListTheme{})
	list.move(1)
	if list.SelectedValue() != "bravo" {
		t.Fatalf("selection = %q", list.SelectedValue())
	}
	rows := gridRows()
	rows[2].Cells[0] = "on"
	list.SetRows(rows)
	if list.SelectedValue() != "bravo" {
		t.Fatalf("SetRows lost the selection: %q", list.SelectedValue())
	}
}

func TestFrameChromeAndPadding(t *testing.T) {
	frame := NewFrame("Plugins", "esc close", nil, nil, NewText("body", 0, 0, nil))
	lines := frame.Render(24)
	if len(lines) != 3 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.HasPrefix(lines[0], "╭") || !strings.Contains(lines[0], " Plugins ") {
		t.Fatalf("top edge = %q", lines[0])
	}
	if !strings.Contains(lines[2], " esc close ") {
		t.Fatalf("bottom edge = %q", lines[2])
	}
	for _, line := range lines {
		if VisibleWidth(line) != 24 {
			t.Fatalf("line width %d != 24: %q", VisibleWidth(line), line)
		}
	}
}

func TestStripANSIRemovesStylingAndLinks(t *testing.T) {
	styled := "\x1b[38;2;1;2;3mhi\x1b[0m \x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\"
	if got := StripANSI(styled); got != "hi link" {
		t.Fatalf("StripANSI = %q", got)
	}
	if got := StripANSI("plain"); got != "plain" {
		t.Fatalf("plain text changed: %q", got)
	}
}
