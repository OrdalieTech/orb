package tui

import (
	"strings"
	"testing"
)

var testSelectTheme = SelectListTheme{}

func visibleIndexOf(t *testing.T, line, text string) int {
	t.Helper()
	index := strings.Index(line, text)
	if index == -1 {
		t.Fatalf("%q not found in %q", text, line)
	}
	return VisibleWidth(line[:index])
}

// Ported from upstream packages/tui/test/select-list.test.ts.
func TestSelectListNormalizesDescriptions(t *testing.T) {
	list := NewSelectList([]SelectItem{{Value: "test", Label: "test", Description: "Line one\nLine two\nLine three"}}, 5, testSelectTheme, SelectListLayoutOptions{})
	rendered := list.Render(100)
	if len(rendered) == 0 {
		t.Fatal("no rendered lines")
	}
	if strings.Contains(rendered[0], "\n") {
		t.Fatalf("rendered line contains newline: %q", rendered[0])
	}
	if !strings.Contains(rendered[0], "Line one Line two Line three") {
		t.Fatalf("description not normalized: %q", rendered[0])
	}
}

func TestSelectListDescriptionAlignment(t *testing.T) {
	list := NewSelectList([]SelectItem{
		{Value: "short", Label: "short", Description: "short description"},
		{Value: "very-long-command-name-that-needs-truncation", Label: "very-long-command-name-that-needs-truncation", Description: "long description"},
	}, 5, testSelectTheme, SelectListLayoutOptions{})
	rendered := list.Render(80)
	if visibleIndexOf(t, rendered[0], "short description") != visibleIndexOf(t, rendered[1], "long description") {
		t.Fatalf("descriptions misaligned:\n%q\n%q", rendered[0], rendered[1])
	}
}

func TestSelectListPrimaryColumnBounds(t *testing.T) {
	list := NewSelectList([]SelectItem{
		{Value: "a", Label: "a", Description: "first"},
		{Value: "bb", Label: "bb", Description: "second"},
	}, 5, testSelectTheme, SelectListLayoutOptions{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 20})
	rendered := list.Render(80)
	if got := visibleIndexOf(t, rendered[0], "first"); got != 14 {
		t.Fatalf("first at %d, want 14 (%q)", got, rendered[0])
	}
	if got := visibleIndexOf(t, rendered[1], "second"); got != 14 {
		t.Fatalf("second at %d, want 14 (%q)", got, rendered[1])
	}

	list = NewSelectList([]SelectItem{
		{Value: "very-long-command-name-that-needs-truncation", Label: "very-long-command-name-that-needs-truncation", Description: "first"},
		{Value: "short", Label: "short", Description: "second"},
	}, 5, testSelectTheme, SelectListLayoutOptions{MinPrimaryColumnWidth: 12, MaxPrimaryColumnWidth: 20})
	rendered = list.Render(80)
	if got := visibleIndexOf(t, rendered[0], "first"); got != 22 {
		t.Fatalf("first at %d, want 22 (%q)", got, rendered[0])
	}
	if got := visibleIndexOf(t, rendered[1], "second"); got != 22 {
		t.Fatalf("second at %d, want 22 (%q)", got, rendered[1])
	}
}

func TestSelectListTruncatePrimaryOverride(t *testing.T) {
	list := NewSelectList([]SelectItem{
		{Value: "very-long-command-name-that-needs-truncation", Label: "very-long-command-name-that-needs-truncation", Description: "first"},
		{Value: "short", Label: "short", Description: "second"},
	}, 5, testSelectTheme, SelectListLayoutOptions{
		MinPrimaryColumnWidth: 12,
		MaxPrimaryColumnWidth: 12,
		TruncatePrimary: func(ctx SelectListTruncatePrimaryContext) string {
			if runeLen(ctx.Text) <= ctx.MaxWidth {
				return ctx.Text
			}
			return runeSlice(ctx.Text, 0, max(0, ctx.MaxWidth-1)) + "…"
		},
	})
	rendered := list.Render(80)
	if !strings.Contains(rendered[0], "…") {
		t.Fatalf("no ellipsis in %q", rendered[0])
	}
	if visibleIndexOf(t, rendered[0], "first") != visibleIndexOf(t, rendered[1], "second") {
		t.Fatalf("descriptions misaligned:\n%q\n%q", rendered[0], rendered[1])
	}
}

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
