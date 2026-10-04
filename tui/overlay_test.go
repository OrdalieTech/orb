package tui

import (
	"slices"
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
	terminal.resetOutput()
	handle.Hide()
	ui.RenderNow()
	if ui.FullRedraws() != redraws {
		t.Fatalf("overlay removal triggered a destructive full redraw: %d -> %d", redraws, ui.FullRedraws())
	}
	if output := terminal.output(); !strings.Contains(output, "\x1b[2K") {
		t.Fatalf("overlay removal did not clear vacated rows: %q", output)
	}
}

// The viewport claims ctrl+PageUp, ctrl+PageDown and ctrl+End ahead of focus
// dispatch, so a focused overlay — extension custom UI, in practice — never
// saw them and the history it covers scrolled invisibly instead.
func TestViewportKeysDeferToFocusedOverlay(t *testing.T) {
	const ctrlPageUp = "\x1b[5;5~"
	body := &mutableLines{lines: make([]string, 12)}
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.SetViewport(body, &mutableLines{lines: []string{"editor"}})
	ui.previousLines = ui.renderViewport(20, 6)

	editor := &overlayFocusRecorder{lines: []string{"EDITOR"}}
	ui.SetFocus(editor)
	ui.handleInput(ctrlPageUp)
	scrolledEnd := ui.viewportEnd
	if ui.viewportFollow || scrolledEnd >= len(body.lines) || len(editor.inputs) != 0 {
		t.Fatalf("plain focus: end=%d follow=%v editor=%q", scrolledEnd, ui.viewportFollow, editor.inputs)
	}

	overlay := &overlayFocusRecorder{lines: []string{"OVERLAY"}}
	handle := ui.ShowOverlay(overlay)
	ui.handleInput(ctrlPageUp)
	if !slices.Equal(overlay.inputs, []string{ctrlPageUp}) || ui.viewportEnd != scrolledEnd {
		t.Fatalf("focused overlay: inputs=%q end=%d, want the overlay to keep the key", overlay.inputs, ui.viewportEnd)
	}

	// A hidden overlay is not covering anything, so the viewport takes the key
	// back even before focus restoration runs.
	handle.SetHidden(true)
	ui.handleInput(ctrlPageUp)
	if ui.viewportEnd >= scrolledEnd || len(overlay.inputs) != 1 {
		t.Fatalf("hidden overlay: end=%d (was %d) inputs=%q", ui.viewportEnd, scrolledEnd, overlay.inputs)
	}
}

func TestDismissedOverlaysReleaseBackingReferences(t *testing.T) {
	for _, hide := range []func(*TUI, OverlayHandle){func(_ *TUI, h OverlayHandle) { h.Hide() }, func(ui *TUI, _ OverlayHandle) { ui.HideOverlay() }} {
		ui := NewTUI(newFakeTerminal(80, 24))
		handle := ui.ShowOverlay(&overlayLines{lines: []string{"dialog"}})
		backing := ui.overlayStack[:cap(ui.overlayStack)]
		hide(ui, handle)
		for _, entry := range backing {
			if entry != nil {
				t.Fatal("dismissed overlay remains retained in stack storage")
			}
		}
	}
}
