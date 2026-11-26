package modes

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

const (
	thoughtStreamingLines = 2
	reasoningLimit        = 4096
	reasoningTail         = 512
)

// ─────────────────────────────────────────────────────────────
// Markdown transformers
// ─────────────────────────────────────────────────────────────

// newMarkdownTransform curries the message context over the transformer chain
// for a tui.Markdown Transform option.
func newMarkdownTransform(messageType string, isStreaming bool, transformers []extensions.MarkdownTransformer) func(string, int) string {
	if len(transformers) == 0 {
		return nil
	}
	return func(markdown string, availableWidth int) string {
		context := extensions.MarkdownTransformContext{
			MessageType:    messageType,
			IsStreaming:    isStreaming,
			AvailableWidth: availableWidth,
		}
		transformed := markdown
		for _, transformer := range transformers {
			transformed = applyMarkdownTransformer(transformer, transformed, context)
		}
		return transformed
	}
}

// applyMarkdownTransformer keeps the current markdown when a transformer
// panics.
func applyMarkdownTransformer(transformer extensions.MarkdownTransformer, markdown string, context extensions.MarkdownTransformContext) (result string) {
	defer func() {
		if recover() != nil {
			result = markdown
		}
	}()
	return transformer(markdown, context)
}

// ─────────────────────────────────────────────────────────────
// AssistantMessageComponent
// ─────────────────────────────────────────────────────────────

type assistantMarkdown struct {
	text      string
	thinking  bool
	component *tui.Markdown
}

type AssistantMessageComponent struct {
	dirty             bool
	markdown          map[int]assistantMarkdown
	renderTheme       *theme.Theme
	mu                sync.Mutex
	contentContainer  *tui.Container
	hideThinking      bool
	mdTheme           tui.MarkdownTheme
	thinkingLabel     string
	outputPad         int
	transformers      []extensions.MarkdownTransformer
	isStreaming       bool
	message           *ai.AssistantMessage
	hasToolCalls      bool
	hasThinking       bool
	hasLongReasoning  bool
	expandedReasoning bool
	toggleHint        tui.Component
	toggleStart       int
	toggleEnd         int
	onChange          func()
	// folded reasoning shows inside the tool group it came between, not here.
	folded bool
	// compactTop suppresses the leading blank line for thought-only blocks sitting right after work.
	compactTop bool
}

func NewAssistantMessageComponent(
	message *ai.AssistantMessage,
	hideThinking bool,
	mdTheme tui.MarkdownTheme,
	thinkingLabel string,
	outputPad int,
	transformers []extensions.MarkdownTransformer,
) *AssistantMessageComponent {
	c := &AssistantMessageComponent{
		renderTheme:      theme.Current().Palette(),
		contentContainer: &tui.Container{},
		hideThinking:     hideThinking,
		mdTheme:          mdTheme,
		thinkingLabel:    thinkingLabel,
		outputPad:        outputPad,
		transformers:     transformers,
	}
	if message != nil {
		c.UpdateContent(message)
	}
	return c
}

func (c *AssistantMessageComponent) UpdateContent(message *ai.AssistantMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setMessageLocked(message)
}

// UpdateContentStreaming also records whether the message is still streaming,
// which feeds the markdown transformer context.
func (c *AssistantMessageComponent) UpdateContentStreaming(message *ai.AssistantMessage, isStreaming bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.isStreaming != isStreaming && len(c.transformers) > 0 {
		c.markdown = nil
	}
	c.isStreaming = isStreaming
	c.setMessageLocked(message)
}

// Only presentation fields are read after the provider resumes mutating its partial.
func (c *AssistantMessageComponent) setMessageLocked(message *ai.AssistantMessage) {
	copy := *message
	copy.ErrorMessage = cloneStringPointer(message.ErrorMessage)
	copy.Content = make(ai.AssistantContent, len(message.Content))
	for i, block := range message.Content {
		switch block := block.(type) {
		case *ai.TextContent:
			value := *block
			copy.Content[i] = &value
		case *ai.ThinkingContent:
			value := *block
			copy.Content[i] = &value
		default:
			copy.Content[i] = block
		}
	}
	c.message, c.dirty = &copy, true
}

func (c *AssistantMessageComponent) SetHideThinkingBlock(hidden bool, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hideThinking, c.thinkingLabel = hidden, label
	c.dirty = true
}

func (c *AssistantMessageComponent) SetHiddenThinkingLabel(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.thinkingLabel = label
	c.dirty = true
}

func (c *AssistantMessageComponent) updateContentLocked(message *ai.AssistantMessage) {
	c.renderTheme = theme.Current().Palette()
	previous := c.markdown
	c.markdown = make(map[int]assistantMarkdown)
	addMarkdown := func(index int, text string, thinking bool) {
		cached, ok := previous[index]
		if !ok || cached.thinking != thinking {
			var style *tui.DefaultTextStyle
			kind := "assistant"
			if thinking {
				kind = "assistant-thinking"
				style = &tui.DefaultTextStyle{Color: func(text string) string { return theme.FG("thinkingText", text) }, Italic: true}
			}
			cached = assistantMarkdown{text: text, thinking: thinking, component: tui.NewMarkdown(text, c.outputPad+2, 0, c.mdTheme, style, &tui.MarkdownOptions{Transform: newMarkdownTransform(kind, c.isStreaming, c.transformers)})}
		} else if cached.text != text {
			cached.component.SetText(text)
			cached.text = text
		}
		c.markdown[index] = cached
		c.contentContainer.AddChild(cached.component)
	}
	c.contentContainer.Clear()
	c.hasThinking = false
	c.hasLongReasoning = false
	c.toggleHint = nil

	hasVisible := false
	hasProse := false
	hasToolCalls := false
	for _, block := range message.Content {
		switch value := block.(type) {
		case *ai.TextContent:
			if strings.TrimSpace(value.Text) != "" {
				hasVisible = true
				hasProse = true
			}
		case *ai.ThinkingContent:
			hasVisible = hasVisible || strings.TrimSpace(value.Thinking) != ""
		case *ai.ToolCall:
			hasToolCalls = true
		}
	}
	c.hasToolCalls = hasToolCalls
	if hasVisible && (!c.compactTop || hasProse) {
		c.contentContainer.AddChild(tui.NewSpacer(1))
	}

	for index := 0; index < len(message.Content); index++ {
		switch value := message.Content[index].(type) {
		case *ai.TextContent:
			if text := strings.TrimSpace(value.Text); text != "" {
				addMarkdown(index, text, false)
			}
		case *ai.ThinkingContent:
			thinkingBlocks := make([]string, 0, 1)
			thinkingBytes := 0
			for ; index < len(message.Content); index++ {
				thinking, ok := message.Content[index].(*ai.ThinkingContent)
				if !ok {
					break
				}
				if text := strings.TrimSpace(thinking.Thinking); text != "" {
					thinkingBlocks = append(thinkingBlocks, text)
					thinkingBytes += len(text) + 2
				}
			}
			index--
			if len(thinkingBlocks) == 0 {
				continue
			}
			c.hasThinking = true
			long := thinkingBytes > reasoningLimit
			c.hasLongReasoning = c.hasLongReasoning || long
			tokens := max(1, thinkingBytes/4)
			title := firstReasoningLine(thinkingBlocks)
			if c.hideThinking {
				label := c.thinkingLabel
				if label == "" {
					label = "Thinking..."
				}
				c.contentContainer.AddChild(tui.NewText(theme.Italic(theme.FG("thinkingText", label)), c.outputPad+2, 0, nil))
			} else if c.isStreaming {
				tail := thinkingBlocks[len(thinkingBlocks)-1]
				if len(tail) > reasoningTail {
					start := len(tail) - reasoningTail
					for start < len(tail) && !utf8.RuneStart(tail[start]) {
						start++
					}
					tail = tail[start:]
				}
				c.contentContainer.AddChild(railedComponent{
					inner: visualLineTail{text: theme.Italic(theme.FG("thinkingText", tail)), maxLines: thoughtStreamingLines, paddingX: 0},
				})
			} else if !c.expandedReasoning {
				row := &thoughtSummaryRow{title: title, tokens: tokens, expanded: false}
				c.toggleHint = row
				c.contentContainer.AddChild(row)
			} else {
				row := &thoughtSummaryRow{title: title, tokens: tokens, expanded: true}
				c.toggleHint = row
				c.contentContainer.AddChild(row)
				body := tui.NewMarkdown(joinReasoning(thinkingBlocks), 0, 0, c.mdTheme,
					&tui.DefaultTextStyle{Color: func(text string) string { return theme.FG("thinkingText", text) }, Italic: true},
					&tui.MarkdownOptions{Transform: newMarkdownTransform("assistant-thinking", false, c.transformers)},
				)
				c.contentContainer.AddChild(railedComponent{inner: body})
			}
			for trailing := index + 1; trailing < len(message.Content); trailing++ {
				switch next := message.Content[trailing].(type) {
				case *ai.TextContent:
					if strings.TrimSpace(next.Text) != "" {
						c.contentContainer.AddChild(tui.NewSpacer(1))
						trailing = len(message.Content)
					}
				case *ai.ThinkingContent:
					if strings.TrimSpace(next.Thinking) != "" {
						c.contentContainer.AddChild(tui.NewSpacer(1))
						trailing = len(message.Content)
					}
				}
			}
		}
	}

	if message.StopReason == ai.StopReasonLength {
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", "Error: Model stopped because it reached the maximum output token limit. The response may be incomplete."), c.outputPad+2, 0, nil))
	} else if !hasToolCalls && message.StopReason == ai.StopReasonAborted {
		abortMessage := "Operation aborted"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" && *message.ErrorMessage != "Request was aborted" {
			abortMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", abortMessage), c.outputPad+2, 0, nil))
	} else if !hasToolCalls && message.StopReason == ai.StopReasonError {
		errorMessage := "Unknown error"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" {
			errorMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", "Error: "+errorMessage), c.outputPad+2, 0, nil))
	}
}

func (c *AssistantMessageComponent) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.markdown = nil
	c.dirty = true
	c.contentContainer.Invalidate()
}
func (c *AssistantMessageComponent) HandleMouse(event tui.MouseEvent) bool {
	if event.Type != tui.MouseRelease || event.Button != 0 && event.Button != 3 {
		return false
	}
	c.mu.Lock()
	if c.hideThinking || c.isStreaming || !c.hasThinking || event.Row < c.toggleStart || event.Row >= c.toggleEnd {
		c.mu.Unlock()
		return false
	}
	c.expandedReasoning = !c.expandedReasoning
	c.dirty = true
	onChange := c.onChange
	c.mu.Unlock()
	if onChange != nil {
		onChange()
	}
	return true
}

// joinReasoning stacks reasoning parts that are only a title ("**Checking
// support**") line under line; parts with a body stay paragraphs apart.
func joinReasoning(blocks []string) string {
	var joined strings.Builder
	for index, block := range blocks {
		if index > 0 {
			if strings.Contains(blocks[index-1], "\n") || strings.Contains(block, "\n") {
				joined.WriteString("\n")
			}
			joined.WriteString("\n")
		}
		joined.WriteString(block)
	}
	return joined.String()
}

// Fold moves the message into the tool group it sits between.
func (c *AssistantMessageComponent) Fold() {
	c.mu.Lock()
	c.folded = true
	c.mu.Unlock()
}

// SetCompactTop removes the leading blank line when this message is a thought-only
// step directly following another tool or reasoning row.
func (c *AssistantMessageComponent) SetCompactTop(compact bool) {
	c.mu.Lock()
	if c.compactTop != compact {
		c.compactTop = compact
		c.dirty = true
	}
	c.mu.Unlock()
}

// reasoningTitle is the first line of the message's latest reasoning, as
// models title their reasoning summaries ("**Locating official docs**").
func (c *AssistantMessageComponent) reasoningTitle() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.message == nil {
		return ""
	}
	for index := len(c.message.Content) - 1; index >= 0; index-- {
		if thinking, ok := c.message.Content[index].(*ai.ThinkingContent); ok {
			for line := range strings.SplitSeq(thinking.Thinking, "\n") {
				if line = strings.Trim(strings.TrimSpace(line), "*#_ :"); line != "" {
					return line
				}
			}
		}
	}
	return ""
}

func (c *AssistantMessageComponent) reasoningTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.message == nil {
		return 0
	}
	total := 0
	for _, block := range c.message.Content {
		if thinking, ok := block.(*ai.ThinkingContent); ok {
			total += len(strings.TrimSpace(thinking.Thinking))
		}
	}
	if total == 0 {
		return 0
	}
	return max(1, total/4)
}

func (c *AssistantMessageComponent) renderGroupChild(width int) []string {
	title := c.reasoningTitle()
	tokens := c.reasoningTokens()
	if title == "" && tokens == 0 {
		return nil
	}
	verb := theme.FG("muted", padVerb("thought"))
	detail := theme.Italic(theme.FG("muted", title))
	right := ""
	if tokens > 0 {
		right = theme.Faint(fmt.Sprintf("%d tok", tokens))
	}
	return []string{formatRowWithRight(verb+detail, right, width, 0)}
}

func firstReasoningLine(blocks []string) string {
	for index := len(blocks) - 1; index >= 0; index-- {
		for line := range strings.SplitSeq(blocks[index], "\n") {
			if line = strings.Trim(strings.TrimSpace(line), "*#_ :"); line != "" {
				return line
			}
		}
	}
	return ""
}

type thoughtSummaryRow struct {
	title    string
	tokens   int
	expanded bool
}

func (*thoughtSummaryRow) Invalidate() {}

func (row *thoughtSummaryRow) Render(width int) []string {
	if width <= 2 {
		return []string{""}
	}
	marker := theme.Faint("›")
	label := theme.FG("muted", "thought")
	if row.expanded {
		marker = theme.FG("muted", "⌄")
		label = theme.FG("toolTitle", "thought")
	}
	left := " " + marker + " " + label
	if !row.expanded && row.title != "" {
		left += theme.FG("muted", " · "+row.title)
	}
	right := ""
	if row.tokens > 0 {
		right = theme.Faint(fmt.Sprintf("%d tok", row.tokens))
	}
	return []string{formatRowWithRight(left, right, width, 0)}
}

type railedComponent struct {
	inner tui.Component
}

func (r railedComponent) Invalidate() {
	if inv, ok := r.inner.(tui.Invalidatable); ok {
		inv.Invalidate()
	}
}

func (r railedComponent) Render(width int) []string {
	if width <= 4 {
		return r.inner.Render(width)
	}
	prefix := " " + theme.Rail() + "  "
	lines := r.inner.Render(width - 4)
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = prefix + line
	}
	return out
}

func (c *AssistantMessageComponent) Render(width int) []string {
	c.mu.Lock()
	folded := c.folded
	c.mu.Unlock()
	if folded {
		return nil
	}
	return c.renderUnfolded(width)
}

func (c *AssistantMessageComponent) renderUnfolded(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderTheme != theme.Current().Palette() {
		c.mdTheme = theme.MarkdownTheme()
		c.markdown = nil
		c.dirty = true
	}
	if c.dirty && c.message != nil {
		c.updateContentLocked(c.message)
		c.dirty = false
	}
	hasToolCalls := c.hasToolCalls
	lines := make([]string, 0)
	c.toggleStart, c.toggleEnd = -1, -1
	for _, child := range c.contentContainer.Children() {
		rendered := child.Render(width)
		if child == c.toggleHint && c.toggleHint != nil {
			c.toggleStart, c.toggleEnd = len(lines), len(lines)+len(rendered)
		}
		lines = append(lines, rendered...)
	}
	if !hasToolCalls && len(lines) > 0 {
		lines[0] = osc133ZoneStart + lines[0]
		lines[len(lines)-1] = osc133ZoneEnd + osc133ZoneFinal + lines[len(lines)-1]
	}
	return lines
}
