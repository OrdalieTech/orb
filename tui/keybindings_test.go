package tui

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeybindingsManagerDefaultsOverridesAndConflicts(t *testing.T) {
	manager := NewDefaultKeybindings(KeybindingsConfig{"tui.input.submit": {"enter", "ctrl+enter"}, "tui.select.confirm": {"ctrl+x"}, "tui.input.copy": {"ctrl+x"}})
	if got := manager.Keys("tui.input.newLine"); !equalKeyIDs(got, []KeyID{"shift+enter", "ctrl+j"}) {
		t.Fatalf("newline keys = %#v", got)
	}
	if got := manager.Keys("tui.input.submit"); !equalKeyIDs(got, []KeyID{"enter", "ctrl+enter"}) {
		t.Fatalf("submit keys = %#v", got)
	}
	conflicts := manager.Conflicts()
	if len(conflicts) != 1 || conflicts[0].Key != "ctrl+x" {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	if !manager.Matches("\x1b[13;5u", "tui.input.submit") {
		t.Fatal("override did not match")
	}
}

func TestLoadKeybindingsFileMigratesLegacyAndCanonicalWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"cursorUp":"ctrl+p","tui.editor.cursorUp":["up","ctrl+n"],"tui.input.submit":[],"invalid":42}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadKeybindingsFile(path)
	if got := config["tui.editor.cursorUp"]; !equalKeyIDs(got, []KeyID{"up", "ctrl+n"}) {
		t.Fatalf("cursor config = %#v", got)
	}
	if got := config["tui.input.submit"]; len(got) != 0 {
		t.Fatalf("empty override = %#v", got)
	}
	if _, ok := config["invalid"]; ok {
		t.Fatal("invalid binding should be ignored")
	}
}

func equalKeyIDs(left, right []KeyID) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
