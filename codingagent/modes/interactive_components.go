package modes

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/tui"

	theme "github.com/OrdalieTech/pi-go/codingagent/modes/theme"
)

const (
	osc133ZoneStart = "\x1b]133;A\x07"
	osc133ZoneEnd   = "\x1b]133;B\x07"
	osc133ZoneFinal = "\x1b]133;C\x07"
)

// ─────────────────────────────────────────────────────────────
// UserMessageComponent
// ─────────────────────────────────────────────────────────────

type UserMessageComponent struct {
	box *tui.Box
}

func NewUserMessageComponent(text string, mdTheme tui.MarkdownTheme, outputPad int) *UserMessageComponent {
	box := tui.NewBox(outputPad, 1, func(t string) string { return theme.BG("userMessageBg", t) })
	md := tui.NewMarkdown(text, 0, 0, mdTheme, &tui.DefaultTextStyle{
		Color: func(t string) string { return theme.FG("userMessageText", t) },
	}, &tui.MarkdownOptions{
		PreserveOrderedListMarkers: true,
		PreserveBackslashEscapes:   true,
	})
	box.AddChild(md)
	return &UserMessageComponent{box: box}
}

func (c *UserMessageComponent) Invalidate() { c.box.Invalidate() }
func (c *UserMessageComponent) Render(width int) []string {
	lines := c.box.Render(width)
	if len(lines) > 0 {
		lines[0] = osc133ZoneStart + lines[0]
		lines[len(lines)-1] = osc133ZoneEnd + osc133ZoneFinal + lines[len(lines)-1]
	}
	return lines
}

// ─────────────────────────────────────────────────────────────
// AssistantMessageComponent
// ─────────────────────────────────────────────────────────────

type AssistantMessageComponent struct {
	mu               sync.Mutex
	container        *tui.Container
	contentContainer *tui.Container
	hideThinking     bool
	mdTheme          tui.MarkdownTheme
	thinkingLabel    string
	outputPad        int
	message          *ai.AssistantMessage
	hasToolCalls     bool
}

func NewAssistantMessageComponent(
	message *ai.AssistantMessage,
	hideThinking bool,
	mdTheme tui.MarkdownTheme,
	thinkingLabel string,
	outputPad int,
) *AssistantMessageComponent {
	c := &AssistantMessageComponent{
		container:        &tui.Container{},
		contentContainer: &tui.Container{},
		hideThinking:     hideThinking,
		mdTheme:          mdTheme,
		thinkingLabel:    thinkingLabel,
		outputPad:        outputPad,
	}
	c.container.AddChild(c.contentContainer)
	if message != nil {
		c.UpdateContent(message)
	}
	return c
}

func (c *AssistantMessageComponent) UpdateContent(message *ai.AssistantMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := *message
	c.message = &copy
	c.updateContentLocked(message)
}

func (c *AssistantMessageComponent) SetHideThinkingBlock(hidden bool, label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hideThinking, c.thinkingLabel = hidden, label
	if c.message != nil {
		c.updateContentLocked(c.message)
	}
}

func (c *AssistantMessageComponent) SetHiddenThinkingLabel(label string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.thinkingLabel = label
	if c.message != nil {
		c.updateContentLocked(c.message)
	}
}

func (c *AssistantMessageComponent) updateContentLocked(message *ai.AssistantMessage) {
	c.contentContainer.Clear()

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
				c.contentContainer.AddChild(tui.NewMarkdown(text, c.outputPad, 0, c.mdTheme, nil, nil))
			}
		case *ai.ThinkingContent:
			thinkingBlocks := make([]string, 0, 1)
			for ; index < len(message.Content); index++ {
				thinking, ok := message.Content[index].(*ai.ThinkingContent)
				if !ok {
					break
				}
				if text := strings.TrimSpace(thinking.Thinking); text != "" {
					thinkingBlocks = append(thinkingBlocks, text)
				}
			}
			index--
			if len(thinkingBlocks) == 0 {
				continue
			}
			if c.hideThinking {
				label := c.thinkingLabel
				if label == "" {
					label = "Thinking..."
				}
				c.contentContainer.AddChild(tui.NewText(theme.Italic(theme.FG("thinkingText", label)), c.outputPad, 0, nil))
			} else {
				c.contentContainer.AddChild(tui.NewMarkdown(strings.Join(thinkingBlocks, "\n\n"), c.outputPad, 0, c.mdTheme, &tui.DefaultTextStyle{
					Color:  func(text string) string { return theme.FG("thinkingText", text) },
					Italic: true,
				}, nil))
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
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", "Error: Model stopped because it reached the maximum output token limit. The response may be incomplete."), c.outputPad, 0, nil))
	} else if !hasToolCalls && message.StopReason == ai.StopReasonAborted {
		abortMessage := "Operation aborted"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" && *message.ErrorMessage != "Request was aborted" {
			abortMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", abortMessage), c.outputPad, 0, nil))
	} else if !hasToolCalls && message.StopReason == ai.StopReasonError {
		errorMessage := "Unknown error"
		if message.ErrorMessage != nil && *message.ErrorMessage != "" {
			errorMessage = *message.ErrorMessage
		}
		c.contentContainer.AddChild(tui.NewSpacer(1))
		c.contentContainer.AddChild(tui.NewText(theme.FG("error", "Error: "+errorMessage), c.outputPad, 0, nil))
	}
}

func (c *AssistantMessageComponent) Invalidate() { c.container.Invalidate() }
func (c *AssistantMessageComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	hasToolCalls := c.hasToolCalls
	lines := c.container.Render(width)
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
	mu              sync.Mutex
	container       *tui.Container
	contentBox      *tui.Box
	toolName        string
	toolCallID      string
	args            any
	expanded        bool
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
	bgFn := func(t string) string { return theme.BG("toolPendingBg", t) }
	box := tui.NewBox(1, 1, bgFn)
	box.AddChild(tui.NewText(theme.FG("toolTitle", theme.Bold(toolName)), 0, 0, nil))

	c := &ToolExecutionComponent{
		container:     &tui.Container{},
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
	c.container.AddChild(tui.NewSpacer(1))
	c.container.AddChild(box)
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

func (c *ToolExecutionComponent) updateDisplay() {
	var bgFn func(string) string
	if c.isPartial {
		bgFn = func(t string) string { return theme.BG("toolPendingBg", t) }
	} else if c.result != nil && c.result.IsError {
		bgFn = func(t string) string { return theme.BG("toolErrorBg", t) }
	} else {
		bgFn = func(t string) string { return theme.BG("toolSuccessBg", t) }
	}

	c.contentBox.SetBackground(bgFn)
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
			c.contentBox.AddChild(rendered)
		}
	} else {
		c.contentBox.AddChild(tui.NewText(theme.FG("toolTitle", theme.Bold(c.toolName)), 0, 0, nil))
	}

	// Tool result
	if c.result != nil {
		if c.toolDef != nil && c.toolDef.RenderResult != nil {
			rendered := c.toolDef.RenderResult(
				agent.AgentToolResult{Content: c.result.Content, Details: c.result.Details},
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
				c.contentBox.AddChild(rendered)
			}
		} else {
			output := c.getTextOutput()
			if output != "" {
				c.contentBox.AddChild(tui.NewText(theme.FG("toolOutput", output), 0, 0, nil))
			}
		}
	}
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

func (c *ToolExecutionComponent) Invalidate() { c.container.Invalidate() }
func (c *ToolExecutionComponent) Render(width int) []string {
	return c.container.Render(width)
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
func (adapter themeAdapter) ThinkingBorderColor(level agent.ThinkingLevel) func(string) string {
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

const bashPreviewLines = 20

type BashExecutionComponent struct {
	mu         sync.Mutex
	container  *tui.Container
	command    string
	output     strings.Builder
	exitCode   *int
	cancelled  bool
	complete   bool
	expanded   bool
	excludeCtx bool
	loader     *tui.Loader
	ui         tui.RenderRequester
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

func (c *BashExecutionComponent) rebuild() {
	c.container.Clear()
	colorKey := "bashMode"
	if c.excludeCtx {
		colorKey = "dim"
	}
	borderColor := func(value string) string { return theme.FG(colorKey, value) }

	c.container.AddChild(tui.NewSpacer(1))

	// Top border
	c.container.AddChild(NewDynamicBorderWithColor(borderColor))

	// Command header
	prefix := "$ "
	if c.excludeCtx {
		prefix = "!! "
	}
	c.container.AddChild(tui.NewText(theme.FG(colorKey, theme.Bold(prefix+c.command)), 1, 0, nil))

	// Output
	output := c.output.String()
	if output != "" {
		if !c.expanded && c.complete {
			lines := strings.Split(output, "\n")
			if len(lines) > bashPreviewLines {
				shown := strings.Join(lines[len(lines)-bashPreviewLines:], "\n")
				skipped := len(lines) - bashPreviewLines
				c.container.AddChild(tui.NewText(
					theme.FG("dim", fmt.Sprintf("... %d lines hidden (%s to expand)", skipped, KeyText("app.tools.expand"))),
					1, 0, nil,
				))
				c.container.AddChild(tui.NewText("\n"+shown, 1, 0, nil))
			} else {
				c.container.AddChild(tui.NewText("\n"+output, 1, 0, nil))
			}
		} else {
			c.container.AddChild(tui.NewText("\n"+output, 1, 0, nil))
		}
	}

	// Status
	if !c.complete && c.loader != nil {
		c.container.AddChild(c.loader)
	} else if c.complete {
		var status string
		if c.cancelled {
			status = theme.FG("warning", "(cancelled)")
		} else if c.exitCode != nil && *c.exitCode != 0 {
			status = theme.FG("error", fmt.Sprintf("(exit %d)", *c.exitCode))
		}
		if status != "" {
			c.container.AddChild(tui.NewText(status, 1, 0, nil))
		}
	}

	// Bottom border
	c.container.AddChild(NewDynamicBorderWithColor(borderColor))
}

func (c *BashExecutionComponent) Invalidate()               { c.container.Invalidate() }
func (c *BashExecutionComponent) Render(width int) []string { return c.container.Render(width) }

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
	Kind StatusIndicatorKind
}

func (si *StatusIndicator) Dispose() { si.Stop() }

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
	msg := fmt.Sprintf("Retrying (%d/%d) in %ds... (%s to cancel)",
		attempt, maxAttempts, (delayMS+999)/1000, KeyText("app.interrupt"))
	return &StatusIndicator{
		Loader: tui.NewLoader(ui,
			func(s string) string { return theme.FG("warning", s) },
			func(s string) string { return theme.FG("muted", s) },
			msg, nil,
		),
		Kind: StatusRetry,
	}
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

type FooterComponent struct {
	session            footerSession
	provider           footerDataProvider
	autoCompactEnabled bool
}

type footerSession interface {
	State() agent.AgentState
}

type footerDataProvider interface {
	GitBranch() string
	Statuses() map[string]string
}

func NewFooterComponent(session footerSession, provider footerDataProvider) *FooterComponent {
	return &FooterComponent{session: session, provider: provider, autoCompactEnabled: true}
}

func (f *FooterComponent) Invalidate() {}

func (f *FooterComponent) Render(width int) []string {
	state := f.session.State()
	pwd := ""
	if provider, ok := f.provider.(interface{ CurrentCWD() string }); ok {
		pwd = provider.CurrentCWD()
	}
	if branch := f.provider.GitBranch(); branch != "" {
		pwd += " (" + branch + ")"
	}
	if provider, ok := f.provider.(interface{ SessionName() string }); ok {
		if name := provider.SessionName(); name != "" {
			pwd += " • " + name
		}
	}

	stats := codingagent.SessionStats{}
	if session, ok := f.session.(interface {
		GetSessionStats() codingagent.SessionStats
	}); ok {
		stats = session.GetSessionStats()
	}
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
	if stats.Cost > 0 {
		statsParts = append(statsParts, fmt.Sprintf("$%.3f", stats.Cost))
	}
	contextWindow := int64(0)
	if state.Model != nil {
		contextWindow = int64(state.Model.ContextWindow)
	}
	percent := "?"
	if stats.ContextUsage != nil {
		if stats.ContextUsage.ContextWindow > 0 {
			contextWindow = int64(stats.ContextUsage.ContextWindow)
		}
		if stats.ContextUsage.Percent != nil {
			percent = fmt.Sprintf("%.1f", *stats.ContextUsage.Percent)
		}
	}
	autoCompactEnabled := f.autoCompactEnabled
	if session, ok := f.session.(interface{ AutoCompactionEnabled() bool }); ok {
		autoCompactEnabled = session.AutoCompactionEnabled()
	}
	auto := ""
	if autoCompactEnabled {
		auto = " (auto)"
	}
	contextDisplay := percent + "%/"
	if percent == "?" {
		contextDisplay = "?/"
	}
	statsParts = append(statsParts, contextDisplay+formatTokens(contextWindow)+auto)
	statsLeft := strings.Join(statsParts, " ")
	if tui.VisibleWidth(statsLeft) > width {
		statsLeft = tui.TruncateToWidth(statsLeft, width, "...", false)
	}

	modelName := "no-model"
	if state.Model != nil {
		modelName = state.Model.ID
		if provider, ok := f.provider.(interface{ AvailableProviderCount() int }); ok && provider.AvailableProviderCount() > 1 {
			modelName = "(" + string(state.Model.Provider) + ") " + modelName
		}
		if state.Model.Reasoning {
			level := string(state.ThinkingLevel)
			if level == "" {
				level = "off"
			}
			if level == "off" {
				modelName += " • thinking off"
			} else {
				modelName += " • " + level
			}
		}
	}
	availableRight := width - tui.VisibleWidth(statsLeft) - 2
	if availableRight < tui.VisibleWidth(modelName) && state.Model != nil && strings.HasPrefix(modelName, "(") {
		modelName = state.Model.ID
	}
	if availableRight < tui.VisibleWidth(modelName) {
		modelName = tui.TruncateToWidth(modelName, max(0, availableRight), "", false)
	}
	padding := strings.Repeat(" ", max(0, width-tui.VisibleWidth(statsLeft)-tui.VisibleWidth(modelName)))
	lines := []string{
		tui.TruncateToWidth(theme.FG("dim", pwd), width, theme.FG("dim", "..."), false),
		theme.FG("dim", statsLeft) + theme.FG("dim", padding+modelName),
	}

	statuses := f.provider.Statuses()
	keys := make([]string, 0, len(statuses))
	for key := range statuses {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		values := make([]string, 0, len(keys))
		for _, key := range keys {
			values = append(values, strings.Join(strings.Fields(statuses[key]), " "))
		}
		lines = append(lines, tui.TruncateToWidth(strings.Join(values, " "), width, theme.FG("dim", "..."), false))
	}
	return lines
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
		box:     tui.NewBox(1, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
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

func (c *CompactionSummaryMessageComponent) Invalidate()               { c.box.Invalidate() }
func (c *CompactionSummaryMessageComponent) Render(width int) []string { return c.box.Render(width) }

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
		box:     tui.NewBox(1, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
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

func (c *BranchSummaryMessageComponent) Invalidate()               { c.box.Invalidate() }
func (c *BranchSummaryMessageComponent) Render(width int) []string { return c.box.Render(width) }

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
		box:     tui.NewBox(1, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
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

func (c *SkillInvocationMessageComponent) Invalidate()               { c.box.Invalidate() }
func (c *SkillInvocationMessageComponent) Render(width int) []string { return c.box.Render(width) }

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
	box := tui.NewBox(1, 1, func(t string) string { return theme.BG("customMessageBg", t) })
	container.AddChild(box)
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
