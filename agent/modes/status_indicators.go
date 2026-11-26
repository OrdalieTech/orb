package modes

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/internal/truncate"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

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
	truncated   bool // earlier output was discarded, here or by the executor
	fullOutput  string
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
	start := time.Now()
	c.loader = tui.NewLoader(ui,
		func(s string) string { return theme.FG("bashMode", s) },
		func(s string) string { return theme.Shimmer(s, time.Since(start)) },
		"Running... ("+KeyText("tui.select.cancel")+" to cancel)", nil,
	)
	c.rebuild()
	return c
}

func (c *BashExecutionComponent) AppendOutput(text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.output.WriteString(text)
	// The executor keeps the full output on disk; the view keeps the tail the bash tool would.
	if c.output.Len() > 2*truncate.DefaultMaxBytes {
		tail := truncate.TruncateTail(c.output.String()).Content
		c.output.Reset()
		c.output.WriteString(tail)
		c.truncated = true
	}
	c.rebuild()
}

func (c *BashExecutionComponent) SetComplete(exitCode *int, cancelled, truncated bool, fullOutputPath *string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.exitCode = exitCode
	c.cancelled = cancelled
	c.truncated = c.truncated || truncated
	if fullOutputPath != nil {
		c.fullOutput = *fullOutputPath
	}
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
		if c.truncated && c.fullOutput != "" {
			statusParts = append(statusParts, theme.FG("warning", "Output truncated. Full output: "+c.fullOutput))
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
	if len(options) > 0 {
		indicator = loaderIndicatorOptions(options[0])
	}
	// The message shimmers, on the spinner's ticks, while the terminal has focus.
	start := time.Now()
	focus, _ := ui.(interface{ TerminalFocused() bool })
	return newStatusIndicator(ui, StatusWorking, "accent", message, indicator, func(s string) string {
		if focus != nil && !focus.TerminalFocused() {
			return theme.FG("muted", s)
		}
		return theme.Shimmer(s, time.Since(start))
	})
}

// newStatusIndicator draws the animated spinner in color and the message muted (or custom).
func newStatusIndicator(ui tui.RenderRequester, kind StatusIndicatorKind, color, message string, indicator *tui.LoaderIndicatorOptions, messageColor ...tui.StyleFunc) *StatusIndicator {
	start := time.Now()
	style := tui.StyleFunc(func(s string) string { return theme.Shimmer(s, time.Since(start)) })
	if len(messageColor) > 0 {
		style = messageColor[0]
	}
	return &StatusIndicator{
		Loader: tui.NewLoader(ui, func(s string) string { return theme.FG(color, s) }, style, message, indicator),
		Kind:   kind,
	}
}

func NewRetryStatusIndicator(ui tui.RenderRequester, attempt, maxAttempts int, delayMS int64) *StatusIndicator {
	retryMessage := func(seconds int) string {
		return fmt.Sprintf("Retrying (%d/%d) in %ds... (%s to cancel)", attempt, maxAttempts, seconds, KeyText("app.interrupt"))
	}
	status := newStatusIndicator(ui, StatusRetry, "warning", retryMessage(int((delayMS+999)/1000)), nil)
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
	return newStatusIndicator(ui, StatusCompaction, "accent", label, nil)
}

func NewBranchSummaryStatusIndicator(ui tui.RenderRequester) *StatusIndicator {
	return newStatusIndicator(ui, StatusBranchSummary, "accent", fmt.Sprintf("Summarizing branch... (%s to cancel)", KeyText("app.interrupt")), nil)
}

// IdleStatus renders two empty lines (same height as a status indicator).
type IdleStatus struct{}

func (IdleStatus) Invalidate() {}
func (IdleStatus) Render(width int) []string {
	empty := strings.Repeat(" ", width)
	return []string{empty, empty}
}
