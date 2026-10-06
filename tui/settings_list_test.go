package tui

import (
	"fmt"
	"reflect"
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

func TestSettingsListDimensions(t *testing.T) {
	for _, height := range []int{-100, -7, -2, -1, 0, 1, 2, 3, 5} {
		for _, width := range []int{-100, -1, 0, 1, 2, 3, 4, 8, 40, 41, 80} {
			for _, count := range []int{0, 1, 3} {
				t.Run(fmt.Sprintf("height=%d/width=%d/items=%d", height, width, count), func(t *testing.T) {
					items := []SettingItem{{ID: "alpha", Label: "Alpha", CurrentValue: "on", Description: "A description that can wrap onto several lines"}, {ID: "beta", Label: "Beta"}, {ID: "gamma", Label: "Gamma"}}
					for _, options := range []SettingsListOptions{{}, {EnableSearch: true}, {FixedGeometry: true}, {EnableSearch: true, FixedGeometry: true}} {
						list := NewSettingsList(items[:count], height, testSettingsTheme, nil, nil, options)
						want := NewSettingsList(items[:count], max(1, height), testSettingsTheme, nil, nil, options)
						for _, filter := range []string{"", "missing", ""} {
							list.applyFilter(filter)
							want.applyFilter(filter)
							for range 4 {
								gotLines, wantLines := list.Render(width), want.Render(width)
								if !reflect.DeepEqual(gotLines, wantLines) {
									t.Fatalf("options=%+v render = %q, want %q", options, gotLines, wantLines)
								}
								press(list, "\x1b[B")
								press(want, "\x1b[B")
							}
						}
					}
				})
			}
		}
	}
}
