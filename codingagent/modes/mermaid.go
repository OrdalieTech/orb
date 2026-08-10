package modes

import (
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/codingagent/extensions"
	theme "github.com/OrdalieTech/orb/codingagent/modes/theme"
	"github.com/OrdalieTech/orb/internal/mermaid"
	"github.com/OrdalieTech/orb/tui"
)

// MermaidTheme is the slice of the interactive theme the Mermaid transformer
// styles diagram spans with (upstream components/mermaid.ts styleSpan).
type MermaidTheme interface {
	Fg(color, text string) string
	Bold(text string) string
}

// mermaidThemeAdapter styles transformer output with the active interactive theme.
type mermaidThemeAdapter struct{}

func (mermaidThemeAdapter) Fg(color, text string) string { return theme.FG(color, text) }
func (mermaidThemeAdapter) Bold(text string) string      { return theme.Bold(text) }

// marked's lexer normalizes carriage returns before tokenizing.
var mermaidCarriageReturns = strings.NewReplacer("\r\n", "\n", "\r", "\n")

// NewMermaidMarkdownTransformer ports upstream createMermaidMarkdownTransformer
// (components/mermaid.ts): it replaces top-level Mermaid code blocks with
// Unicode terminal diagrams. getMode returns the settings-manager mermaid
// rendering mode ("off" | "final" | "streaming"); a nil theme renders unstyled.
func NewMermaidMarkdownTransformer(getMode func() string, mermaidTheme MermaidTheme) extensions.MarkdownTransformer {
	return func(markdown string, context extensions.MarkdownTransformContext) string {
		mode := getMode()
		if mode == "off" || context.MessageType == "assistant-thinking" || (context.IsStreaming && mode != "streaming") {
			return markdown
		}

		source := mermaidCarriageReturns.Replace(markdown)
		var builder strings.Builder
		last := 0
		for _, token := range tui.LexTopLevelCodeTokens(source) {
			if !isMermaidInfo(token.Info) {
				continue
			}
			art := mermaid.Render(token.Text)
			if art == nil || art.Width > context.AvailableWidth {
				continue
			}
			builder.WriteString(source[last:token.Start])
			if !context.IsStreaming && len(art.Warnings) > 0 {
				suffix := ""
				if len(art.Warnings) > 1 {
					suffix = fmt.Sprintf(" (+%d more)", len(art.Warnings)-1)
				}
				warning := "Mermaid diagram not rendered: " + art.Warnings[0] + suffix
				if mermaidTheme != nil {
					warning = mermaidTheme.Fg("warning", warning)
				}
				builder.WriteString(source[token.Start:token.End])
				builder.WriteString("\n" + mermaidCodeSpan(warning) + "  \n")
			} else {
				lines := art.Plain
				if mermaidTheme != nil {
					lines = mermaidThemedLines(art, mermaidTheme)
				}
				spans := make([]string, len(lines))
				for index, line := range lines {
					spans[index] = mermaidCodeSpan(line)
				}
				// Markdown hard breaks keep every diagram row on its own line.
				builder.WriteString(strings.Join(spans, "  \n") + "\n")
			}
			last = token.End
		}
		builder.WriteString(source[last:])
		return builder.String()
	}
}

func isMermaidInfo(info string) bool {
	fields := strings.Fields(info)
	return len(fields) > 0 && strings.ToLower(fields[0]) == "mermaid"
}

// mermaidCodeSpan encodes a diagram row as inline code so Markdown preserves
// its spacing and box-drawing characters: NBSP for blank rows (an empty code
// span has no visible height), a delimiter longer than any backtick run in the
// content, and space padding when the content starts or ends with a backtick.
func mermaidCodeSpan(line string) string {
	content := line
	if content == "" {
		content = "\u00a0"
	}
	longestRun, run := 0, 0
	for index := 0; index < len(content); index++ {
		if content[index] == '`' {
			run++
			longestRun = max(longestRun, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", longestRun+1)
	padding := ""
	if strings.HasPrefix(content, "`") || strings.HasSuffix(content, "`") {
		padding = " "
	}
	return fence + padding + content + padding + fence
}

func mermaidThemedLines(art *mermaid.Art, mermaidTheme MermaidTheme) []string {
	lines := make([]string, len(art.Styled))
	for index, row := range art.Styled {
		var builder strings.Builder
		for _, span := range row {
			builder.WriteString(mermaidStyleSpan(span, mermaidTheme))
		}
		lines[index] = builder.String()
	}
	return lines
}

func mermaidStyleSpan(span mermaid.Span, mermaidTheme MermaidTheme) string {
	switch span.Cls {
	case "border":
		return mermaidTheme.Fg("borderMuted", span.Text)
	case "text":
		return mermaidTheme.Fg("text", span.Text)
	case "edge":
		return mermaidTheme.Fg("accent", span.Text)
	case "edgeLabel":
		return mermaidTheme.Fg("muted", span.Text)
	case "title":
		return mermaidTheme.Fg("accent", mermaidTheme.Bold(span.Text))
	default: // "none"
		return span.Text
	}
}
