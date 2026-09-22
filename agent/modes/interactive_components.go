package modes

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/extensions"
	sessionstore "github.com/OrdalieTech/orb/agent/session"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

const (
	osc133ZoneStart  = "\x1b]133;A\x07"
	osc133ZoneEnd    = "\x1b]133;B\x07"
	osc133ZoneFinal  = "\x1b]133;C\x07"
	toolPreviewLines = 3
	reasoningLimit   = 4096
	reasoningTail    = 512
)

// ─────────────────────────────────────────────────────────────
// Markdown transformers (upstream components/markdown-transform.ts)
// ─────────────────────────────────────────────────────────────

// newMarkdownTransform curries the message context over the transformer chain
// for a tui.Markdown Transform option (upstream createMarkdownTransform).
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
// panics, mirroring upstream's per-transformer try/catch.
func applyMarkdownTransformer(transformer extensions.MarkdownTransformer, markdown string, context extensions.MarkdownTransformContext) (result string) {
	defer func() {
		if recover() != nil {
			result = markdown
		}
	}()
	return transformer(markdown, context)
}

// ─────────────────────────────────────────────────────────────
// Chat bands
// ─────────────────────────────────────────────────────────────

// chatBandPad is the interior horizontal padding of tool and custom band
// boxes; the user band scales as outputPad+1 instead so the padding setting
// keeps meaning. With the one-cell gutter every band's text starts at column
// 3 by default, aligned with assistant prose (outputPad+2) — opencode's
// indent ladder.
const chatBandPad = 2

// renderBand reserves one gutter cell left of a band box so all chat bands
// share a left edge: the user-message band passes an accent bar, tool and
// custom bands pass "" for a blank cell. Callers resolve the gutter at render
// time so it follows theme changes. The returned slice is never the child's
// cached slice, so callers may prefix zone markers safely.
func renderBand(inner tui.Component, width int, gutter string) []string {
	if width <= 1 {
		return append([]string(nil), inner.Render(width)...)
	}
	rendered := inner.Render(width - 1)
	if gutter == "" {
		gutter = " "
	}
	lines := make([]string, len(rendered))
	for index, line := range rendered {
		lines[index] = gutter + line
	}
	return lines
}

// chatBand is renderBand as a Component, for the bands that are added as a
// child rather than rendered by their owner.
type chatBand struct{ inner tui.Component }

func (band *chatBand) Invalidate() {
	if invalidator, ok := band.inner.(tui.Invalidatable); ok {
		invalidator.Invalidate()
	}
}

func (band *chatBand) Render(width int) []string { return renderBand(band.inner, width, "") }

// startupWarnings renders startup diagnostics as one compact warning band:
// one line per warning truncated to the viewport width (never wrapped),
// extension paths reduced to their basename, and same-name collisions merged
// onto a single line. Full texts stay available where they originate
// (/reload output and stderr in print modes).
type startupWarnings struct{ lines []string }

func newStartupWarnings(diagnostics []StartupDiagnostic) *startupWarnings {
	lines := make([]string, 0, len(diagnostics))
	collisions := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		switch diagnostic.Kind {
		case StartupDiagnosticCollision:
			collisions = append(collisions, diagnostic.Message)
		case StartupDiagnosticExtension:
			message := strings.TrimPrefix(diagnostic.Message, "Failed to load extension: ")
			if cut := strings.Index(message, " imported from "); cut > 0 {
				message = message[:cut]
			}
			lines = append(lines, "extension "+filepath.Base(diagnostic.Path)+": "+message)
		default:
			lines = append(lines, diagnostic.Message)
		}
	}
	if len(collisions) > 0 {
		label := "name collision: "
		if len(collisions) > 1 {
			label = "name collisions: "
		}
		lines = append(lines, label+strings.Join(collisions, ", "))
	}
	return &startupWarnings{lines: lines}
}

func (*startupWarnings) Invalidate() {}

func (warnings *startupWarnings) Render(width int) []string {
	if width <= 3 {
		return append([]string(nil), warnings.lines...)
	}
	// Two cells after the bar, so the text lands on the same column as every
	// other band's body (gutter + chatBandPad).
	bar := theme.FG("warning", string(tui.BandGutterBar)) + "  "
	lines := make([]string, len(warnings.lines))
	for index, line := range warnings.lines {
		lines[index] = bar + theme.FG("warning", tui.TruncateToWidth(line, width-3, "…", false))
	}
	return lines
}

// ─────────────────────────────────────────────────────────────
// UserMessageComponent
// ─────────────────────────────────────────────────────────────

type UserMessageComponent struct {
	box *tui.Box
}

func NewUserMessageComponent(text string, mdTheme tui.MarkdownTheme, outputPad int, transformers []extensions.MarkdownTransformer) *UserMessageComponent {
	box := tui.NewBox(outputPad+1, 0, func(t string) string { return theme.BG("userMessageBg", t) })
	md := tui.NewMarkdown(text, 0, 0, mdTheme, &tui.DefaultTextStyle{
		Color: func(t string) string { return theme.FG("userMessageText", t) },
	}, &tui.MarkdownOptions{
		PreserveOrderedListMarkers: true,
		PreserveBackslashEscapes:   true,
		Transform:                  newMarkdownTransform("user", false, transformers),
	})
	box.AddChild(md)
	return &UserMessageComponent{box: box}
}

func (c *UserMessageComponent) Invalidate() { c.box.Invalidate() }
func (c *UserMessageComponent) Render(width int) []string {
	lines := renderBand(c.box, width, theme.FG("accent", string(tui.BandGutterBar)))
	if len(lines) == 0 {
		return nil
	}
	lines[0] = osc133ZoneStart + lines[0]
	lines[len(lines)-1] = osc133ZoneEnd + osc133ZoneFinal + lines[len(lines)-1]
	// Leading blank only: every block that can follow opens with its own, so
	// closing with one too would double the gap below the message.
	return append([]string{""}, lines...)
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
	hasLongReasoning  bool
	expandedReasoning bool
	toggleHint        *tui.Text
	toggleStart       int
	toggleEnd         int
	onChange          func()
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

// UpdateContentStreaming mirrors upstream updateContent(message, isStreaming):
// the streaming flag feeds the markdown transformer context.
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
	c.hasLongReasoning = false
	c.toggleHint = nil

	hasVisible := false
	hasToolCalls := false
	for _, block := range message.Content {
		switch value := block.(type) {
		case *ai.TextContent:
			hasVisible = hasVisible || strings.TrimSpace(value.Text) != ""
		case *ai.ThinkingContent:
			hasVisible = hasVisible || strings.TrimSpace(value.Thinking) != ""
		case *ai.ToolCall:
			hasToolCalls = true
		}
	}
	c.hasToolCalls = hasToolCalls
	if hasVisible {
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
			long := thinkingBytes > reasoningLimit
			c.hasLongReasoning = c.hasLongReasoning || long
			if c.hideThinking {
				label := c.thinkingLabel
				if label == "" {
					label = "Thinking..."
				}
				c.contentContainer.AddChild(tui.NewText(theme.Italic(theme.FG("thinkingText", label)), c.outputPad+2, 0, nil))
			} else if long && (c.isStreaming || !c.expandedReasoning) {
				tail := thinkingBlocks[len(thinkingBlocks)-1]
				if len(tail) > reasoningTail {
					start := len(tail) - reasoningTail
					for start < len(tail) && !utf8.RuneStart(tail[start]) {
						start++
					}
					tail = tail[start:]
				}
				c.contentContainer.AddChild(visualLineTail{text: theme.Italic(theme.FG("thinkingText", tail)), maxLines: 3, paddingX: c.outputPad + 2})
				label := "… earlier reasoning · click to expand"
				if c.isStreaming {
					label = "… earlier reasoning · available when done"
				}
				c.toggleHint = tui.NewText(theme.FG("muted", label), c.outputPad+2, 0, nil)
				c.contentContainer.AddChild(c.toggleHint)
			} else {
				if long {
					c.toggleHint = tui.NewText(theme.FG("muted", "… reasoning · click to collapse"), c.outputPad+2, 0, nil)
					c.contentContainer.AddChild(c.toggleHint)
				}
				addMarkdown(index, strings.Join(thinkingBlocks, "\n\n"), true)
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
	if c.hideThinking || c.isStreaming || !c.hasLongReasoning || event.Row < c.toggleStart || event.Row >= c.toggleEnd {
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
func (c *AssistantMessageComponent) Render(width int) []string {
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

// ─────────────────────────────────────────────────────────────
// ToolExecutionComponent
// ─────────────────────────────────────────────────────────────

type ToolExecutionComponent struct {
	renderTheme     *theme.Theme
	mu              sync.Mutex
	contentBox      *tui.Box
	toolName        string
	toolCallID      string
	args            any
	expanded        bool
	hovered         bool
	showImages      bool
	isPartial       bool
	result          *toolResult
	toolDef         *extensions.ToolDefinition
	ui              tui.RenderRequester
	cwd             string
	execStarted     bool
	argsComplete    bool
	rendererState   map[string]any
	callComponent   extensions.Component
	resultComponent extensions.Component
}

type toolResult struct {
	Content ai.ToolResultContent
	IsError bool
	Details any
}

func NewToolExecutionComponent(
	toolName, toolCallID string,
	args any,
	showImages bool,
	toolDef *extensions.ToolDefinition,
	ui tui.RenderRequester,
	cwd string,
) *ToolExecutionComponent {
	box := tui.NewBox(chatBandPad, 0, nil)
	box.AddChild(tui.NewText(theme.FG("toolTitle", theme.Bold(toolName)), 0, 0, nil))

	c := &ToolExecutionComponent{
		contentBox:    box,
		toolName:      toolName,
		toolCallID:    toolCallID,
		args:          args,
		showImages:    showImages,
		isPartial:     true,
		toolDef:       toolDef,
		ui:            ui,
		cwd:           cwd,
		rendererState: make(map[string]any),
	}
	c.updateDisplay()
	return c
}

func (c *ToolExecutionComponent) UpdateArgs(args any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.args = args
	c.updateDisplay()
}

func (c *ToolExecutionComponent) MarkExecutionStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execStarted = true
	c.updateDisplay()
}

func (c *ToolExecutionComponent) SetArgsComplete() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.argsComplete = true
	c.updateDisplay()
}

func (c *ToolExecutionComponent) UpdateResult(content ai.ToolResultContent, isError bool, details any, partial bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result = &toolResult{Content: content, IsError: isError, Details: details}
	c.isPartial = partial
	c.updateDisplay()
}

func (c *ToolExecutionComponent) SetExpanded(expanded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expanded = expanded
	c.updateDisplay()
}

func (c *ToolExecutionComponent) HandleMouse(event tui.MouseEvent) bool {
	c.mu.Lock()
	changed := false
	switch event.Type {
	case tui.MouseMove:
		hovered := event.Row >= 0 && (c.result != nil || c.callComponent != nil)
		if hovered != c.hovered {
			c.hovered = hovered
			changed = true
		}
	case tui.MouseRelease:
		if (event.Button == 0 || event.Button == 3) && (c.result != nil || c.callComponent != nil) {
			c.expanded = !c.expanded
			c.updateDisplay()
			changed = true
		}
	}
	c.mu.Unlock()
	if changed && c.ui != nil {
		c.ui.RequestRender()
	}
	return changed
}

func (c *ToolExecutionComponent) updateDisplay() {
	c.renderTheme = theme.Current().Palette()
	c.contentBox.Clear()

	// Tool call header
	if c.toolDef != nil && c.toolDef.RenderCall != nil {
		rendered := c.toolDef.RenderCall(c.args, themeAdapter{}, extensions.ToolRenderContext{
			Args:             c.args,
			ToolCallID:       c.toolCallID,
			Invalidate:       func() { c.ui.RequestRender() },
			LastComponent:    c.callComponent,
			State:            c.rendererState,
			CWD:              c.cwd,
			ExecutionStarted: c.execStarted,
			ArgsComplete:     c.argsComplete,
			IsPartial:        c.isPartial,
			Expanded:         c.expanded,
			ShowImages:       c.showImages,
			IsError:          c.result != nil && c.result.IsError,
		})
		if rendered != nil {
			c.callComponent = rendered
			if _, compact := rendered.(toolCallHeader); !compact && (toolActivityKind(c.toolName) != "" || strings.EqualFold(c.toolName, "bash")) {
				rendered = toolCallHeader{inner: rendered, expanded: c.expanded}
			}
			c.contentBox.AddChild(rendered)
		}
	} else {
		c.contentBox.AddChild(toolCallHeader{inner: tui.NewText(theme.FG("accent", theme.Bold(c.toolName)), 0, 0, nil), expanded: c.expanded})
	}

	// Tool result
	if c.result != nil {
		if c.toolDef != nil && c.toolDef.RenderResult != nil {
			rendered := c.toolDef.RenderResult(
				engine.AgentToolResult{Content: c.result.Content, Details: c.result.Details},
				extensions.ToolRenderResultOptions{Expanded: c.expanded, IsPartial: c.isPartial},
				themeAdapter{},
				extensions.ToolRenderContext{
					Args:          c.args,
					ToolCallID:    c.toolCallID,
					Invalidate:    func() { c.ui.RequestRender() },
					LastComponent: c.resultComponent,
					State:         c.rendererState,
					CWD:           c.cwd,
					Expanded:      c.expanded,
					IsPartial:     c.isPartial,
					IsError:       c.result.IsError,
				},
			)
			if rendered != nil {
				c.resultComponent = rendered
				_, plainOutput := rendered.(*toolOutputPreview)
				if !plainOutput || c.showOutput() {
					c.contentBox.AddChild(toolResultClip{inner: rendered, expanded: c.expanded})
				}
			}
		} else if c.showOutput() {
			output := c.getTextOutput()
			if output != "" {
				c.contentBox.AddChild(toolResultClip{inner: newToolOutputPreview(
					output,
					extensions.ToolRenderResultOptions{Expanded: c.expanded, IsPartial: c.isPartial},
					themeAdapter{},
				), expanded: c.expanded})
			}
		}
	}
}

func (c *ToolExecutionComponent) showOutput() bool {
	return c.expanded || c.result != nil && c.result.IsError || c.isPartial && toolActivityKind(c.toolName) == ""
}

type toolCallHeader struct {
	inner        tui.Component
	expanded     bool
	title        string
	keepTailFrom int
}

func (header toolCallHeader) Invalidate() {
	if component, ok := header.inner.(tui.Invalidatable); ok {
		component.Invalidate()
	}
}

func (header toolCallHeader) Render(width int) []string {
	if !header.expanded && header.title != "" {
		title, _, multiline := strings.Cut(header.title, "\n")
		budget := max(0, width-2)
		clipped := multiline || tui.VisibleWidth(title) > budget
		if header.keepTailFrom > 0 && header.keepTailFrom+1 < budget && tui.VisibleWidth(title) > budget {
			prefix := tui.SliceByColumn(title, 0, header.keepTailFrom, true)
			tailWidth := budget - header.keepTailFrom - 1
			title = prefix + "…" + tui.SliceByColumn(title, tui.VisibleWidth(title)-tailWidth, tailWidth, true)
		}
		suffix := " ›"
		if clipped {
			suffix = " …"
		}
		return []string{tui.TruncateToWidth(title, budget, "…", false) + theme.FG("accent", tui.TruncateToWidth(suffix, width, "", false))}
	}
	lines := header.inner.Render(width)
	if header.expanded || len(lines) == 0 {
		return lines
	}
	suffix := " ›"
	if len(lines) > 1 {
		suffix = " …"
	}
	return []string{tui.TruncateToWidth(strings.TrimRight(lines[0], " "), max(0, width-2), "…", false) + theme.FG("accent", tui.TruncateToWidth(suffix, width, "", false))}
}

type toolOutputPreview struct {
	output  string
	options extensions.ToolRenderResultOptions
	palette extensions.Theme
}

func lastOutputLines(output string, count int) (string, bool) {
	cut := len(output)
	for range count {
		cut = strings.LastIndexByte(output[:cut], '\n')
		if cut < 0 {
			return output, false
		}
	}
	return output[cut+1:], true
}

type toolResultClip struct {
	inner    tui.Component
	expanded bool
}

func (preview toolResultClip) Render(width int) []string {
	padding := min(2, max(0, width-1))
	lines := preview.inner.Render(max(1, width-padding))
	for len(lines) > 0 && !tui.IsImageLine(lines[0]) && strings.TrimSpace(tui.StripANSI(lines[0])) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return nil
	}
	_, alreadyClipped := preview.inner.(*toolOutputPreview)
	count := len(lines)
	if !preview.expanded && !alreadyClipped {
		count = min(count, toolPreviewLines)
	}
	result := make([]string, 1, count+2)
	for _, line := range lines[:count] {
		result = append(result, strings.Repeat(" ", padding)+line)
	}
	if count < len(lines) {
		result = append(result, strings.Repeat(" ", padding)+tui.TruncateToWidth(theme.FG("muted", fmt.Sprintf("… %d more · click to expand", len(lines)-count)), width-padding, "…", false))
	}
	return result
}

func newToolOutputPreview(output string, options extensions.ToolRenderResultOptions, palette extensions.Theme) *toolOutputPreview {
	if palette == nil {
		palette = themeAdapter{}
	}
	return &toolOutputPreview{output: output, options: options, palette: palette}
}

func (preview *toolOutputPreview) Render(width int) []string {
	output := preview.output
	if output == "" {
		return nil
	}
	if preview.options.Expanded {
		lines := strings.Split(output, "\n")
		for index := range lines {
			lines[index] = preview.palette.FG("toolOutput", lines[index])
		}
		return tui.NewText(strings.Join(lines, "\n"), 0, 0, nil).Render(width)
	}
	output, earlier := lastOutputLines(output, toolPreviewLines)
	styled := strings.Split(output, "\n")
	for index := range styled {
		styled[index] = preview.palette.FG("toolOutput", styled[index])
	}
	truncated := tui.TruncateToVisualLines(strings.Join(styled, "\n"), toolPreviewLines, width, 0)
	lines := append([]string(nil), truncated.VisualLines...)
	if earlier || truncated.SkippedCount > 0 {
		hint := preview.palette.FG("muted", "… earlier output · click to expand")
		lines = append(lines, tui.TruncateToWidth(hint, width, "...", false))
	}
	return lines
}

func (c *ToolExecutionComponent) getTextOutput() string {
	if c.result == nil {
		return ""
	}
	var parts []string
	for _, block := range c.result.Content {
		if tb, ok := block.(*ai.TextContent); ok && tb.Text != "" {
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func (c *ToolExecutionComponent) Invalidate() { c.contentBox.Invalidate() }
func (c *ToolExecutionComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderTheme != theme.Current().Palette() {
		c.callComponent, c.resultComponent = nil, nil
		c.updateDisplay()
	}
	marker, color := "✓", "success"
	if c.isPartial {
		marker, color = "○", "accent"
		if c.execStarted || c.argsComplete {
			marker = "●"
		}
	} else if c.result != nil && c.result.IsError {
		marker, color = "×", "error"
	}
	if c.hovered && color != "error" {
		color = "accent"
	}
	lines := renderBand(c.contentBox, width, "")
	if width > 1 && len(lines) > 0 {
		lines[0] = theme.FG(color, marker) + strings.TrimPrefix(lines[0], " ")
	}
	return append([]string{""}, lines...)
}

type toolActivityGroup struct {
	mu       sync.Mutex
	tools    []*ToolExecutionComponent
	expanded bool
	hovered  bool
	hover    *ToolExecutionComponent
	rows     []toolActivityRow
	ui       tui.RenderRequester
}

type toolActivityRow struct {
	tool       *ToolExecutionComponent
	start, end int
}

func toolActivityKind(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "grep", "find", "glob":
		return "search"
	case "ls":
		return "listing"
	}
	return ""
}

func (group *toolActivityGroup) SetExpanded(expanded bool) {
	group.mu.Lock()
	defer group.mu.Unlock()
	group.expanded = expanded
	for _, tool := range group.tools {
		tool.SetExpanded(expanded)
	}
}

func (group *toolActivityGroup) Invalidate() {
	group.mu.Lock()
	defer group.mu.Unlock()
	for _, tool := range group.tools {
		tool.Invalidate()
	}
}

func (group *toolActivityGroup) Render(width int) []string {
	group.mu.Lock()
	defer group.mu.Unlock()
	group.rows = group.rows[:0]
	var lines []string
	if len(group.tools) > 1 {
		counts := map[string]int{}
		active := false
		for _, tool := range group.tools {
			counts[toolActivityKind(tool.toolName)]++
			tool.mu.Lock()
			active = active || tool.isPartial
			tool.mu.Unlock()
		}
		var labels []string
		for _, kind := range []string{"read", "search", "listing"} {
			if count := counts[kind]; count > 0 {
				label := kind
				if count != 1 {
					label += "s"
					if kind == "search" {
						label = "searches"
					}
				}
				labels = append(labels, fmt.Sprintf("%d %s", count, label))
			}
		}
		marker, color := "›", "accent"
		if group.expanded {
			marker = "⌄"
		}
		if group.hovered {
			color = "toolTitle"
		}
		label := "Explored"
		if active {
			label = "Exploring"
		}
		lines = []string{"", tui.TruncateToWidth(theme.FG(color, marker+"  "+theme.Bold(label))+theme.FG("toolTitle", " · "+strings.Join(labels, " · ")), width, "…", false)}
	}
	for _, tool := range group.tools {
		tool.mu.Lock()
		visible := len(group.tools) == 1 || group.expanded || tool.isPartial || tool.result != nil && tool.result.IsError
		tool.mu.Unlock()
		if visible {
			start := len(lines)
			lines = append(lines, tool.Render(width)...)
			group.rows = append(group.rows, toolActivityRow{tool: tool, start: start, end: len(lines)})
		}
	}
	return lines
}

func (group *toolActivityGroup) HandleMouse(event tui.MouseEvent) bool {
	group.mu.Lock()
	var target *ToolExecutionComponent
	local := event
	for _, row := range group.rows {
		if event.Row >= row.start && event.Row < row.end {
			target = row.tool
			local.Row -= row.start
			break
		}
	}
	changed := false
	previous := group.hover
	if event.Type == tui.MouseMove {
		hovered := len(group.tools) > 1 && event.Row == 1
		changed = hovered != group.hovered
		group.hovered, group.hover = hovered, target
	} else if event.Type == tui.MouseRelease && (event.Button == 0 || event.Button == 3) && len(group.tools) > 1 && event.Row == 1 {
		group.expanded = !group.expanded
		changed = true
	}
	group.mu.Unlock()
	if event.Type == tui.MouseMove && previous != nil && previous != target {
		changed = previous.HandleMouse(tui.MouseEvent{Type: tui.MouseMove, Row: -1}) || changed
	}
	if target != nil {
		changed = target.HandleMouse(local) || changed
	}
	if changed && group.ui != nil {
		group.ui.RequestRender()
	}
	return changed
}

// themeAdapter bridges the extension Theme interface to our theme package.
type themeAdapter struct{ value *theme.Theme }

func (adapter themeAdapter) FG(color, text string) string {
	if adapter.value != nil {
		return adapter.value.Foreground(color, text)
	}
	return theme.FG(color, text)
}
func (adapter themeAdapter) BG(color, text string) string {
	if adapter.value != nil {
		return adapter.value.Background(color, text)
	}
	return theme.BG(color, text)
}
func (themeAdapter) Bold(text string) string          { return theme.Bold(text) }
func (themeAdapter) Italic(text string) string        { return theme.Italic(text) }
func (themeAdapter) Underline(text string) string     { return theme.Underline(text) }
func (themeAdapter) Inverse(text string) string       { return theme.Inverse(text) }
func (themeAdapter) Strikethrough(text string) string { return theme.Strikethrough(text) }

func (adapter themeAdapter) FGANSI(color string) string {
	if adapter.value != nil {
		value, _ := adapter.value.ForegroundANSI(color)
		return value
	}
	return theme.FGANSI(color)
}
func (adapter themeAdapter) BGANSI(color string) string {
	if adapter.value != nil {
		value, _ := adapter.value.BackgroundANSI(color)
		return value
	}
	return theme.BGANSI(color)
}
func (adapter themeAdapter) ColorMode() string {
	if adapter.value != nil {
		return string(adapter.value.ColorMode())
	}
	return theme.ColorModeGlobal()
}
func (adapter themeAdapter) ThinkingBorderColor(level engine.ThinkingLevel) func(string) string {
	if adapter.value != nil {
		key := "thinkingMedium"
		switch level {
		case "off":
			key = "thinkingOff"
		case "minimal":
			key = "thinkingMinimal"
		case "low":
			key = "thinkingLow"
		case "high":
			key = "thinkingHigh"
		case "xhigh":
			key = "thinkingXhigh"
		case "max":
			key = "thinkingMax"
		}
		return func(value string) string { return adapter.value.Foreground(key, value) }
	}
	return theme.ThinkingBorderColor(level)
}
func (adapter themeAdapter) BashModeBorderColor() func(string) string {
	if adapter.value != nil {
		return func(value string) string { return adapter.value.Foreground("bashMode", value) }
	}
	return theme.BashModeBorderColor()
}

// ─────────────────────────────────────────────────────────────
// BashExecutionComponent
// ─────────────────────────────────────────────────────────────

const bashPreviewLines = 3

type visualLineTail struct {
	text     string
	maxLines int
	paddingX int
	earlier  bool
	hint     string
}

func (preview visualLineTail) Render(width int) []string {
	truncated := tui.TruncateToVisualLines(preview.text, preview.maxLines, width, preview.paddingX)
	if preview.hint == "" || truncated.SkippedCount == 0 && !preview.earlier {
		return truncated.VisualLines
	}
	return append(truncated.VisualLines, tui.TruncateToWidth(strings.Repeat(" ", preview.paddingX)+theme.FG("muted", preview.hint), width, "…", false))
}

type BashExecutionComponent struct {
	renderTheme *theme.Theme
	mu          sync.Mutex
	container   *tui.Container
	command     string
	output      strings.Builder
	exitCode    *int
	cancelled   bool
	complete    bool
	expanded    bool
	hovered     bool
	excludeCtx  bool
	loader      *tui.Loader
	ui          tui.RenderRequester
}

func NewBashExecutionComponent(command string, ui tui.RenderRequester, excludeFromContext bool) *BashExecutionComponent {
	c := &BashExecutionComponent{
		container:  &tui.Container{},
		command:    command,
		excludeCtx: excludeFromContext,
		ui:         ui,
	}
	c.loader = tui.NewLoader(ui,
		func(s string) string { return theme.FG("bashMode", s) },
		func(s string) string { return theme.FG("muted", s) },
		"Running... ("+KeyText("tui.select.cancel")+" to cancel)", nil,
	)
	c.rebuild()
	return c
}

func (c *BashExecutionComponent) AppendOutput(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output.WriteString(text)
	c.rebuild()
}

func (c *BashExecutionComponent) SetComplete(exitCode *int, cancelled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exitCode = exitCode
	c.cancelled = cancelled
	c.complete = true
	if c.loader != nil {
		c.loader.Stop()
		c.loader = nil
	}
	c.rebuild()
}

func (c *BashExecutionComponent) SetExpanded(expanded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expanded = expanded
	c.rebuild()
}

func (c *BashExecutionComponent) HandleMouse(event tui.MouseEvent) bool {
	c.mu.Lock()
	changed := false
	switch event.Type {
	case tui.MouseMove:
		hovered := event.Row >= 0 && c.output.Len() > 0
		if hovered != c.hovered {
			c.hovered = hovered
			changed = true
		}
	case tui.MouseRelease:
		if (event.Button == 0 || event.Button == 3) && c.output.Len() > 0 {
			c.expanded = !c.expanded
			c.rebuild()
			changed = true
		}
	}
	c.mu.Unlock()
	if changed && c.ui != nil {
		c.ui.RequestRender()
	}
	return changed
}

func (c *BashExecutionComponent) rebuild() {
	c.renderTheme = theme.Current().Palette()
	c.container.Clear()
	colorKey := "bashMode"
	if c.excludeCtx {
		colorKey = "dim"
	}
	c.container.AddChild(tui.NewSpacer(1))

	// Command header
	prefix := "$ "
	if c.excludeCtx {
		prefix = "!! "
	}
	c.container.AddChild(tui.NewText(theme.FG(colorKey, theme.Bold(prefix+c.command)), 3, 0, nil))

	// Output
	output := strings.TrimSuffix(c.output.String(), "\n")
	if output != "" {
		c.container.AddChild(tui.NewSpacer(1))
		if c.expanded {
			c.container.AddChild(tui.NewText(theme.FG("muted", output), 5, 0, nil))
		} else {
			preview, earlier := lastOutputLines(output, bashPreviewLines)
			c.container.AddChild(visualLineTail{
				text:     theme.FG("muted", preview),
				maxLines: bashPreviewLines,
				paddingX: 5,
				earlier:  earlier,
				hint:     "… earlier output · click to expand",
			})
		}
	}

	// Status
	if !c.complete && c.loader != nil {
		c.container.AddChild(c.loader)
	} else if c.complete {
		statusParts := make([]string, 0, 2)
		if c.cancelled {
			statusParts = append(statusParts, theme.FG("warning", "(cancelled)"))
		} else if c.exitCode != nil && *c.exitCode != 0 {
			statusParts = append(statusParts, theme.FG("error", fmt.Sprintf("(exit %d)", *c.exitCode)))
		}
		if len(statusParts) > 0 {
			c.container.AddChild(tui.NewText(strings.Join(statusParts, "\n"), 3, 0, nil))
		}
	}

}

func (c *BashExecutionComponent) Invalidate() { c.container.Invalidate() }
func (c *BashExecutionComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.renderTheme != theme.Current().Palette() {
		c.rebuild()
	}
	lines := c.container.Render(width)
	if c.hovered && len(lines) > 1 {
		lines = append([]string(nil), lines...)
		lines[1] = theme.Underline(lines[1])
	}
	return lines
}

// ─────────────────────────────────────────────────────────────
// StatusIndicators
// ─────────────────────────────────────────────────────────────

type StatusIndicatorKind string

const (
	StatusWorking       StatusIndicatorKind = "working"
	StatusRetry         StatusIndicatorKind = "retry"
	StatusCompaction    StatusIndicatorKind = "compaction"
	StatusBranchSummary StatusIndicatorKind = "branchSummary"
)

type StatusIndicator struct {
	*tui.Loader
	Kind      StatusIndicatorKind
	countdown *CountdownTimer
}

func (si *StatusIndicator) Dispose() {
	if si.countdown != nil {
		si.countdown.Dispose()
	}
	si.Stop()
}

func NewWorkingStatusIndicator(ui tui.RenderRequester, message string, options ...*extensions.WorkingIndicatorOptions) *StatusIndicator {
	var indicator *tui.LoaderIndicatorOptions
	if len(options) > 0 && options[0] != nil {
		frames := []string(nil)
		if options[0].Frames != nil {
			frames = append([]string{}, options[0].Frames...)
		}
		interval := time.Duration(0)
		if options[0].IntervalMS > 0 {
			interval = time.Duration(options[0].IntervalMS) * time.Millisecond
		}
		indicator = &tui.LoaderIndicatorOptions{Frames: frames, Interval: interval}
	}
	return &StatusIndicator{
		Loader: tui.NewLoader(ui,
			func(s string) string { return theme.FG("accent", s) },
			func(s string) string { return theme.FG("muted", s) },
			message, indicator,
		),
		Kind: StatusWorking,
	}
}

func NewRetryStatusIndicator(ui tui.RenderRequester, attempt, maxAttempts int, delayMS int64) *StatusIndicator {
	retryMessage := func(seconds int) string {
		return fmt.Sprintf("Retrying (%d/%d) in %ds... (%s to cancel)", attempt, maxAttempts, seconds, KeyText("app.interrupt"))
	}
	status := &StatusIndicator{
		Loader: tui.NewLoader(ui,
			func(s string) string { return theme.FG("warning", s) },
			func(s string) string { return theme.FG("muted", s) },
			retryMessage(int((delayMS+999)/1000)), nil,
		),
		Kind: StatusRetry,
	}
	status.countdown = NewCountdownTimer(delayMS, ui, func(seconds int) {
		status.SetMessage(retryMessage(seconds))
	}, nil)
	return status
}

func NewCompactionStatusIndicator(ui tui.RenderRequester, reason string) *StatusIndicator {
	cancelHint := fmt.Sprintf("(%s to cancel)", KeyText("app.interrupt"))
	var label string
	switch reason {
	case "manual":
		label = "Compacting context... " + cancelHint
	case "overflow":
		label = "Context overflow detected, Auto-compacting... " + cancelHint
	default:
		label = "Auto-compacting... " + cancelHint
	}
	return &StatusIndicator{
		Loader: tui.NewLoader(ui,
			func(s string) string { return theme.FG("accent", s) },
			func(s string) string { return theme.FG("muted", s) },
			label, nil,
		),
		Kind: StatusCompaction,
	}
}

func NewBranchSummaryStatusIndicator(ui tui.RenderRequester) *StatusIndicator {
	return &StatusIndicator{
		Loader: tui.NewLoader(ui,
			func(s string) string { return theme.FG("accent", s) },
			func(s string) string { return theme.FG("muted", s) },
			fmt.Sprintf("Summarizing branch... (%s to cancel)", KeyText("app.interrupt")), nil,
		),
		Kind: StatusBranchSummary,
	}
}

// IdleStatus renders two empty lines (same height as a status indicator).
type IdleStatus struct{}

func (IdleStatus) Invalidate() {}
func (IdleStatus) Render(width int) []string {
	empty := strings.Repeat(" ", width)
	return []string{empty, empty}
}

// ─────────────────────────────────────────────────────────────
// FooterComponent
// ─────────────────────────────────────────────────────────────

type statusHit struct {
	row, start, end int
	action          func()
}

type FooterComponent struct {
	hitMu              sync.Mutex
	hits               []statusHit
	session            footerSession
	provider           footerDataProvider
	verbose            bool
	autoCompactEnabled bool
	branchMu           sync.Mutex
	branch             string
	providerCount      int
	branchAt           time.Time
	branchRefreshing   bool
}

type footerSession interface {
	State() engine.AgentState
}

type footerDataProvider interface {
	GitBranch() string
	Statuses() map[string]string
}

func NewFooterComponent(session footerSession, provider footerDataProvider, verbose bool) *FooterComponent {
	return &FooterComponent{session: session, provider: provider, verbose: verbose, autoCompactEnabled: true}
}

func (f *FooterComponent) Invalidate() {}

func (f *FooterComponent) metadata() (string, int) {
	f.branchMu.Lock()
	refresh := time.Since(f.branchAt) >= 500*time.Millisecond && !f.branchRefreshing
	if refresh {
		f.branchRefreshing = true
	}
	branch, providerCount := f.branch, f.providerCount
	f.branchMu.Unlock()
	if !refresh {
		return branch, providerCount
	}
	load := func() (string, int) {
		count := 0
		if provider, ok := f.provider.(interface{ AvailableProviderCount() int }); ok {
			count = provider.AvailableProviderCount()
		}
		return f.provider.GitBranch(), count
	}
	store := func(branch string, providerCount int) {
		f.branchMu.Lock()
		f.branch, f.providerCount = branch, providerCount
		f.branchAt, f.branchRefreshing = time.Now(), false
		f.branchMu.Unlock()
	}
	if invalidator, ok := f.provider.(tui.Invalidatable); ok {
		go func() {
			store(load())
			invalidator.Invalidate()
		}()
		return branch, providerCount
	}
	branch, providerCount = load()
	store(branch, providerCount)
	return branch, providerCount
}

func (f *FooterComponent) cwd() string {
	if provider, ok := f.provider.(interface{ CurrentCWD() string }); ok {
		return provider.CurrentCWD()
	}
	return ""
}

func footerContextSummary(display engine.AgentDisplayState, usage *harness.ContextUsage) string {
	contextWindow := int64(display.ContextWindow)
	percent := "?"
	if usage != nil {
		if usage.ContextWindow > 0 {
			contextWindow = int64(usage.ContextWindow)
		}
		if usage.Percent != nil {
			percent = fmt.Sprintf("%.1f", *usage.Percent)
		}
	}
	if percent == "?" {
		return "?/" + formatTokens(contextWindow)
	}
	return percent + "%/" + formatTokens(contextWindow)
}

// modelFooterForms keeps the model readable while the footer narrows.
func modelFooterForms(display engine.AgentDisplayState, providerCount int) []string {
	if !display.HasModel {
		return []string{"no-model"}
	}
	thinking := ""
	if display.Reasoning {
		level := string(display.ThinkingLevel)
		if level == "" {
			level = "off"
		}
		thinking = " " + thinkingMeter(level)
	}
	forms := make([]string, 0, 3)
	if providerCount > 1 {
		forms = append(forms, "("+string(display.Provider)+") "+display.ModelID+thinking)
	}
	if thinking != "" {
		forms = append(forms, display.ModelID+thinking)
	}
	return append(forms, display.ModelID)
}

func thinkingMeter(level string) string {
	switch level {
	case "minimal":
		return "○"
	case "low":
		return "◔"
	case "medium":
		return "◑"
	case "high":
		return "◕"
	case "xhigh":
		return "●"
	case "max":
		return "◉"
	default:
		return "·"
	}
}

func compactFooterLine(display engine.AgentDisplayState, context *harness.ContextUsage, statuses []string, width int) string {
	model := display.ModelID
	if !display.HasModel {
		model = "Choose model"
	}
	left := model
	if display.Reasoning {
		left += " " + thinkingMeter(string(display.ThinkingLevel))
	}
	right := strings.Join(statuses, " · ")
	leftBudget := width
	if right != "" {
		leftBudget = max(min(tui.VisibleWidth(model), width*2/3), width-tui.VisibleWidth(right)-2)
	}
	if tui.VisibleWidth(left) > leftBudget {
		left = model
	}
	left = tui.TruncateToWidth(left, max(0, leftBudget), "…", false)
	rightBudget := max(0, width-tui.VisibleWidth(left)-2)
	if context != nil && context.Percent != nil {
		used := "?"
		if context.Tokens != nil {
			used = formatTokens(*context.Tokens)
		}
		summary := fmt.Sprintf("%s|%.0f%%", used, *context.Percent)
		candidate := summary
		if right != "" {
			candidate = right + " · " + summary
		}
		if tui.VisibleWidth(candidate) <= rightBudget {
			right = candidate
		}
	}
	right = tui.TruncateToWidth(right, rightBudget, "…", false)
	return left + strings.Repeat(" ", max(0, width-tui.VisibleWidth(left)-tui.VisibleWidth(right))) + right
}

func (f *FooterComponent) render(width int) []string {
	f.hitMu.Lock()
	f.hits = nil
	f.hitMu.Unlock()
	providerCount := 0
	pwd := ""
	if f.verbose {
		branch, count := f.metadata()
		providerCount = count
		pwd = f.cwd()
		if branch != "" {
			pwd += " (" + branch + ")"
		}
		if provider, ok := f.provider.(interface{ SessionName() string }); ok {
			if name := provider.SessionName(); name != "" {
				shownInEditor := false
				if placement, supported := f.provider.(interface{ SessionNameInEditor(int) bool }); supported {
					shownInEditor = placement.SessionNameInEditor(width)
				}
				if !shownInEditor {
					pwd += " • " + name
				}
			}
		}
	}

	display, stats, latestCacheHitRate, autoCompactEnabled := f.collect()
	statuses := f.provider.Statuses()
	keys := make([]string, 0, len(statuses))
	for key := range statuses {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, strings.Join(strings.Fields(statuses[key]), " "))
	}
	if !f.verbose {
		if cwd := f.cwd(); cwd != "" {
			path := shortenSessionPath(cwd)
			model := display.ModelID
			if !display.HasModel {
				model = "Choose model"
			}
			if display.Reasoning {
				model += " " + thinkingMeter(string(display.ThinkingLevel))
			}
			available := width - tui.VisibleWidth(model) - tui.VisibleWidth(strings.Join(values, " · ")) - 2
			if len(values) > 0 {
				available -= 3
			}
			if tui.VisibleWidth(path) > available {
				path = "…/" + filepath.Base(cwd)
			}
			values = append(values, path)
		}
		line := compactFooterLine(display, stats.ContextUsage, values, width)
		f.recordStatusHits(line, 0, keys, values)
		f.recordThinkingHit(line, 0, display)
		return []string{theme.FG("dim", line)}
	}

	contextSummary := footerContextSummary(display, stats.ContextUsage)
	modelForms := modelFooterForms(display, providerCount)
	modelName := modelForms[0]
	statsParts := make([]string, 0, 7)
	if stats.Tokens.Input > 0 {
		statsParts = append(statsParts, "↑"+formatTokens(stats.Tokens.Input))
	}
	if stats.Tokens.Output > 0 {
		statsParts = append(statsParts, "↓"+formatTokens(stats.Tokens.Output))
	}
	if stats.Tokens.CacheRead > 0 {
		statsParts = append(statsParts, "R"+formatTokens(stats.Tokens.CacheRead))
	}
	if stats.Tokens.CacheWrite > 0 {
		statsParts = append(statsParts, "W"+formatTokens(stats.Tokens.CacheWrite))
	}
	if (stats.Tokens.CacheRead > 0 || stats.Tokens.CacheWrite > 0) && latestCacheHitRate != nil {
		statsParts = append(statsParts, fmt.Sprintf("CH%.1f%%", *latestCacheHitRate))
	}
	if stats.Cost > 0 {
		statsParts = append(statsParts, fmt.Sprintf("$%.3f", stats.Cost))
	}
	auto := ""
	if autoCompactEnabled {
		auto = " (auto)"
	}
	statsParts = append(statsParts, contextSummary+auto)
	statsLeft := strings.Join(statsParts, " ")
	if tui.VisibleWidth(statsLeft) > width {
		statsLeft = tui.TruncateToWidth(statsLeft, width, "...", false)
	}
	availableRight := width - tui.VisibleWidth(statsLeft) - 2
	if availableRight < tui.VisibleWidth(modelName) && display.HasModel && strings.HasPrefix(modelName, "(") {
		modelName = display.ModelID
	}
	if availableRight < tui.VisibleWidth(modelName) {
		modelName = tui.TruncateToWidth(modelName, max(0, availableRight), "", false)
	}
	padding := strings.Repeat(" ", max(0, width-tui.VisibleWidth(statsLeft)-tui.VisibleWidth(modelName)))
	lines := []string{
		tui.TruncateToWidth(theme.FG("dim", pwd), width, theme.FG("dim", "..."), false),
		theme.FG("dim", statsLeft) + theme.FG("dim", padding+modelName),
	}

	f.recordThinkingHit(lines[1], 1, display)
	if len(keys) > 0 {
		statusLine := tui.TruncateToWidth(strings.Join(values, " "), width, "…", false)
		f.recordStatusHits(statusLine, len(lines), keys, values)
		lines = append(lines, statusLine)
	}
	return lines
}

func (f *FooterComponent) recordStatusHits(text string, row int, keys, values []string) {
	provider, ok := f.provider.(interface{ StatusAction(string) func() })
	if !ok {
		return
	}
	text = tui.StripANSI(text)
	var hits []statusHit
	for i, key := range keys {
		action := provider.StatusAction(key)
		fields := strings.Fields(values[i])
		if action == nil || len(fields) == 0 {
			continue
		}
		value := tui.StripANSI(values[i])
		start := strings.Index(text, value)
		if start < 0 && strings.HasSuffix(text, "…") {
			start = strings.LastIndex(text, fields[0])
			if start >= 0 && !strings.HasPrefix(value, strings.TrimSuffix(text[start:], "…")) {
				start = -1
			}
		}
		if start < 0 {
			continue
		}
		column := tui.VisibleWidth(text[:start]) + 1
		hits = append(hits, statusHit{row: row, start: column, end: min(tui.VisibleWidth(text)+1, column+tui.VisibleWidth(value)), action: action})
	}
	f.hitMu.Lock()
	f.hits = append(f.hits, hits...)
	f.hitMu.Unlock()
}

func (f *FooterComponent) recordThinkingHit(text string, row int, display engine.AgentDisplayState) {
	provider, ok := f.provider.(interface{ StatusAction(string) func() })
	if !display.Reasoning || !ok {
		return
	}
	text = tui.StripANSI(text)
	label := display.ModelID + " " + thinkingMeter(string(display.ThinkingLevel))
	start := strings.Index(text, label)
	action := provider.StatusAction("orb:thinking")
	if start < 0 || action == nil {
		return
	}
	column := tui.VisibleWidth(text[:start]) + tui.VisibleWidth(label)
	f.hitMu.Lock()
	f.hits = append(f.hits, statusHit{row: row, start: column, end: column + 1, action: action})
	f.hitMu.Unlock()
}

func (f *FooterComponent) HandleMouse(event tui.MouseEvent) bool {
	if event.Type != tui.MousePress || event.Button != 0 {
		return false
	}
	f.hitMu.Lock()
	var action func()
	for _, hit := range f.hits {
		if event.Row == hit.row && event.Column >= hit.start && event.Column < hit.end {
			action = hit.action
			break
		}
	}
	f.hitMu.Unlock()
	if action == nil {
		return false
	}
	if event.Clicks < 2 {
		action()
	}
	return true
}

func (f *FooterComponent) Render(width int) []string {
	const padding = 1
	if width <= padding*2 {
		return f.render(width)
	}
	lines := f.render(width - padding*2)
	for index, line := range lines {
		lines[index] = " " + line + strings.Repeat(" ", max(1, width-1-tui.VisibleWidth(line)))
	}
	return lines
}

// collect uses SessionRuntime's revision-gated snapshot, while narrower test
// and host seams provide only the values needed by compact or verbose mode.
func (f *FooterComponent) collect() (engine.AgentDisplayState, agent.SessionStats, *float64, bool) {
	display := engine.AgentDisplayState{}
	stats := agent.SessionStats{}
	var latestCacheHitRate *float64
	autoCompactEnabled := f.autoCompactEnabled
	if session, ok := f.session.(interface {
		FooterSnapshot() agent.FooterSnapshot
	}); ok {
		snapshot := session.FooterSnapshot()
		display = snapshot.Display
		stats.ContextUsage = snapshot.ContextUsage
		stats.Cost = snapshot.Cost
		if f.verbose {
			stats.Tokens = snapshot.Tokens
			autoCompactEnabled = snapshot.AutoCompactEnabled
			if snapshot.HasLatestCacheHitRate {
				rate := snapshot.LatestCacheHitRate
				latestCacheHitRate = &rate
			}
		}
	} else {
		state := f.session.State()
		display.ThinkingLevel = state.ThinkingLevel
		if state.Model != nil {
			display.HasModel = true
			display.ModelID = state.Model.ID
			display.Provider = state.Model.Provider
			display.ContextWindow = state.Model.ContextWindow
			display.Reasoning = state.Model.Reasoning
		}
		if f.verbose {
			if session, ok := f.session.(interface {
				GetSessionStats() agent.SessionStats
			}); ok {
				stats = session.GetSessionStats()
			}
			if session, ok := f.session.(interface {
				Manager() *sessionstore.SessionManager
			}); ok && session.Manager() != nil {
				aggregate, _ := session.Manager().AggregateStats()
				stats.Tokens = agent.SessionTokenTotals{
					Input: aggregate.InputTokens, Output: aggregate.OutputTokens,
					CacheRead: aggregate.CacheReadTokens, CacheWrite: aggregate.CacheWriteTokens,
				}
				stats.Tokens.Total = stats.Tokens.Input + stats.Tokens.Output + stats.Tokens.CacheRead + stats.Tokens.CacheWrite
				stats.Cost = aggregate.Cost
				if aggregate.HasLatestCacheHitRate {
					rate := aggregate.LatestCacheHitRate
					latestCacheHitRate = &rate
				}
			}
			if session, ok := f.session.(interface{ AutoCompactionEnabled() bool }); ok {
				autoCompactEnabled = session.AutoCompactionEnabled()
			}
		} else {
			if session, ok := f.session.(interface {
				GetContextUsage() *harness.ContextUsage
			}); ok {
				stats.ContextUsage = session.GetContextUsage()
			} else if session, ok := f.session.(interface {
				GetSessionStats() agent.SessionStats
			}); ok {
				stats.ContextUsage = session.GetSessionStats().ContextUsage
			}
		}
	}
	return display, stats, latestCacheHitRate, autoCompactEnabled
}

// ─────────────────────────────────────────────────────────────
// CompactionSummaryMessageComponent
// ─────────────────────────────────────────────────────────────

type CompactionSummaryMessageComponent struct {
	box      *tui.Box
	expanded bool
	summary  string
	tokens   int64
	mdTheme  tui.MarkdownTheme
}

func NewCompactionSummaryMessage(summary string, tokensBefore int64, mdTheme tui.MarkdownTheme) *CompactionSummaryMessageComponent {
	c := &CompactionSummaryMessageComponent{
		box:     tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
		summary: summary,
		tokens:  tokensBefore,
		mdTheme: mdTheme,
	}
	c.updateDisplay()
	return c
}

func (c *CompactionSummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

func (c *CompactionSummaryMessageComponent) updateDisplay() {
	c.box.Clear()
	label := theme.FG("customMessageLabel", theme.Bold("[compaction]"))
	c.box.AddChild(tui.NewText(label, 0, 0, nil))
	c.box.AddChild(tui.NewSpacer(1))

	tokenStr := formatInteger(c.tokens)
	if c.expanded {
		header := fmt.Sprintf("**Compacted from %s tokens**\n\n", tokenStr)
		c.box.AddChild(tui.NewMarkdown(header+c.summary, 0, 0, c.mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
	} else {
		c.box.AddChild(tui.NewText(
			theme.FG("customMessageText", fmt.Sprintf("Compacted from %s tokens (", tokenStr))+
				theme.FG("dim", KeyText("app.tools.expand"))+
				theme.FG("customMessageText", " to expand)"),
			0, 0, nil,
		))
	}
}

func (c *CompactionSummaryMessageComponent) Invalidate() { c.box.Invalidate() }
func (c *CompactionSummaryMessageComponent) Render(width int) []string {
	return renderBand(c.box, width, "")
}

// ─────────────────────────────────────────────────────────────
// BranchSummaryMessageComponent
// ─────────────────────────────────────────────────────────────

type BranchSummaryMessageComponent struct {
	box      *tui.Box
	expanded bool
	summary  string
	mdTheme  tui.MarkdownTheme
}

func NewBranchSummaryMessage(summary string, mdTheme tui.MarkdownTheme) *BranchSummaryMessageComponent {
	c := &BranchSummaryMessageComponent{
		box:     tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
		summary: summary,
		mdTheme: mdTheme,
	}
	c.updateDisplay()
	return c
}

func (c *BranchSummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

func (c *BranchSummaryMessageComponent) updateDisplay() {
	c.box.Clear()
	label := theme.FG("customMessageLabel", theme.Bold("[branch]"))
	c.box.AddChild(tui.NewText(label, 0, 0, nil))
	c.box.AddChild(tui.NewSpacer(1))

	if c.expanded {
		header := "**Branch Summary**\n\n"
		c.box.AddChild(tui.NewMarkdown(header+c.summary, 0, 0, c.mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
	} else {
		c.box.AddChild(tui.NewText(
			theme.FG("customMessageText", "Branch summary (")+
				theme.FG("dim", KeyText("app.tools.expand"))+
				theme.FG("customMessageText", " to expand)"),
			0, 0, nil,
		))
	}
}

func (c *BranchSummaryMessageComponent) Invalidate() { c.box.Invalidate() }
func (c *BranchSummaryMessageComponent) Render(width int) []string {
	return renderBand(c.box, width, "")
}

// ─────────────────────────────────────────────────────────────
// SkillInvocationMessageComponent
// ─────────────────────────────────────────────────────────────

type SkillInvocationMessageComponent struct {
	box      *tui.Box
	expanded bool
	name     string
	content  string
	mdTheme  tui.MarkdownTheme
}

func NewSkillInvocationMessage(name, content string, mdTheme tui.MarkdownTheme) *SkillInvocationMessageComponent {
	c := &SkillInvocationMessageComponent{
		box:     tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
		name:    name,
		content: content,
		mdTheme: mdTheme,
	}
	c.updateDisplay()
	return c
}

func (c *SkillInvocationMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

func (c *SkillInvocationMessageComponent) updateDisplay() {
	c.box.Clear()
	if c.expanded {
		label := theme.FG("customMessageLabel", theme.Bold("[skill]"))
		c.box.AddChild(tui.NewText(label, 0, 0, nil))
		header := fmt.Sprintf("**%s**\n\n", c.name)
		c.box.AddChild(tui.NewMarkdown(header+c.content, 0, 0, c.mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
	} else {
		line := theme.FG("customMessageLabel", theme.Bold("[skill]")+" ") +
			theme.FG("customMessageText", c.name) +
			theme.FG("dim", fmt.Sprintf(" (%s to expand)", KeyText("app.tools.expand")))
		c.box.AddChild(tui.NewText(line, 0, 0, nil))
	}
}

func (c *SkillInvocationMessageComponent) Invalidate() {
	c.box.Invalidate()
	c.updateDisplay()
}
func (c *SkillInvocationMessageComponent) Render(width int) []string {
	return renderBand(c.box, width, "")
}

// ─────────────────────────────────────────────────────────────
// CustomMessageComponent
// ─────────────────────────────────────────────────────────────

type CustomMessageComponent struct {
	container *tui.Container
	box       *tui.Box
	expanded  bool
}

func NewCustomMessageComponent(customType string, content any, mdTheme tui.MarkdownTheme) *CustomMessageComponent {
	container := &tui.Container{}
	container.AddChild(tui.NewSpacer(1))
	box := tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) })
	container.AddChild(&chatBand{inner: box})
	label := theme.FG("customMessageLabel", theme.Bold(fmt.Sprintf("[%s]", customType)))
	box.AddChild(tui.NewText(label, 0, 0, nil))
	box.AddChild(tui.NewSpacer(1))
	text := fmt.Sprintf("%v", content)
	if text != "" {
		box.AddChild(tui.NewMarkdown(text, 0, 0, mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
	}
	return &CustomMessageComponent{container: container, box: box}
}

func (c *CustomMessageComponent) SetExpanded(expanded bool) { c.expanded = expanded }
func (c *CustomMessageComponent) Invalidate()               { c.container.Invalidate() }
func (c *CustomMessageComponent) Render(width int) []string { return c.container.Render(width) }
