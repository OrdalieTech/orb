package modes

// Port of upstream test/mermaid.test.ts.

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
)

type mermaidTransformOptions struct {
	maxWidth    int
	isStreaming bool
	messageType string
	mode        string
	theme       MermaidTheme
}

func transformMermaid(markdown string, options mermaidTransformOptions) string {
	mode := options.mode
	if mode == "" {
		mode = "streaming"
	}
	messageType := options.messageType
	if messageType == "" {
		messageType = "assistant"
	}
	maxWidth := options.maxWidth
	if maxWidth == 0 {
		maxWidth = 100
	}
	transformer := NewMermaidMarkdownTransformer(func() string { return mode }, options.theme)
	return transformer(markdown, extensions.MarkdownTransformContext{
		MessageType:    messageType,
		IsStreaming:    options.isStreaming,
		AvailableWidth: maxWidth,
	})
}

func mustContain(t *testing.T, rendered, want string) {
	t.Helper()
	if !strings.Contains(rendered, want) {
		t.Errorf("output missing %q:\n%s", want, rendered)
	}
}

func mustNotContain(t *testing.T, rendered, want string) {
	t.Helper()
	if strings.Contains(rendered, want) {
		t.Errorf("output unexpectedly contains %q:\n%s", want, rendered)
	}
}

type markerMermaidTheme struct{}

func (markerMermaidTheme) Fg(color, text string) string {
	return "<" + color + ">" + text + "</" + color + ">"
}

func (markerMermaidTheme) Bold(text string) string { return "<bold>" + text + "</bold>" }

func TestMermaidReplacesCodeBlocksWithDiagrams(t *testing.T) {
	markdown := "Before\n\n```mermaid\nflowchart LR\n  A[Start] --> B[Done]\n```\nAfter"
	rendered := transformMermaid(markdown, mermaidTransformOptions{})

	mustContain(t, rendered, "Before")
	mustContain(t, rendered, "┌───────┐")
	mustContain(t, rendered, "│ Start ├───▶│ Done │")
	mustContain(t, rendered, "└───────┘    └──────┘`\nAfter")
	mustNotContain(t, rendered, "```mermaid")
	mustContain(t, rendered, "After")
}

func TestMermaidLeavesUnsupportedAndOversizedUnchanged(t *testing.T) {
	unsupported := "```mermaid\npie\n  title Pets\n  \"Dogs\" : 4\n```"
	oversized := "```mermaid\nflowchart LR\n  A[Start] --> B[Done]\n```"

	if got := transformMermaid(unsupported, mermaidTransformOptions{}); got != unsupported {
		t.Errorf("unsupported diagram changed: %q", got)
	}
	if got := transformMermaid(oversized, mermaidTransformOptions{maxWidth: 10}); got != oversized {
		t.Errorf("oversized diagram changed: %q", got)
	}
}

func TestMermaidMapsSemanticSpansThroughTheme(t *testing.T) {
	rendered := transformMermaid("```mermaid\nflowchart LR\n  A --> B\n```", mermaidTransformOptions{theme: markerMermaidTheme{}})

	mustContain(t, rendered, "<borderMuted>")
	mustContain(t, rendered, "<accent>")
}

func TestMermaidRendersIncompleteBlocksDuringStreaming(t *testing.T) {
	partialMarkdown := "```mermaid\nflowchart LR\n  A --> B"

	mustContain(t, transformMermaid(partialMarkdown, mermaidTransformOptions{isStreaming: true}), "───▶")
}

func TestMermaidFallsBackWithWarningAfterStreaming(t *testing.T) {
	markdown := "```mermaid\nflowchart LR\n  A[Foo]:::highlight --> B[Bar]\n```"
	final := transformMermaid(markdown, mermaidTransformOptions{})
	followedByText := transformMermaid(markdown+"\nFollowing text", mermaidTransformOptions{})
	streaming := transformMermaid(markdown, mermaidTransformOptions{isStreaming: true})

	mustContain(t, final, markdown)
	mustContain(t, final, "```\n`Mermaid diagram not rendered")
	mustContain(t, final, `dropped, expected a link: ":::highlight --> B[Bar]"`)
	mustNotContain(t, final, "more)")
	mustContain(t, followedByText, "  \nFollowing text")
	mustNotContain(t, streaming, "Mermaid diagram not rendered")
	mustNotContain(t, streaming, "```mermaid")
	mustContain(t, streaming, "│ Foo │")
}

func TestMermaidSummarizesAdditionalWarnings(t *testing.T) {
	markdown := "```mermaid\nflowchart LR\n  A[Foo]:::highlight --> B[Bar]\n  C[Baz]:::other --> D[Qux]\n```"
	rendered := transformMermaid(markdown, mermaidTransformOptions{})

	mustContain(t, rendered, markdown)
	mustContain(t, rendered, `dropped, expected a link: ":::highlight --> B[Bar]"`)
	mustContain(t, rendered, "(+1 more)")
	mustNotContain(t, rendered, `dropped, expected a link: ":::other --> D[Qux]"`)
}

func TestMermaidRespectsRenderingModesAndThinking(t *testing.T) {
	markdown := "```mermaid\nflowchart LR\n  A --> B\n```"

	if got := transformMermaid(markdown, mermaidTransformOptions{mode: "off"}); got != markdown {
		t.Errorf("mode off changed markdown: %q", got)
	}
	if got := transformMermaid(markdown, mermaidTransformOptions{mode: "final", isStreaming: true}); got != markdown {
		t.Errorf("mode final while streaming changed markdown: %q", got)
	}
	mustNotContain(t, transformMermaid(markdown, mermaidTransformOptions{mode: "final"}), "```mermaid")
	if got := transformMermaid(markdown, mermaidTransformOptions{messageType: "assistant-thinking"}); got != markdown {
		t.Errorf("thinking block changed markdown: %q", got)
	}
}
