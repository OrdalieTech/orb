package tui

import (
	"strings"
	"testing"
)

var testSelectTheme = SelectListTheme{}

func TestSelectListNavigationAndCallbacks(t *testing.T) {
	items := []SelectItem{{Value: "one"}, {Value: "two"}, {Value: "three"}}
	list := NewSelectList(items, 5, testSelectTheme, SelectListLayoutOptions{})

	var selected, cancelled []string
	list.OnSelect = func(item SelectItem) { selected = append(selected, item.Value) }
	list.OnCancel = func() { cancelled = append(cancelled, "cancel") }

	// Up from the top wraps to the bottom; down from the bottom wraps to top.
	press(list, "\x1b[A")
	if item, _ := list.GetSelectedItem(); item.Value != "three" {
		t.Fatalf("after wrap-up: %q", item.Value)
	}
	press(list, "\x1b[B")
	if item, _ := list.GetSelectedItem(); item.Value != "one" {
		t.Fatalf("after wrap-down: %q", item.Value)
	}
	press(list, "\x1b[B", "\r")
	if len(selected) != 1 || selected[0] != "two" {
		t.Fatalf("selected = %v", selected)
	}
	press(list, "\x1b")
	if len(cancelled) != 1 {
		t.Fatalf("cancelled = %v", cancelled)
	}
}

func TestSelectListFilterAndScroll(t *testing.T) {
	items := []SelectItem{{Value: "alpha"}, {Value: "beta"}, {Value: "alp"}, {Value: "gamma"}}
	list := NewSelectList(items, 2, testSelectTheme, SelectListLayoutOptions{})
	list.SetFilter("al")
	if item, _ := list.GetSelectedItem(); item.Value != "alpha" {
		t.Fatalf("after filter: %q", item.Value)
	}
	rendered := list.Render(40)
	if len(rendered) != 2 {
		t.Fatalf("filtered render = %q", rendered)
	}

	list.SetFilter("zzz")
	rendered = list.Render(40)
	if len(rendered) != 1 || !strings.Contains(rendered[0], "No matching commands") {
		t.Fatalf("no-match render = %q", rendered)
	}

	// Scroll indicator with more items than maxVisible.
	list = NewSelectList(items, 2, testSelectTheme, SelectListLayoutOptions{})
	rendered = list.Render(40)
	if len(rendered) != 3 || !strings.Contains(rendered[2], "(1/4)") {
		t.Fatalf("scroll render = %q", rendered)
	}
}
