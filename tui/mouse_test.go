package tui

import (
	"strings"
	"testing"
	"time"
)

type clickTarget struct {
	lines  []string
	events []MouseEvent
	accept bool
}

func (target *clickTarget) Render(int) []string { return target.lines }
func (target *clickTarget) HandleMouse(event MouseEvent) bool {
	target.events = append(target.events, event)
	return target.accept
}

func TestParseMouseDecodesSGRReports(t *testing.T) {
	tests := []struct {
		data string
		want MouseEvent
		ok   bool
	}{
		{"\x1b[<0;12;5M", MouseEvent{Type: MousePress, Row: 4, Column: 11}, true},
		{"\x1b[<0;12;5m", MouseEvent{Type: MouseRelease, Row: 4, Column: 11}, true},
		{"\x1b[<32;12;5M", MouseEvent{Type: MouseDrag, Row: 4, Column: 11}, true},
		{"\x1b[<2;1;1M", MouseEvent{Type: MousePress, Button: 2}, true},
		{"\x1b[<64;1;1M", MouseEvent{Type: MouseWheelUp}, true},
		{"\x1b[<65;1;1M", MouseEvent{Type: MouseWheelDown, Button: 1}, true},
		{"\x1b[<16;3;2M", MouseEvent{Type: MousePress, Ctrl: true, Row: 1, Column: 2}, true},
		{"\x1b[<12;3;2M", MouseEvent{Type: MousePress, Shift: true, Alt: true, Row: 1, Column: 2}, true},
		// Horizontal wheel, legacy X10, and malformed reports are swallowed.
		{"\x1b[<66;1;1M", MouseEvent{}, false},
		{"\x1b[M !!", MouseEvent{}, false},
		{"\x1b[<0;0;0M", MouseEvent{}, false},
		{"\x1b[<a;1;1M", MouseEvent{}, false},
	}
	for _, test := range tests {
		got, ok := parseMouse(test.data)
		if ok != test.ok || got != test.want {
			t.Errorf("parseMouse(%q) = %+v %v, want %+v %v", test.data, got, ok, test.want, test.ok)
		}
	}
}

// viewportWithTarget builds a 20x6 viewport whose chrome is one filler line
// followed by target, so target owns screen rows 3..5.
func viewportWithTarget(t *testing.T, target *clickTarget) (*TUI, *mutableLines) {
	t.Helper()
	body := &mutableLines{lines: []string{"body 0", "body 1", "body 2", "body 3", "body 4", "body 5"}}
	chrome := &Container{}
	chrome.AddChild(&mutableLines{lines: []string{"chrome"}})
	chrome.AddChild(target)
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.SetViewport(body, chrome)
	ui.previousLines = ui.renderViewport(20, 6)
	return ui, body
}

func TestTUIDispatchesMouseToComponentUnderCursor(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)
	ui.SetSelectionHandler(func(string) { t.Fatal("consumed click started a text selection") })

	if !ui.handleViewportInput("\x1b[<0;4;5M") {
		t.Fatal("mouse report was not consumed by the TUI")
	}
	if len(target.events) != 1 {
		t.Fatalf("events = %+v", target.events)
	}
	if got := target.events[0]; got.Row != 1 || got.Column != 3 || got.Type != MousePress || got.Clicks != 1 {
		t.Fatalf("local event = %+v, want row 1 column 3 press", got)
	}
	if ui.selection.active {
		t.Fatal("selection started under a component that consumed the press")
	}

	ui.handleViewportInput("\x1b[<0;4;5m")
	ui.handleViewportInput("\x1b[<0;4;5M")
	if got := target.events[len(target.events)-1]; got.Clicks != 2 {
		t.Fatalf("second press = %+v, want Clicks 2", got)
	}
}

func TestTUIMouseFallsThroughWhenComponentDeclines(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}}
	ui, _ := viewportWithTarget(t, target)
	ui.SetSelectionHandler(func(string) {})

	ui.handleViewportInput("\x1b[<0;4;5M")
	if len(target.events) != 1 || !ui.selection.active {
		t.Fatalf("declined press did not reach text selection: events=%d selection=%+v", len(target.events), ui.selection)
	}
}

func TestTUIModifiedMouseSkipsComponentDispatch(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)
	ui.SetSelectionHandler(func(string) {})

	ui.handleViewportInput("\x1b[<4;4;5M") // shift+left press
	if len(target.events) != 0 || !ui.selection.active {
		t.Fatalf("shift-modified press was captured: events=%d selection=%+v", len(target.events), ui.selection)
	}
}

func TestTUIWheelPrefersComponentThenViewport(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)

	ui.handleViewportInput("\x1b[<64;4;5M") // wheel up over the component
	if len(target.events) != 1 || target.events[0].Type != MouseWheelUp {
		t.Fatalf("wheel did not reach the component: %+v", target.events)
	}
	if !ui.viewportFollow {
		t.Fatal("component wheel scrolled the transcript")
	}

	ui.handleViewportInput("\x1b[<64;4;1M") // wheel up over the transcript
	if ui.viewportFollow || len(target.events) != 1 {
		t.Fatalf("transcript wheel = follow %v events %+v", ui.viewportFollow, target.events)
	}
}

func TestTUIDragSelectionKeepsPriorityOverComponents(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })

	ui.handleViewportInput("\x1b[<0;1;1M")  // press on the transcript
	ui.handleViewportInput("\x1b[<32;4;2M") // drag down into the chrome
	ui.handleViewportInput("\x1b[<0;4;2m")  // release over the component
	if len(target.events) != 0 {
		t.Fatalf("an in-flight selection leaked into the component: %+v", target.events)
	}
	select {
	case got := <-copied:
		if !strings.HasPrefix(got, "body 4") {
			t.Fatalf("copied = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("drag across a mouse-aware component no longer copies")
	}
}

func TestTUIDispatchesMouseToOverlay(t *testing.T) {
	target := &clickTarget{lines: []string{"overlay 0", "overlay 1"}, accept: true}
	ui, _ := viewportWithTarget(t, &clickTarget{lines: []string{"one", "two", "three"}})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	ui.ShowOverlay(target, OverlayOptions{Width: AbsoluteSize(10), Anchor: OverlayTopLeft})
	ui.RenderNow()

	ui.handleViewportInput("\x1b[<0;3;2M") // second overlay row, third column
	if len(target.events) != 1 || target.events[0].Row != 1 || target.events[0].Column != 2 {
		t.Fatalf("overlay event = %+v", target.events)
	}
}

func TestTUIScrollbarClickStillBeatsComponents(t *testing.T) {
	body := &mutableLines{}
	for range 100 {
		body.lines = append(body.lines, strings.Repeat("x", 5))
	}
	target := &clickTarget{lines: []string{"one"}, accept: true}
	ui := NewTUI(newFakeTerminal(10, 6))
	ui.SetViewport(body, target)
	ui.previousLines = ui.renderViewport(10, 6)

	ui.handleViewportInput("\x1b[<0;10;1M")
	if !ui.selection.scrollbar || len(target.events) != 0 {
		t.Fatalf("scrollbar click = %+v, component events %+v", ui.selection, target.events)
	}
}

func TestSelectListClickSelectsAndDoubleClickConfirms(t *testing.T) {
	items := []SelectItem{{Value: "one"}, {Value: "two"}, {Value: "three"}, {Value: "four"}}
	list := NewSelectList(items, 4, SelectListTheme{}, SelectListLayoutOptions{})
	confirmed := ""
	list.OnSelect = func(item SelectItem) { confirmed = item.Value }

	if !list.HandleMouse(MouseEvent{Type: MousePress, Row: 2, Clicks: 1}) {
		t.Fatal("click was not consumed")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "three" || confirmed != "" {
		t.Fatalf("single click = %q confirmed %q", item.Value, confirmed)
	}
	if !list.HandleMouse(MouseEvent{Type: MousePress, Row: 2, Clicks: 2}) {
		t.Fatal("double click was not consumed")
	}
	if confirmed != "three" {
		t.Fatalf("double click confirmed %q", confirmed)
	}
	if list.HandleMouse(MouseEvent{Type: MousePress, Row: 9, Clicks: 1}) {
		t.Fatal("click below the visible window was consumed")
	}
	list.HandleMouse(MouseEvent{Type: MouseWheelDown})
	if item, _ := list.GetSelectedItem(); item.Value != "four" {
		t.Fatalf("wheel = %q", item.Value)
	}

	// A scrolled window offsets the row, so hit-testing has to follow it, and
	// the click that recentres it must not move the row out from under the
	// double click that follows.
	scrolled := NewSelectList(items, 3, SelectListTheme{}, SelectListLayoutOptions{})
	scrolled.OnSelect = func(item SelectItem) { confirmed = item.Value }
	scrolled.SetSelectedIndex(3)
	scrolled.HandleMouse(MouseEvent{Type: MousePress, Row: 0, Clicks: 1})
	if item, _ := scrolled.GetSelectedItem(); item.Value != "two" {
		t.Fatalf("scrolled click = %q, want two", item.Value)
	}
	scrolled.HandleMouse(MouseEvent{Type: MousePress, Row: 0, Clicks: 2})
	if confirmed != "two" {
		t.Fatalf("double click on a recentred list confirmed %q, want two", confirmed)
	}
}

func TestSettingsListClickSelectsAndDoubleClickCycles(t *testing.T) {
	changed := ""
	list := NewSettingsList([]SettingItem{
		{ID: "a", Label: "A", CurrentValue: "on", Values: []string{"on", "off"}},
		{ID: "b", Label: "B", CurrentValue: "on", Values: []string{"on", "off"}},
	}, 10, SettingsListTheme{Cursor: "> "}, func(id, value string) { changed = id + "=" + value }, nil, SettingsListOptions{})
	list.Render(40)

	list.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Clicks: 1})
	if !list.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Clicks: 2}) {
		t.Fatal("settings click was not consumed")
	}
	if changed != "b=off" {
		t.Fatalf("changed = %q", changed)
	}
}

func TestEditorClickPositionsCursorAcrossWideCharacters(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("ab界cd")
	editor.Render(20)

	for _, test := range []struct{ column, want int }{{0, 0}, {2, 2}, {3, 2}, {4, 3}, {99, 5}} {
		if !editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: test.column, Clicks: 1}) {
			t.Fatalf("click at column %d was not consumed", test.column)
		}
		if _, col := editor.GetCursor(); col != test.want {
			t.Fatalf("click at column %d = cursor col %d, want %d", test.column, col, test.want)
		}
	}
	if editor.HandleMouse(MouseEvent{Type: MousePress, Row: 0, Clicks: 1}) {
		t.Fatal("click on the editor border was consumed")
	}
}

func TestEditorClickAcceptsAutocompleteSuggestion(t *testing.T) {
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func(lines []string, _, cursorCol int, _ bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{
			Items:  []AutocompleteItem{{Value: "alpha"}, {Value: "beta"}},
			Prefix: runeSlice(lines[0], 0, cursorCol),
		}
	}})
	press(editor, "#", "x")
	editor.flushAutocomplete()
	if !editor.IsShowingAutocomplete() {
		t.Fatal("autocomplete did not open")
	}
	editor.Render(40)

	// Rows: 0 border, 1 text, 2 border, 3 first suggestion, 4 second.
	if !editor.HandleMouse(MouseEvent{Type: MousePress, Row: 4, Clicks: 1}) {
		t.Fatal("suggestion click was not consumed")
	}
	wantText(t, editor, "beta")
	if editor.IsShowingAutocomplete() {
		t.Fatal("autocomplete stayed open after a suggestion was clicked")
	}
}
