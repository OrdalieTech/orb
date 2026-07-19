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

// The three cases below port upstream keybindings-migration.test.ts. Go has
// no on-disk rewrite step; the same migrations apply when the file loads.

func TestLoadKeybindingsFileRewritesLegacyNamesToNamespacedIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"cursorUp":["up","ctrl+p"],"expandTools":"ctrl+x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadKeybindingsFile(path)
	if len(config) != 2 {
		t.Fatalf("migrated config = %#v, want exactly the two namespaced ids", config)
	}
	if got := config["tui.editor.cursorUp"]; !equalKeyIDs(got, []KeyID{"up", "ctrl+p"}) {
		t.Fatalf("tui.editor.cursorUp = %#v", got)
	}
	if got := config["app.tools.expand"]; !equalKeyIDs(got, []KeyID{"ctrl+x"}) {
		t.Fatalf("app.tools.expand = %#v", got)
	}
}

func TestLoadKeybindingsFileKeepsNamespacedValueWhenBothExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"expandTools":"ctrl+x","app.tools.expand":"ctrl+y"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LoadKeybindingsFile(path)
	if len(config) != 1 || !equalKeyIDs(config["app.tools.expand"], []KeyID{"ctrl+y"}) {
		t.Fatalf("migrated config = %#v, want only app.tools.expand=[ctrl+y]", config)
	}
}

func TestKeybindingsManagerLoadsLegacyNamesInMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"selectConfirm":"enter","interrupt":"ctrl+x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	definitions := append([]KeybindingDefinition{
		{ID: "app.interrupt", DefaultKeys: []KeyID{"escape"}, Description: "Cancel or abort"},
	}, TUIKeybindingDefinitions...)
	manager := NewKeybindingsFromFile(definitions, path)
	user := manager.UserBindings()
	if len(user) != 2 || !equalKeyIDs(user["tui.select.confirm"], []KeyID{"enter"}) || !equalKeyIDs(user["app.interrupt"], []KeyID{"ctrl+x"}) {
		t.Fatalf("user bindings = %#v", user)
	}
	if got := manager.Keys("tui.select.confirm"); !equalKeyIDs(got, []KeyID{"enter"}) {
		t.Fatalf("effective tui.select.confirm = %#v", got)
	}
	if got := manager.Keys("app.interrupt"); !equalKeyIDs(got, []KeyID{"ctrl+x"}) {
		t.Fatalf("effective app.interrupt = %#v", got)
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
