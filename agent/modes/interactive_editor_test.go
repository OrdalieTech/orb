package modes

import (
	"testing"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

func newFrameEditor(t *testing.T, columns, rows int) *CustomEditor {
	t.Helper()
	initTestTheme(t)
	editor := NewCustomEditor(tui.NewTUI(newFakeTerminal(columns, rows)), theme.EditorTheme(), NewAppKeybindings(nil))
	editor.SetFocused(true)
	return editor
}

func TestComposerFrameMouseLandsOnTheClickedCharacter(t *testing.T) {
	editor := newFrameEditor(t, 24, 24)
	editor.SetText("abcdef")
	editor.Render(24)

	// Row 0 is the top rail; column 0 is the left rail, so column 1+n is the
	// nth character.
	if !editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 1, Column: 4}) {
		t.Fatal("click inside the composer was not consumed")
	}
	if _, column := editor.GetCursor(); column != 3 {
		t.Fatalf("click at column 4 placed the cursor at %d, want 3", column)
	}
	if editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Column: 4}) {
		t.Fatal("click on the top rail should fall through to the transcript")
	}

	editor.Render(2)
	if !editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 1, Column: 1}) {
		t.Fatal("click in an unframed narrow composer was not consumed")
	}
	if _, column := editor.GetCursor(); column != 1 {
		t.Fatalf("narrow click at column 1 placed the cursor at %d", column)
	}
}

func TestDirectShortcutsPreserveEnterAndDraft(t *testing.T) {
	editor := newFrameEditor(t, 80, 24)
	var palette, models, submits, rename, resume int
	editor.OnAction("app.commandPalette", func() { palette++ })
	editor.OnAction("app.model.select", func() { models++ })
	editor.OnAction("app.session.resume", func() { resume++ })
	editor.OnAction("app.session.rename", func() { rename++ })
	editor.OnSubmit = func(string) { submits++ }
	editor.SetText("keep this draft")
	editor.HandleInput(tui.KeyEvent{Raw: "\x10"})
	if palette != 1 || editor.GetText() != "keep this draft" {
		t.Fatalf("palette=%d draft=%q", palette, editor.GetText())
	}
	editor.HandleInput(tui.KeyEvent{Raw: "\x12"})
	if rename != 1 || resume != 0 || editor.GetText() != "keep this draft" {
		t.Fatalf("rename=%d resume=%d draft=%q", rename, resume, editor.GetText())
	}
	for _, key := range []string{"\x1b[109;5u"} {
		editor.HandleInput(tui.KeyEvent{Raw: key})
	}
	if models != 1 || submits != 0 {
		t.Fatalf("model shortcuts: models=%d submits=%d", models, submits)
	}
	editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if submits != 1 || models != 1 {
		t.Fatalf("Enter was intercepted: models=%d submits=%d", models, submits)
	}
}

func TestPaletteShortcutRespectsLiveUserOverrides(t *testing.T) {
	editor := newFrameEditor(t, 80, 24)
	var palette, cycle int
	editor.OnAction("app.commandPalette", func() { palette++ })
	editor.OnAction("app.model.cycleForward", func() { cycle++ })
	editor.keybindings.SetUserBindings(tui.KeybindingsConfig{"app.model.cycleForward": {"Ctrl+P"}})
	for range 10 {
		editor.HandleInput(tui.KeyEvent{Raw: "\x10"})
	}
	if cycle != 10 || palette != 0 {
		t.Fatalf("override: cycle=%d palette=%d", cycle, palette)
	}
	editor.keybindings.SetUserBindings(nil)
	editor.HandleInput(tui.KeyEvent{Raw: "\x10"})
	if palette != 1 {
		t.Fatal("removing override did not restore the palette")
	}
}

func TestComposerShiftEnterNeverQueuesOrSubmits(t *testing.T) {
	previous := tui.GetKeybindings()
	t.Cleanup(func() { tui.SetKeybindings(previous); tui.SetKittyProtocolActive(false) })
	for _, kitty := range []bool{false, true} {
		tui.SetKittyProtocolActive(kitty)
		for _, sequence := range []string{"\x1b\r", "\n", "\x1b[13;2u", "\x1b[13;2~", "\x1b[27;2;13~", "\x1b[13;2:2u"} {
			editor := newFrameEditor(t, 80, 24)
			tui.SetKeybindings(editor.keybindings)
			editor.SetText("draft\\")
			queued, submitted, extension := 0, 0, 0
			editor.OnAction("app.message.followUp", func() { queued++ })
			editor.OnSubmit = func(string) { submitted++ }
			editor.OnExtensionShortcut = func(string) bool { extension++; return true }
			editor.HandleInput(tui.KeyEvent{Raw: sequence})
			if got := editor.GetText(); got != "draft\\\n" || queued != 0 || submitted != 0 || extension != 0 {
				t.Fatalf("kitty=%v sequence=%q draft=%q queued=%d submitted=%d extension=%d", kitty, sequence, got, queued, submitted, extension)
			}
		}
	}
}

func TestComposerExplicitAltEnterStillQueuesAndEnterSubmits(t *testing.T) {
	previous := tui.GetKeybindings()
	t.Cleanup(func() { tui.SetKeybindings(previous); tui.SetKittyProtocolActive(false) })
	editor := newFrameEditor(t, 80, 24)
	tui.SetKeybindings(editor.keybindings)
	queued, submitted := 0, 0
	editor.OnAction("app.message.followUp", func() { queued++ })
	editor.OnSubmit = func(string) { submitted++ }
	editor.SetText("draft")
	editor.HandleInput(tui.KeyEvent{Raw: "\x1b[13;3u"})
	editor.HandleInput(tui.KeyEvent{Raw: "\r"})
	if queued != 1 || submitted != 1 {
		t.Fatalf("queued=%d submitted=%d", queued, submitted)
	}
}
