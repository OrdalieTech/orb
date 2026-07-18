package tui

import (
	"strings"
	"testing"
)

type overlayLines struct {
	lines          []string
	requestedWidth int
}

func (component *overlayLines) Render(width int) []string {
	component.requestedWidth = width
	return component.lines
}

type overlayFocusRecorder struct {
	lines   []string
	focused bool
	inputs  []string
	onInput func(string)
}

func (component *overlayFocusRecorder) Render(int) []string { return component.lines }
func (component *overlayFocusRecorder) HandleInput(event KeyEvent) {
	component.inputs = append(component.inputs, event.Raw)
	if component.onInput != nil {
		component.onInput(event.Raw)
	}
}
func (component *overlayFocusRecorder) SetFocused(focused bool) { component.focused = focused }

type overlayCursorRecorder struct{ overlayFocusRecorder }

func (component *overlayCursorRecorder) Render(int) []string {
	marker := ""
	if component.focused {
		marker = CursorMarker
	}
	return []string{"ab" + marker + "c"}
}

func overlaySliceContains(line string, column, width int, value string) bool {
	return strings.Contains(SliceByColumn(line, column, width, true), value)
}

func TestOverlayLayoutOptionsAndTerminalHeight(t *testing.T) {
	tests := []struct {
		name             string
		options          OverlayOptions
		wantRow, wantCol int
		wantWidth        int
	}{
		{name: "top-left", options: OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(10)}, wantRow: 0, wantCol: 0, wantWidth: 10},
		{name: "top-center", options: OverlayOptions{Anchor: OverlayTopCenter, Width: AbsoluteSize(10)}, wantRow: 0, wantCol: 5, wantWidth: 10},
		{name: "top-right", options: OverlayOptions{Anchor: OverlayTopRight, Width: AbsoluteSize(10)}, wantRow: 0, wantCol: 10, wantWidth: 10},
		{name: "left-center", options: OverlayOptions{Anchor: OverlayLeftCenter, Width: AbsoluteSize(10)}, wantRow: 2, wantCol: 0, wantWidth: 10},
		{name: "center", options: OverlayOptions{Anchor: OverlayCenter, Width: AbsoluteSize(10)}, wantRow: 2, wantCol: 5, wantWidth: 10},
		{name: "right-center", options: OverlayOptions{Anchor: OverlayRightCenter, Width: AbsoluteSize(10)}, wantRow: 2, wantCol: 10, wantWidth: 10},
		{name: "bottom-left", options: OverlayOptions{Anchor: OverlayBottomLeft, Width: AbsoluteSize(10)}, wantRow: 5, wantCol: 0, wantWidth: 10},
		{name: "bottom-center", options: OverlayOptions{Anchor: OverlayBottomCenter, Width: AbsoluteSize(10)}, wantRow: 5, wantCol: 5, wantWidth: 10},
		{name: "bottom-right", options: OverlayOptions{Anchor: OverlayBottomRight, Width: AbsoluteSize(10)}, wantRow: 5, wantCol: 10, wantWidth: 10},
		{name: "margin", options: OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(10), Margin: UniformOverlayMargin(2)}, wantRow: 2, wantCol: 2, wantWidth: 10},
		{name: "offset", options: OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(10), OffsetX: 3, OffsetY: 2}, wantRow: 2, wantCol: 3, wantWidth: 10},
		{name: "percentage-position", options: OverlayOptions{Width: AbsoluteSize(10), Row: PercentSize(50), Col: PercentSize(50)}, wantRow: 2, wantCol: 5, wantWidth: 10},
		{name: "percentage-width-minimum", options: OverlayOptions{Anchor: OverlayTopLeft, Width: PercentSize(10), MinWidth: 6}, wantRow: 0, wantCol: 0, wantWidth: 6},
		{name: "negative-margin-clamped", options: OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(10), Margin: &OverlayMargin{Top: -4, Left: -7}}, wantRow: 0, wantCol: 0, wantWidth: 10},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			terminal := newFakeTerminal(20, 6)
			ui := NewTUI(terminal)
			overlay := &overlayLines{lines: []string{"OVR"}}
			ui.ShowOverlay(overlay, testCase.options)
			got := ui.compositeOverlays(nil, 20, 6)
			if len(got) != 6 {
				t.Fatalf("composite height = %d, want 6", len(got))
			}
			if overlay.requestedWidth != testCase.wantWidth {
				t.Fatalf("render width = %d, want %d", overlay.requestedWidth, testCase.wantWidth)
			}
			if !overlaySliceContains(got[testCase.wantRow], testCase.wantCol, testCase.wantWidth, "OVR") {
				t.Fatalf("overlay missing at row %d col %d: %#v", testCase.wantRow, testCase.wantCol, got)
			}
		})
	}

	t.Run("short-content-is-padded-to-terminal-height", func(t *testing.T) {
		terminal := newFakeTerminal(80, 24)
		ui := NewTUI(terminal)
		ui.ShowOverlay(&overlayLines{lines: []string{"OVERLAY_TOP", "OVERLAY_MID", "OVERLAY_BOT"}})
		got := ui.compositeOverlays([]string{"Line 1", "Line 2", "Line 3"}, 80, 24)
		if len(got) != 24 || !strings.Contains(got[10], "OVERLAY_TOP") || !strings.Contains(got[12], "OVERLAY_BOT") {
			t.Fatalf("short-content overlay = %#v", got)
		}
	})

	t.Run("overlay-is-relative-to-bottom-viewport", func(t *testing.T) {
		terminal := newFakeTerminal(20, 4)
		ui := NewTUI(terminal)
		ui.ShowOverlay(&overlayLines{lines: []string{"TOP"}}, OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(4)})
		base := []string{"0", "1", "2", "3", "4", "5", "6", "7"}
		got := ui.compositeOverlays(base, 20, 4)
		if !strings.Contains(got[4], "TOP") || strings.Contains(got[0], "TOP") {
			t.Fatalf("viewport-relative overlay = %#v", got)
		}
	})

	t.Run("max-height", func(t *testing.T) {
		terminal := newFakeTerminal(20, 10)
		ui := NewTUI(terminal)
		ui.ShowOverlay(&overlayLines{lines: []string{"L1", "L2", "L3", "L4", "L5", "L6"}}, OverlayOptions{Anchor: OverlayTopLeft, MaxHeight: PercentSize(50)})
		got := strings.Join(ui.compositeOverlays(nil, 20, 10), "\n")
		if !strings.Contains(got, "L5") || strings.Contains(got, "L6") {
			t.Fatalf("max-height composite = %q", got)
		}
	})
}

func TestOverlayCompositionCJKTabsStylesAndStackOrder(t *testing.T) {
	segments := ExtractSegments("abcd让EFGH", 5, 9, 11, true)
	if segments.Before != "abcd" || segments.BeforeWidth != 4 || segments.After != "H" || segments.AfterWidth != 1 {
		t.Fatalf("CJK segments = %#v", segments)
	}
	asciiSegments := ExtractSegments("abcdG EFGH", 5, 9, 11, true)
	if asciiSegments.Before != "abcdG" || asciiSegments.BeforeWidth != 5 || VisibleWidth(asciiSegments.Before) != asciiSegments.BeforeWidth {
		t.Fatalf("ASCII segments = %#v", asciiSegments)
	}
	composite := compositeLineAt("abcd让EFGH", "│XX│", 5, 4, 20)
	if strings.Contains(composite, "让") || VisibleWidth(composite) != 20 || !overlaySliceContains(composite, 5, 4, "│XX│") {
		t.Fatalf("CJK composite = %q width %d", composite, VisibleWidth(composite))
	}
	boundaryComposite := compositeLineAt("abcd让EFGH", "│XX│", 4, 4, 20)
	if strings.Contains(boundaryComposite, "让") || VisibleWidth(boundaryComposite) != 20 || !overlaySliceContains(boundaryComposite, 4, 4, "│XX│") {
		t.Fatalf("CJK boundary composite = %q width %d", boundaryComposite, VisibleWidth(boundaryComposite))
	}

	tabText := "out 192M\t.pi/skill-tests/results-ha"
	tabSlice, tabSliceWidth := sliceWithWidth(tabText, 0, 10, true)
	if tabSlice != "out 192M" || tabSliceWidth != 8 || VisibleWidth(tabSlice) != tabSliceWidth {
		t.Fatalf("tab slice = %q width %d", tabSlice, tabSliceWidth)
	}
	tabSegments := ExtractSegments(tabText, 10, 13, 10, true)
	if tabSegments.Before != "out 192M" || tabSegments.BeforeWidth != 8 || VisibleWidth(tabSegments.Before) != tabSegments.BeforeWidth {
		t.Fatalf("tab segments = %#v", tabSegments)
	}
	tabFits := ExtractSegments(tabText, 11, 13, 10, true)
	if tabFits.Before != "out 192M\t" || tabFits.BeforeWidth != 11 || VisibleWidth(tabFits.Before) != tabFits.BeforeWidth {
		t.Fatalf("fitting tab segments = %#v", tabFits)
	}
	for _, controlSequence := range []string{
		"\x1b]8;;https://example.test/a\tb\x07",
		"\x1b]0;window\ttitle\x1b\\",
		"\x1b_payload\tdata\x1b\\",
	} {
		input := controlSequence + "label\ttext"
		if got, want := NormalizeTerminalOutput(input), controlSequence+"label   text"; got != want {
			t.Fatalf("control sequence normalization = %q, want %q", got, want)
		}
	}

	tabbed := compositeLineAt("base 1          ", "\tX", 4, 4, 16)
	normalized := applyLineResets([]string{tabbed})[0]
	if strings.ContainsRune(normalized, '\t') || !overlaySliceContains(normalized, 0, 4, "base") ||
		!overlaySliceContains(normalized, 4, 4, "X") || VisibleWidth(normalized) != 16 {
		t.Fatalf("tab composite = %q width %d", normalized, VisibleWidth(normalized))
	}

	styled := compositeLineAt("\x1b[3m"+strings.Repeat("X", 20)+"\x1b[23m", "OVR", 5, 3, 20)
	styled = applyLineResets([]string{styled})[0]
	if !strings.HasSuffix(styled, segmentReset) || VisibleWidth(styled) != 20 {
		t.Fatalf("styled composite = %q", styled)
	}

	terminal := newFakeTerminal(20, 6)
	ui := NewTUI(terminal)
	lower := ui.ShowOverlay(&overlayLines{lines: []string{"A"}}, OverlayOptions{Row: AbsoluteSize(0), Col: AbsoluteSize(0), Width: AbsoluteSize(1), NonCapturing: true})
	ui.ShowOverlay(&overlayLines{lines: []string{"B"}}, OverlayOptions{Row: AbsoluteSize(0), Col: AbsoluteSize(0), Width: AbsoluteSize(1), NonCapturing: true})
	if got := ui.compositeOverlays(nil, 20, 6); !strings.Contains(got[0], "B") {
		t.Fatalf("creation order = %#v", got)
	}
	lower.Focus()
	if got := ui.compositeOverlays(nil, 20, 6); !strings.Contains(got[0], "A") {
		t.Fatalf("focus order = %#v", got)
	}
}

func TestOverlayHasOverlayTracksActualVisibility(t *testing.T) {
	ui := NewTUI(newFakeTerminal(20, 6))
	visible := false
	handle := ui.ShowOverlay(&overlayLines{lines: []string{"OVERLAY"}}, OverlayOptions{Visible: func(int, int) bool { return visible }})
	if ui.HasOverlay() {
		t.Fatal("responsive-hidden overlay was reported visible")
	}
	visible = true
	if !ui.HasOverlay() {
		t.Fatal("responsive-visible overlay was not reported")
	}
	handle.SetHidden(true)
	if ui.HasOverlay() {
		t.Fatal("explicitly hidden overlay was reported visible")
	}
	handle.SetHidden(false)
	if !ui.HasOverlay() {
		t.Fatal("unhidden overlay was not reported")
	}
	handle.Hide()
	if ui.HasOverlay() {
		t.Fatal("removed overlay was reported visible")
	}
}

func TestOverlayFocusNonCapturingResponsiveAndRestore(t *testing.T) {
	t.Run("non-capturing-focus-cycle", func(t *testing.T) {
		ui := NewTUI(newFakeTerminal(80, 24))
		editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
		overlay := &overlayFocusRecorder{lines: []string{"OVERLAY"}}
		ui.SetFocus(editor)
		handle := ui.ShowOverlay(overlay, OverlayOptions{NonCapturing: true})
		if !editor.focused || overlay.focused {
			t.Fatalf("creation focus editor=%v overlay=%v", editor.focused, overlay.focused)
		}
		handle.Focus()
		if editor.focused || !overlay.focused || !handle.IsFocused() {
			t.Fatalf("explicit focus editor=%v overlay=%v", editor.focused, overlay.focused)
		}
		handle.Unfocus()
		if !editor.focused || overlay.focused || handle.IsFocused() {
			t.Fatalf("unfocus editor=%v overlay=%v", editor.focused, overlay.focused)
		}
	})

	t.Run("responsive-visibility-skips-non-capturing-fallback", func(t *testing.T) {
		ui := NewTUI(newFakeTerminal(80, 24))
		editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
		fallback := &overlayFocusRecorder{lines: []string{"FALLBACK"}}
		passive := &overlayFocusRecorder{lines: []string{"PASSIVE"}}
		primary := &overlayFocusRecorder{lines: []string{"PRIMARY"}}
		visible := true
		ui.SetFocus(editor)
		ui.ShowOverlay(fallback)
		ui.ShowOverlay(passive, OverlayOptions{NonCapturing: true})
		ui.ShowOverlay(primary, OverlayOptions{Visible: func(int, int) bool { return visible }})
		visible = false
		ui.handleInput("x")
		if strings.Join(fallback.inputs, "") != "x" || len(primary.inputs) != 0 || len(passive.inputs) != 0 || !fallback.focused {
			t.Fatalf("fallback inputs=%q primary=%q passive=%q focus=%v", fallback.inputs, primary.inputs, passive.inputs, fallback.focused)
		}
	})

	t.Run("blocked-base-replacement-restores-overlay", func(t *testing.T) {
		ui := NewTUI(newFakeTerminal(80, 24))
		editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
		replacement := &overlayFocusRecorder{lines: []string{"REPLACEMENT"}}
		overlay := &overlayFocusRecorder{lines: []string{"OVERLAY"}}
		ui.SetFocus(editor)
		ui.ShowOverlay(overlay)
		overlay.onInput = func(data string) {
			if data == "b" {
				ui.SetFocus(replacement)
			}
		}
		replacement.onInput = func(data string) {
			if data == "\r" {
				ui.SetFocus(editor)
			}
		}
		ui.handleInput("b")
		ui.handleInput("\r")
		ui.handleInput("x")
		if strings.Join(overlay.inputs, "") != "bx" || strings.Join(replacement.inputs, "") != "\r" || !overlay.focused {
			t.Fatalf("overlay=%q replacement=%q focused=%v", overlay.inputs, replacement.inputs, overlay.focused)
		}
	})

	t.Run("hide-restores-visual-frontmost-capturing-overlay", func(t *testing.T) {
		ui := NewTUI(newFakeTerminal(80, 24))
		editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
		first := &overlayFocusRecorder{lines: []string{"FIRST"}}
		second := &overlayFocusRecorder{lines: []string{"SECOND"}}
		third := &overlayFocusRecorder{lines: []string{"THIRD"}}
		ui.SetFocus(editor)
		firstHandle := ui.ShowOverlay(first)
		secondHandle := ui.ShowOverlay(second)
		ui.ShowOverlay(third)
		firstHandle.Focus()
		secondHandle.Focus()
		secondHandle.SetHidden(true)
		ui.handleInput("x")
		if strings.Join(first.inputs, "") != "x" || !first.focused || len(third.inputs) != 0 {
			t.Fatalf("first=%q third=%q focus=%v", first.inputs, third.inputs, first.focused)
		}
	})
}

func TestOverlaySuppressesClearOnShrinkUntilRemoved(t *testing.T) {
	terminal := newFakeTerminal(20, 6)
	ui := NewTUI(terminal)
	ui.SetClearOnShrink(true)
	content := &mutableLines{lines: []string{"0", "1", "2", "3", "4"}}
	ui.AddChild(content)
	handle := ui.ShowOverlay(&overlayLines{lines: []string{"OVERLAY"}}, OverlayOptions{Anchor: OverlayTopLeft, Width: AbsoluteSize(8)})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	redraws := ui.FullRedraws()
	content.lines = []string{"0"}
	ui.RenderNow()
	if ui.FullRedraws() != redraws {
		t.Fatalf("active overlay triggered clear-on-shrink: %d -> %d", redraws, ui.FullRedraws())
	}
	handle.Hide()
	ui.RenderNow()
	if ui.FullRedraws() <= redraws {
		t.Fatalf("overlay removal did not restore clear-on-shrink: %d -> %d", redraws, ui.FullRedraws())
	}
}

func TestFocusedOverlayPositionsHardwareCursorAndRestoresBaseFocus(t *testing.T) {
	terminal := newFakeTerminal(20, 6)
	ui := NewTUI(terminal)
	editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
	overlay := &overlayCursorRecorder{}
	ui.AddChild(&mutableLines{lines: []string{"base"}})
	ui.SetFocus(editor)
	handle := ui.ShowOverlay(overlay, OverlayOptions{Row: AbsoluteSize(2), Col: AbsoluteSize(5), Width: AbsoluteSize(10)})
	if err := ui.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ui.Stop() }()
	output := terminal.output()
	if strings.Contains(output, CursorMarker) || !strings.Contains(output, "\x1b[8G") || !overlay.focused || !handle.IsFocused() {
		t.Fatalf("focused overlay cursor output=%q focused=%v", output, overlay.focused)
	}
	handle.Hide()
	if !editor.focused || overlay.focused {
		t.Fatalf("restored focus editor=%v overlay=%v", editor.focused, overlay.focused)
	}
}
