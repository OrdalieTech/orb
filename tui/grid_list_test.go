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

func TestGridListWrapsDetailIntoTwoReservedRows(t *testing.T) {
	list := NewGridList([]GridRow{{Value: "ssh", Cells: []string{"SSH"}, Detail: []string{"SSH login failed. Check your keys and saved host key."}}}, 1, GridListTheme{})
	list.DetailHeight, list.WrapDetail = 2, true
	lines := list.Render(36)
	if len(lines) != 4 || !strings.Contains(lines[2], "SSH login failed.") || !strings.Contains(lines[3], "saved host key.") {
		t.Fatalf("error was not wrapped into two lines: %q", lines)
	}
	for _, line := range lines {
		if VisibleWidth(line) > 36 || strings.Contains(line, "…") {
			t.Fatalf("wrapped error clipped: %q", line)
		}
	}
}

func TestFrameChromeAndPadding(t *testing.T) {
	frame := NewFrame("Plugins", "esc close", nil, nil, NewText("body", 0, 0, nil))
	lines := frame.Render(24)
	// Contained layout, tight vertical padding: unbroken borders with
	// everything inside — top, title, blank, body, blank, footer, bottom.
	if len(lines) != 7 {
		t.Fatalf("lines = %d: %q", len(lines), lines)
	}
	if strings.Trim(lines[0], "╭─╮") != "" || strings.Trim(lines[6], "╰─╯") != "" {
		t.Fatalf("borders are broken: %q / %q", lines[0], lines[6])
	}
	if !strings.Contains(lines[1], "Plugins") || !strings.Contains(lines[3], "body") || !strings.Contains(lines[5], "esc close") {
		t.Fatalf("interior wrong: %q", lines)
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

func TestGridListSearchFiltersWithStableGeometry(t *testing.T) {
	list := NewGridList(gridRows(), 10, GridListTheme{Cursor: "> "})
	list.Searchable = true
	cancelled := false
	list.OnCancel = func() { cancelled = true }
	base := list.Render(60)
	list.HandleInput(KeyEvent{Raw: "b"})
	list.HandleInput(KeyEvent{Raw: "r"})
	filtered := list.Render(60)
	if len(filtered) != len(base) {
		t.Fatalf("filtering changed the height: %d != %d", len(filtered), len(base))
	}
	if list.SelectedValue() != "bravo" {
		t.Fatalf("selection = %q, want bravo", list.SelectedValue())
	}
	joined := strings.Join(filtered, "\n")
	if strings.Contains(joined, "Section") || strings.Contains(joined, "alpha") {
		t.Fatalf("filtered view leaks non-matches:\n%s", joined)
	}
	// First escape clears the query; only the second closes.
	list.HandleInput(KeyEvent{Raw: "\x1b"})
	if cancelled || list.SelectedValue() == "" {
		t.Fatalf("escape closed instead of clearing the query")
	}
	if !strings.Contains(strings.Join(list.Render(60), "\n"), "alpha") {
		t.Fatal("clearing the query did not restore the rows")
	}
	list.HandleInput(KeyEvent{Raw: "\x1b"})
	if !cancelled {
		t.Fatal("second escape did not close")
	}
}

func TestGridMouseLayoutTracksOnlyVisibleRows(t *testing.T) {
	rows := make([]GridRow, 10000)
	for i := range rows {
		rows[i] = GridRow{Value: "item", Cells: []string{"item"}}
	}
	list := NewGridList(rows, 8, GridListTheme{})
	list.selected = 9000
	list.Render(80)
	if len(list.rowLines) > 8 {
		t.Fatalf("mouse layout retained %d rows", len(list.rowLines))
	}
	index, ok := list.ListRowAt(0)
	if !ok || index < 8993 || index > 9000 {
		t.Fatalf("visible row maps to %d, %v", index, ok)
	}
	list.SetMaxVisible(3)
	list.Render(24)
	if len(list.rowLines) > 3 || list.selected != 9000 {
		t.Fatal("resize lost the selected row or window bound")
	}
}

func TestPlainFrameHasPaddedPanelAndEscapeHint(t *testing.T) {
	frame := NewPanel("Commands", "", nil, nil, func() string { return "\x1b[48;5;236m" }, NewText("body", 0, 0, nil))
	for _, width := range []int{12, 40, 80} {
		lines := frame.Render(width)
		if len(lines) != 5 {
			t.Fatalf("unexpected geometry: %#v", lines)
		}
		for _, line := range lines {
			if VisibleWidth(line) != width || strings.ContainsAny(StripANSI(line), "│─╭╮╰╯") || !strings.HasPrefix(line, "\x1b[48;5;236m") {
				t.Fatalf("invalid panel row: %q", line)
			}
		}
		padding := "  "
		if width < 60 {
			padding = " "
		}
		if !strings.HasSuffix(StripANSI(lines[1]), "esc"+padding) {
			t.Fatalf("missing escape hint: %q", lines[1])
		}
	}
}

func TestFrameActionStaysVisibleAndSupportsKeyboardAndMouse(t *testing.T) {
	list := NewGridList([]GridRow{{Value: "account", Cells: []string{"Personal"}}}, 3, GridListTheme{})
	list.Searchable = true
	frame := NewFrame("Providers", "", nil, nil, list)
	frame.Plain = true
	frame.Action = "+ Connect provider"
	calls := 0
	frame.OnAction = func() { calls++ }
	for _, width := range []int{24, 36, 80} {
		for _, query := range []string{"", "no matching accounts"} {
			list.SetQuery(query)
			lines := frame.Render(width)
			if !strings.Contains(StripANSI(lines[frame.actionRow]), "[+ Connect provider]") {
				t.Fatalf("missing action at width %d: %q", width, lines)
			}
			for _, line := range lines {
				if VisibleWidth(line) != width {
					t.Fatalf("overflow at width %d: %q", width, line)
				}
			}
			before := calls
			frame.HandleMouse(MouseEvent{Type: MousePress, Row: frame.actionRow, Column: frame.actionColumn, Clicks: 1})
			frame.HandleMouse(MouseEvent{Type: MousePress, Row: frame.actionRow, Column: frame.actionColumn, Clicks: 2})
			if calls != before+1 {
				t.Fatalf("click calls = %d", calls-before)
			}
		}
	}
	frame.HandleInput(KeyEvent{Raw: "\t"})
	if !frame.actionFocused {
		t.Fatal("Tab did not focus header action")
	}
	before := calls
	frame.HandleInput(KeyEvent{Raw: "\r"})
	if calls != before+1 {
		t.Fatal("Enter did not activate header action")
	}
	frame.HandleInput(KeyEvent{Raw: "\t"})
	list.SetQuery("")
	frame.HandleInput(KeyEvent{Raw: "p"})
	if list.query != "p" {
		t.Fatal("Tab did not restore search input")
	}
}

func TestFrameActionConcurrentRenderAndInput(t *testing.T) {
	frame := NewFrame("Providers", "", nil, nil, nil)
	frame.Action, frame.OnAction = "+ Connect provider", func() {}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 100 {
			frame.Render(40)
		}
	}()
	for range 100 {
		frame.HandleInput(KeyEvent{Raw: "\t"})
		frame.SetFocused(true)
		frame.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 22})
	}
	<-done
}

func TestNarrowPanelPaddingAndMouseStayAligned(t *testing.T) {
	for _, width := range []int{32, 59, 60, 80} {
		inset := 2
		if width < 60 {
			inset = 1
		}
		clicked := false
		list := NewGridList([]GridRow{{Value: "one", Cells: []string{"Choice"}}}, 3, GridListTheme{})
		list.OnConfirm = func(string) { clicked = true }
		frame := NewPanel("", "", nil, nil, nil, list)
		lines := frame.Render(width)
		if strings.Index(StripANSI(lines[1]), ">") != inset {
			t.Fatalf("width %d: %q", width, lines[1])
		}
		frame.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: inset, Clicks: 1})
		if !clicked {
			t.Fatalf("width %d: mouse missed visible choice", width)
		}
	}
}
