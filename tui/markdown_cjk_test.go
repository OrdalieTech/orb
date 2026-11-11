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

// HTML comments render nothing, block or inline, and leave no extra blank line (pi #11710).
func TestMarkdownHidesHTMLComments(t *testing.T) {
	render := func(text string) string {
		lines := NewMarkdown(text, 0, 0, MarkdownTheme{}, nil, nil).Render(60)
		for index, line := range lines {
			lines[index] = strings.TrimRight(StripANSI(line), " ")
		}
		return strings.Join(lines, "\n")
	}
	for _, test := range []struct{ text, want string }{
		{"## [1.0.0]\n\n<!-- release-notes:start -->\nFixed a bug.\n\nSee <!-- todo: link --> the docs.", "## [1.0.0]\n\nFixed a bug.\n\nSee the docs."},
		{"<!-- start -->\n\n## [1.0.0]\n\n<!-- a -->\n\n- item\n\n<!-- b -->\n\n## [0.9.0]\n\n<!-- end -->", "## [1.0.0]\n\n- item\n\n## [0.9.0]"},
		{"<!--\nmulti\nline\n-->\ntext", "text"},
		{"text\n\n<!-- streaming", "text"},
		{"> q\n>\n> <!-- c -->\n>\n> r\n\n- a\n\n  <!-- c -->\n\n  b\n- <!-- c --> item", "> q\n>\n> r\n\n- a\n\n  b\n- item"},
	} {
		if got, want := render(test.text), render(test.want); got != want {
			t.Errorf("%q rendered as %q, want %q", test.text, got, want)
		}
	}
	for text, want := range map[string]string{
		"with <tag> markers":       "with <tag> markers",
		"<!-- c --> trailing text": "trailing text",
		"<div>\n<!-- c -->a</div>": "<div>\na</div>",
	} {
		if got := render(text); got != want {
			t.Errorf("%q rendered as %q, want %q", text, got, want)
		}
	}
}
