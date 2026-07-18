package tui

import (
	"strings"
	"testing"
)

var testSettingsTheme = SettingsListTheme{Cursor: "→ "}

// SettingsList behavior is upstream-untested; these tests pin the ported
// semantics: cycling, submenus, search, hints.
func TestSettingsListCycleValues(t *testing.T) {
	var changes []string
	list := NewSettingsList([]SettingItem{
		{ID: "theme", Label: "Theme", CurrentValue: "dark", Values: []string{"dark", "light"}},
	}, 5, testSettingsTheme, func(id, value string) { changes = append(changes, id+"="+value) }, func() {}, SettingsListOptions{})

	press(list, "\r")
	press(list, " ")
	if len(changes) != 2 || changes[0] != "theme=light" || changes[1] != "theme=dark" {
		t.Fatalf("changes = %v", changes)
	}
}

func TestSettingsListSubmenu(t *testing.T) {
	var changes []string
	var done func(*string)
	submenu := &Text{}
	list := NewSettingsList([]SettingItem{
		{ID: "model", Label: "Model", CurrentValue: "a", Submenu: func(current string, doneCallback func(*string)) Component {
			if current != "a" {
				t.Fatalf("submenu current = %q", current)
			}
			done = doneCallback
			return submenu
		}},
	}, 5, testSettingsTheme, func(id, value string) { changes = append(changes, id+"="+value) }, func() {}, SettingsListOptions{})

	press(list, "\r")
	if done == nil {
		t.Fatal("submenu not opened")
	}
	// While open, render delegates to the submenu.
	submenu.SetText("submenu content")
	rendered := list.Render(40)
	if len(rendered) == 0 || !strings.Contains(strings.Join(rendered, "\n"), "submenu content") {
		t.Fatalf("submenu render = %q", rendered)
	}

	value := "b"
	done(&value)
	if len(changes) != 1 || changes[0] != "model=b" {
		t.Fatalf("changes = %v", changes)
	}
	rendered = list.Render(40)
	if !strings.Contains(strings.Join(rendered, "\n"), "Model") {
		t.Fatalf("main list not restored: %q", rendered)
	}
}

func TestSettingsListSynchronousSubmenuCompletion(t *testing.T) {
	var changes []string
	list := NewSettingsList([]SettingItem{{
		ID: "model", Label: "Model", CurrentValue: "a",
		Submenu: func(_ string, done func(*string)) Component {
			value := "b"
			done(&value)
			return &Text{}
		},
	}}, 5, testSettingsTheme, func(id, value string) {
		changes = append(changes, id+"="+value)
	}, func() {}, SettingsListOptions{})

	press(list, "\r")
	if len(changes) != 1 || changes[0] != "model=b" {
		t.Fatalf("changes = %v", changes)
	}
	if rendered := strings.Join(list.Render(40), "\n"); !strings.Contains(rendered, "Model") || !strings.Contains(rendered, "b") {
		t.Fatalf("main list not restored after synchronous done: %q", rendered)
	}
}

func TestSettingsListEscapeCancels(t *testing.T) {
	cancelled := 0
	list := NewSettingsList([]SettingItem{{ID: "x", Label: "X", CurrentValue: "1"}}, 5, testSettingsTheme, func(string, string) {}, func() { cancelled++ }, SettingsListOptions{})
	press(list, "\x1b")
	if cancelled != 1 {
		t.Fatalf("cancelled = %d", cancelled)
	}
}

func TestSettingsListSearch(t *testing.T) {
	list := NewSettingsList([]SettingItem{
		{ID: "theme", Label: "Theme", CurrentValue: "dark"},
		{ID: "model", Label: "Model", CurrentValue: "gpt"},
	}, 5, testSettingsTheme, func(string, string) {}, func() {}, SettingsListOptions{EnableSearch: true})

	press(list, "m", "o", "d")
	rendered := strings.Join(list.Render(60), "\n")
	if strings.Contains(rendered, "Theme") {
		t.Fatalf("filter did not hide Theme: %q", rendered)
	}
	if !strings.Contains(rendered, "Model") {
		t.Fatalf("filter dropped Model: %q", rendered)
	}
	if !strings.Contains(rendered, "Type to search") {
		t.Fatalf("hint missing: %q", rendered)
	}

	press(list, "z")
	rendered = strings.Join(list.Render(60), "\n")
	if !strings.Contains(rendered, "No matching settings") {
		t.Fatalf("no-match hint missing: %q", rendered)
	}
}

func TestSettingsListDescriptionAndScroll(t *testing.T) {
	list := NewSettingsList([]SettingItem{
		{ID: "a", Label: "A", CurrentValue: "1", Description: "Longer description for the selected item"},
		{ID: "b", Label: "B", CurrentValue: "2"},
		{ID: "c", Label: "C", CurrentValue: "3"},
	}, 2, testSettingsTheme, func(string, string) {}, func() {}, SettingsListOptions{})

	rendered := strings.Join(list.Render(60), "\n")
	if !strings.Contains(rendered, "Longer description") {
		t.Fatalf("description missing: %q", rendered)
	}
	if !strings.Contains(rendered, "(1/3)") {
		t.Fatalf("scroll info missing: %q", rendered)
	}
	if !strings.Contains(rendered, "Enter/Space to change") {
		t.Fatalf("hint missing: %q", rendered)
	}
}

func TestSettingsListUpdateValue(t *testing.T) {
	list := NewSettingsList([]SettingItem{{ID: "x", Label: "X", CurrentValue: "old"}}, 5, testSettingsTheme, func(string, string) {}, func() {}, SettingsListOptions{})
	list.UpdateValue("x", "new")
	rendered := strings.Join(list.Render(60), "\n")
	if !strings.Contains(rendered, "new") {
		t.Fatalf("value not updated: %q", rendered)
	}
}
