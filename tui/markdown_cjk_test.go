package tui

import (
	"strings"
	"testing"
)

// Bold and italic next to CJK punctuation render, as marked-cjk-friendly makes them in pi 1.4.0.
func TestMarkdownEmphasisBesideCJKPunctuation(t *testing.T) {
	for _, text := range []string{"这是**「重要」**内容", "これは**「重要」**です"} {
		rendered := strings.Join(NewMarkdown(text, 0, 0, MarkdownTheme{}, nil, nil).Render(80), "\n")
		if strings.Contains(rendered, "**") {
			t.Fatalf("%s rendered as %q", text, rendered)
		}
	}
	if rendered := strings.Join(NewMarkdown("a**b", 0, 0, MarkdownTheme{}, nil, nil).Render(80), "\n"); !strings.Contains(rendered, "**") {
		t.Fatalf("an unclosed run became emphasis: %q", rendered)
	}
}
