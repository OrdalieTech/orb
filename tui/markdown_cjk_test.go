package tui

import (
	"strings"
	"testing"
)

// Bold and italic next to CJK punctuation render, as marked-cjk-friendly makes them in pi 1.4.0.
func TestMarkdownEmphasisBesideCJKPunctuation(t *testing.T) {
	for _, text := range []string{"这是**「重要」**内容", "これは**「重要」**です", "这是_「重要」_内容", "_word_中"} {
		rendered := strings.Join(NewMarkdown(text, 0, 0, MarkdownTheme{}, nil, nil).Render(80), "\n")
		if strings.Contains(rendered, "**") || strings.Contains(rendered, "_") {
			t.Fatalf("%s rendered as %q", text, rendered)
		}
	}
	for _, text := range []string{"a**b", "snake_case_word"} {
		if rendered := strings.Join(NewMarkdown(text, 0, 0, MarkdownTheme{}, nil, nil).Render(80), "\n"); strings.TrimSpace(rendered) != text {
			t.Fatalf("%s rendered as %q", text, rendered)
		}
	}
}
