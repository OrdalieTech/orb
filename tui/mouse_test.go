package tui

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// Every list-like component drives the one shared pointer semantic.
var (
	_ ListMouseTarget    = (*SelectList)(nil)
	_ MouseMotionHandler = (*SelectList)(nil)
	_ ListMouseTarget    = (*SettingsList)(nil)
	_ MouseMotionHandler = (*Editor)(nil)
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
		// Some terminals report a generic release (button bits 3) rather than
		// repeating the pressed button; it still ends the gesture.
		{"\x1b[<3;12;5m", MouseEvent{Type: MouseRelease, Button: 3, Row: 4, Column: 11}, true},
		{"\x1b[<32;12;5M", MouseEvent{Type: MouseDrag, Row: 4, Column: 11}, true},
		{"\x1b[<2;1;1M", MouseEvent{Type: MousePress, Button: 2}, true},
		{"\x1b[<64;1;1M", MouseEvent{Type: MouseWheelUp}, true},
		{"\x1b[<65;1;1M", MouseEvent{Type: MouseWheelDown, Button: 1}, true},
		{"\x1b[<16;3;2M", MouseEvent{Type: MousePress, Ctrl: true, Row: 1, Column: 2}, true},
		{"\x1b[<12;3;2M", MouseEvent{Type: MousePress, Shift: true, Alt: true, Row: 1, Column: 2}, true},
		// Button bits 3 on a motion report mean no button is held: a hover.
		{"\x1b[<35;12;5M", MouseEvent{Type: MouseMove, Button: 3, Row: 4, Column: 11}, true},
		{"\x1b[<39;3;2M", MouseEvent{Type: MouseMove, Button: 3, Shift: true, Row: 1, Column: 2}, true},
		{"\x1b[<33;12;5M", MouseEvent{Type: MouseDrag, Button: 1, Row: 4, Column: 11}, true},
		// Horizontal wheel, legacy X10, and malformed reports are swallowed.
		{"\x1b[<66;1;1M", MouseEvent{}, false},
		{"\x1b[M !!", MouseEvent{}, false},
		{"\x1b[<0;0;0M", MouseEvent{}, false},
		{"\x1b[<a;1;1M", MouseEvent{}, false},
		{"\x1b[<35;1M", MouseEvent{}, false},
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

	if !ui.handleMouse("\x1b[<0;4;5M") {
		t.Fatal("mouse report was not consumed by the TUI")
	}
	// Motion nothing consumes changes nothing, so it schedules no frame.
	if ui.handleMouse("\x1b[<35;4;1M") {
		t.Fatal("unconsumed motion over the transcript asked for a render")
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

	ui.handleMouse("\x1b[<0;4;5m")
	ui.handleMouse("\x1b[<0;4;5M")
	if got := target.events[len(target.events)-1]; got.Clicks != 2 {
		t.Fatalf("second press = %+v, want Clicks 2", got)
	}
}

func TestTUIMouseFallsThroughWhenComponentDeclines(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}}
	ui, _ := viewportWithTarget(t, target)
	ui.SetSelectionHandler(func(string) {})

	// A declined press still falls through to the viewport, but selection is
	// constrained to the transcript: a chrome press starts nothing.
	ui.handleMouse("\x1b[<0;4;5M")
	if len(target.events) != 1 || ui.selection.active {
		t.Fatalf("declined chrome press = events %d selection %+v, want fall-through without selection", len(target.events), ui.selection)
	}
	// The fall-through path itself stays alive: a declined wheel scrolls the
	// transcript.
	ui.handleMouse("\x1b[<64;4;5M")
	if ui.viewportFollow {
		t.Fatal("declined wheel did not fall through to the transcript")
	}
}

func TestTUIModifiedMouseSkipsComponentDispatch(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)
	ui.SetSelectionHandler(func(string) {})

	ui.handleMouse("\x1b[<4;4;1M") // shift+left press on the transcript
	if len(target.events) != 0 || !ui.selection.active {
		t.Fatalf("shift-modified press was captured: events=%d selection=%+v", len(target.events), ui.selection)
	}
	ui.handleMouse("\x1b[<4;4;1m")

	// Over the chrome the modifier still skips dispatch, and the constrained
	// selection starts nothing either.
	ui.handleMouse("\x1b[<4;4;5M")
	if len(target.events) != 0 || ui.selection.active {
		t.Fatalf("shift-modified chrome press = events %d selection %+v, want neither dispatch nor selection", len(target.events), ui.selection)
	}
}

func TestTUIWheelPrefersComponentThenViewport(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)

	ui.handleMouse("\x1b[<64;4;5M") // wheel up over the component
	if len(target.events) != 1 || target.events[0].Type != MouseWheelUp {
		t.Fatalf("wheel did not reach the component: %+v", target.events)
	}
	if !ui.viewportFollow {
		t.Fatal("component wheel scrolled the transcript")
	}

	ui.handleMouse("\x1b[<64;4;1M") // wheel up over the transcript
	if ui.viewportFollow || len(target.events) != 1 {
		t.Fatalf("transcript wheel = follow %v events %+v", ui.viewportFollow, target.events)
	}
}

func TestTUIDragSelectionKeepsPriorityOverComponents(t *testing.T) {
	target := &clickTarget{lines: []string{"one", "two", "three"}, accept: true}
	ui, _ := viewportWithTarget(t, target)
	copied := make(chan string, 1)
	ui.SetSelectionHandler(func(text string) { copied <- text })

	ui.handleMouse("\x1b[<0;1;1M")  // press on the transcript
	ui.handleMouse("\x1b[<32;4;2M") // drag down into the chrome
	// Generic release: terminals that drop the button number on release must
	// still terminate the drag and copy.
	ui.handleMouse("\x1b[<3;4;2m")
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

	ui.handleMouse("\x1b[<0;3;2M") // second overlay row, third column
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

	ui.handleMouse("\x1b[<0;10;1M")
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

// selectListRows strips the cursor prefix off every rendered item row and
// drops the scroll counter, so window-content comparisons see only which
// items are visible, not which one is highlighted.
func selectListRows(list *SelectList, width int) []string {
	rows := []string{}
	for _, line := range list.Render(width) {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(line, "→ "), "  ")
		if strings.HasPrefix(trimmed, "(") {
			continue
		}
		rows = append(rows, trimmed)
	}
	return rows
}

func TestSelectListHoverMovesHighlightInPlace(t *testing.T) {
	items := []SelectItem{{Value: "one"}, {Value: "two"}, {Value: "three"}}
	confirmed := ""
	list := NewSelectList(items, 4, SelectListTheme{}, SelectListLayoutOptions{})
	list.OnSelect = func(item SelectItem) { confirmed = item.Value }

	if !list.HandleMouse(MouseEvent{Type: MouseMove, Row: 2}) {
		t.Fatal("hover was not consumed")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "three" || confirmed != "" {
		t.Fatalf("hover = %q confirmed %q", item.Value, confirmed)
	}
	if list.HandleMouse(MouseEvent{Type: MouseMove, Row: 3}) {
		t.Fatal("hover below the items was consumed")
	}
}

func TestSelectListHoverOnScrollableListPreservesWindow(t *testing.T) {
	items := []SelectItem{{Value: "one"}, {Value: "two"}, {Value: "three"}, {Value: "four"}, {Value: "five"}, {Value: "six"}}
	confirmed := ""
	list := NewSelectList(items, 3, SelectListTheme{}, SelectListLayoutOptions{})
	list.OnSelect = func(item SelectItem) { confirmed = item.Value }
	list.SetSelectedIndex(4)

	// Window is anchored on "five": rows four, five, six.
	before := selectListRows(list, 40)

	// Hovering the first visible row selects it without scrolling: the
	// visible row set is identical before and after.
	if !list.HandleMouse(MouseEvent{Type: MouseMove, Row: 0}) {
		t.Fatal("hover on a scrollable list was not consumed")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "four" {
		t.Fatalf("hover selected %q, want four", item.Value)
	}
	if after := selectListRows(list, 40); !slices.Equal(before, after) {
		t.Fatalf("hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}

	// The last visible row behaves the same at the other edge.
	if !list.HandleMouse(MouseEvent{Type: MouseMove, Row: 2}) {
		t.Fatal("hover on the last visible row was not consumed")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "six" {
		t.Fatalf("hover selected %q, want six", item.Value)
	}
	if after := selectListRows(list, 40); !slices.Equal(before, after) {
		t.Fatalf("edge hover re-anchored the window:\nbefore %q\nafter  %q", before, after)
	}
	// The scroll-info line below the rows is not hoverable.
	if list.HandleMouse(MouseEvent{Type: MouseMove, Row: 3}) {
		t.Fatal("hover on the scroll-info line was consumed")
	}

	// The wheel keeps its keyboard-like recentring, and hover keeps tracking
	// the rows the recentred window actually shows.
	list.HandleMouse(MouseEvent{Type: MouseWheelUp})
	if item, _ := list.GetSelectedItem(); item.Value != "five" {
		t.Fatalf("wheel selected %q, want five", item.Value)
	}
	list.Render(40)
	if !list.HandleMouse(MouseEvent{Type: MouseMove, Row: 0}) {
		t.Fatal("hover after wheel was not consumed")
	}
	if item, _ := list.GetSelectedItem(); item.Value != "four" {
		t.Fatalf("hover after wheel selected %q, want four", item.Value)
	}

	// Enter confirms the hover selection exactly like a keyboard one.
	press(list, "\r")
	if confirmed != "four" {
		t.Fatalf("enter confirmed %q, want four", confirmed)
	}
}

type motionTarget struct{ clickTarget }

func (target *motionTarget) WantsMouseMotion() bool { return true }

func TestTUIFocusScopesMouseMotionTracking(t *testing.T) {
	terminal := newFakeTerminal(20, 6)
	ui := NewTUI(terminal)
	selector := &motionTarget{clickTarget{lines: []string{"one", "two"}, accept: true}}
	editor := &clickTarget{lines: []string{"editor"}}
	chrome := &Container{}
	chrome.AddChild(editor)
	chrome.AddChild(selector)
	ui.SetViewport(&mutableLines{lines: []string{"body"}}, chrome)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	terminal.resetOutput()

	ui.SetFocus(selector)
	if !strings.Contains(terminal.output(), mouseMotionOn) {
		t.Fatal("focusing a hover-capable component did not enable any-motion tracking")
	}
	terminal.resetOutput()
	ui.SetFocus(selector)
	if terminal.output() != "" {
		t.Fatalf("re-focusing rewrote the tracking state: %q", terminal.output())
	}
	ui.SetFocus(editor)
	if !strings.Contains(terminal.output(), mouseMotionOff) {
		t.Fatal("moving focus away did not disable any-motion tracking")
	}

	// A motion report reaches the component under the cursor as a MouseMove
	// rebased onto its own rows.
	ui.SetFocus(selector)
	ui.RenderNow()
	ui.handleMouse("\x1b[<35;3;6M")
	if len(selector.events) != 1 || selector.events[0].Type != MouseMove || selector.events[0].Row != 1 {
		t.Fatalf("motion events = %+v", selector.events)
	}

	// Stopping always clears motion tracking alongside the alternate screen,
	// and a restart with the selector still focused re-asserts it.
	if err := ui.Stop(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(terminal.output(), alternateScreenOff) {
		t.Fatal("stop did not restore the terminal")
	}
	terminal.resetOutput()
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	if !strings.Contains(terminal.output(), mouseMotionOn) {
		t.Fatal("restart did not re-assert any-motion tracking for the focused selector")
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

func TestSettingsListWheelAndClickShareUnifiedPath(t *testing.T) {
	changed := ""
	list := NewSettingsList([]SettingItem{
		{ID: "a", Label: "A", CurrentValue: "on", Values: []string{"on", "off"}},
		{ID: "b", Label: "B", CurrentValue: "on", Values: []string{"on", "off"}},
		{ID: "c", Label: "C", CurrentValue: "on", Values: []string{"on", "off"}},
	}, 10, SettingsListTheme{Cursor: "> "}, func(id, value string) { changed = id + "=" + value }, nil, SettingsListOptions{})
	list.Render(40)

	// The unified handler drives wheel, click, and double click; hover is
	// deliberately not consumed (the description panel below the rows varies
	// in height inside the bottom-anchored chrome, see HandleMouse).
	if !HandleListMouse(list, MouseEvent{Type: MouseWheelDown}) {
		t.Fatal("wheel was not consumed")
	}
	if list.selectedIndex != 1 {
		t.Fatalf("wheel selected %d, want 1", list.selectedIndex)
	}
	if list.HandleMouse(MouseEvent{Type: MouseMove, Row: 2}) {
		t.Fatal("settings hover was consumed")
	}
	if !list.HandleMouse(MouseEvent{Type: MousePress, Row: 2, Clicks: 1}) {
		t.Fatal("click was not consumed")
	}
	if !list.HandleMouse(MouseEvent{Type: MousePress, Row: 2, Clicks: 2}) {
		t.Fatal("double click was not consumed")
	}
	if changed != "c=off" {
		t.Fatalf("changed = %q", changed)
	}
}

func TestEditorAutocompletePopupScopesMouseMotion(t *testing.T) {
	terminal := newFakeTerminal(40, 10)
	ui := NewTUI(terminal)
	editor := NewEditor(ui, EditorTheme{})
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func(lines []string, _, cursorCol int, _ bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{
			Items:  []AutocompleteItem{{Value: "alpha"}, {Value: "beta"}},
			Prefix: runeSlice(lines[0], 0, cursorCol),
		}
	}})
	chrome := &Container{}
	chrome.AddChild(editor)
	ui.SetViewport(&mutableLines{lines: []string{"body"}}, chrome)
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()

	// A focused editor with no popup must not enable any-motion tracking.
	terminal.resetOutput()
	ui.SetFocus(editor)
	if strings.Contains(terminal.output(), mouseMotionOn) {
		t.Fatal("focusing the bare editor enabled any-motion tracking")
	}
	if editor.WantsMouseMotion() {
		t.Fatal("bare editor advertises mouse motion")
	}

	// Opening the popup resyncs and turns 1003 on. The open path is
	// asynchronous, so poll for the escape bytes.
	press(editor, "#", "x")
	editor.flushAutocomplete()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(terminal.output(), mouseMotionOn) {
		if time.Now().After(deadline) {
			t.Fatal("opening the autocomplete popup did not enable any-motion tracking")
		}
		time.Sleep(time.Millisecond)
	}
	if !editor.WantsMouseMotion() {
		t.Fatal("editor with an open popup does not advertise mouse motion")
	}

	// Hover over a popup row moves the highlight through the shared path.
	editor.Render(40)
	if !editor.HandleMouse(MouseEvent{Type: MouseMove, Row: 4}) {
		t.Fatal("popup hover was not consumed")
	}
	if item, _ := editor.autocompleteList.GetSelectedItem(); item.Value != "beta" {
		t.Fatalf("popup hover selected %q, want beta", item.Value)
	}

	// Closing the popup resyncs synchronously through the input path.
	terminal.resetOutput()
	press(editor, "\x1b")
	if editor.IsShowingAutocomplete() {
		t.Fatal("escape did not close the popup")
	}
	if !strings.Contains(terminal.output(), mouseMotionOff) {
		t.Fatal("closing the autocomplete popup did not disable any-motion tracking")
	}
	if editor.WantsMouseMotion() {
		t.Fatal("editor still advertises mouse motion after the popup closed")
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
