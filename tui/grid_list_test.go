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
	if list.SelectedValue() != "alpha" {
		t.Fatalf("initial selection = %q, want alpha (headers unselectable)", list.SelectedValue())
	}
	lines := list.Render(60)
	// Rows: header, alpha (selected, + detail), bravo.
	if len(lines) != 4 {
		t.Fatalf("lines = %d: %q", len(lines), lines)
	}
	alpha, bravo := lines[1], lines[3]
	if !strings.HasPrefix(alpha, "> on") || !strings.HasPrefix(bravo, "  off") {
		t.Fatalf("cursor/cells wrong:\n%q\n%q", alpha, bravo)
	}
	// The name column starts at the same screen column in both rows.
	if strings.Index(alpha, "alpha") != strings.Index(bravo, "bravo-long") {
		t.Fatalf("misaligned columns:\n%q\n%q", alpha, bravo)
	}
	if !strings.Contains(lines[2], "detail line") {
		t.Fatalf("selected detail missing: %q", lines[2])
	}
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
