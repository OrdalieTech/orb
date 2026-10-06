package tui

import (
	"fmt"
	"reflect"
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

func TestSelectListDimensions(t *testing.T) {
	for _, height := range []int{-100, -7, -2, -1, 0, 1, 2, 3, 5} {
		for _, width := range []int{-100, -1, 0, 1, 2, 3, 4, 8, 40, 41, 80} {
			for _, count := range []int{0, 1, 3} {
				t.Run(fmt.Sprintf("height=%d/width=%d/items=%d", height, width, count), func(t *testing.T) {
					items := []SelectItem{{Value: "alpha", Description: "A description"}, {Value: "beta"}, {Value: "gamma"}}
					list := NewSelectList(items[:count], height, testSelectTheme, SelectListLayoutOptions{})
					want := NewSelectList(items[:count], max(1, height), testSelectTheme, SelectListLayoutOptions{})
					for _, filter := range []string{"", "missing", ""} {
						list.SetFilter(filter)
						want.SetFilter(filter)
						for range 4 {
							gotLines, wantLines := list.Render(width), want.Render(width)
							if !reflect.DeepEqual(gotLines, wantLines) {
								t.Fatalf("render = %q, want %q", gotLines, wantLines)
							}
							press(list, "\x1b[B")
							press(want, "\x1b[B")
						}
					}
				})
			}
		}
	}
}
