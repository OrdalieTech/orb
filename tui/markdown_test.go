package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownCacheInvalidationAndPadding(t *testing.T) {
	theme := MarkdownTheme{Bold: func(value string) string { return "\x1b[1m" + value + "\x1b[22m" }}
	markdown := NewMarkdown("**hello**", 1, 1, theme, nil, nil)
	first := markdown.Render(12)
	second := markdown.Render(12)
	if len(first) != 3 || first[0] != strings.Repeat(" ", 12) || first[2] != strings.Repeat(" ", 12) {
		t.Fatalf("render = %#v", first)
	}
	if &first[0] != &second[0] {
		t.Fatal("cached render did not reuse the line slice")
	}
	markdown.SetText("changed")
	if changed := markdown.Render(12); reflect.DeepEqual(changed, first) {
		t.Fatal("SetText did not invalidate the render cache")
	}
}

func TestMarkdownBackgroundAndCodeIndentFillWidth(t *testing.T) {
	background := func(value string) string { return "<bg>" + value + "</bg>" }
	theme := MarkdownTheme{CodeBlockIndent: ">>"}
	markdown := NewMarkdown("```\nx\n```", 0, 0, theme, &DefaultTextStyle{Background: background}, nil)
	lines := markdown.Render(10)
	if len(lines) != 3 {
		t.Fatalf("lines = %#v", lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "<bg>") || !strings.HasSuffix(line, "</bg>") {
			t.Fatalf("background did not cover line: %q", line)
		}
	}
	if !strings.Contains(lines[1], ">>x") {
		t.Fatalf("code indent missing: %q", lines[1])
	}
}

func TestMarkdownEmptyContentMatchesUpstream(t *testing.T) {
	markdown := NewMarkdown(" \n\t", 2, 2, MarkdownTheme{}, nil, nil)
	if lines := markdown.Render(20); len(lines) != 0 {
		t.Fatalf("empty markdown = %#v", lines)
	}
}
