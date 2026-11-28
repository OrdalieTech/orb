package modes

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"

	theme "github.com/OrdalieTech/orb/agent/modes/theme"
)

const (
	toolPreviewLines         = 3
	toolExpandedPreviewLines = 12
	editPreviewLines         = 5
)

// keeping at least two spaces between them and dropping right when too narrow.
func formatRowWithRight(left, right string, width, keepTailFrom int) string {
	if width <= 0 {
		return ""
	}
	rightW := tui.VisibleWidth(right)
	if right == "" || rightW+6 > width {
		if keepTailFrom > 0 && tui.VisibleWidth(left) > width {
			left = middleCut(left, keepTailFrom, width)
		}
		return tui.TruncateToWidth(left, width, "…", false)
	}
	leftBudget := width - rightW - 2
	if keepTailFrom > 0 && tui.VisibleWidth(left) > leftBudget {
		left = middleCut(left, keepTailFrom, leftBudget)
	}
	clipped := tui.TruncateToWidth(left, leftBudget, "…", false)
	gap := max(2, width-tui.VisibleWidth(clipped)-rightW)
	return clipped + strings.Repeat(" ", gap) + right
}

const nestedVerbWidth = 10

func padVerb(verb string) string {
	w := tui.VisibleWidth(verb)
	if w >= nestedVerbWidth-1 {
		return verb + " "
	}
	return verb + strings.Repeat(" ", nestedVerbWidth-w)
}

func formatDurationShort(d time.Duration) string {
	if d < 100*time.Millisecond {
		return ""
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}

// renderTonalTray wraps body lines in a soft borderless background band
// (toolSuccessBg or toolErrorBg) padded cleanly to width.
func renderTonalTray(lines []string, width int, indent int, bgRole string) []string {
	if len(lines) == 0 {
		return nil
	}
	pad := strings.Repeat(" ", max(0, indent))
	bg := ""
	if bgRole != "" {
		bg = theme.BGANSI(bgRole)
	}
	out := make([]string, len(lines))
	if bg == "" || width <= indent+2 {
		for i, line := range lines {
			out[i] = pad + tui.TruncateToWidth(line, max(1, width-indent), "…", false)
		}
		return out
	}
	trayW := width - indent
	innerW := max(1, trayW-2)
	for i, line := range lines {
		if tui.IsImageLine(line) {
			out[i] = pad + line
			continue
		}
		// Lines already carrying their own background (e.g. diff added/removed rows)
		// keep their tint while aligning to the same indent.
		if strings.Contains(line, "\x1b[48;") {
			out[i] = pad + tui.TruncateToWidth(line, trayW, "", false)
			continue
		}
		clipped := tui.TruncateToWidth(line, innerW, "…", false)
		fill := max(0, innerW-tui.VisibleWidth(clipped))
		out[i] = pad + bg + " " + tui.ReopenAfterReset(bg, clipped) + strings.Repeat(" ", fill+1) + "\x1b[49m"
	}
	return out
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
	showAll         bool
	hovered         bool
	showImages      bool
	isPartial       bool
	result          *toolResult
	toolDef         *extensions.ToolDefinition
	ui              tui.RenderRequester
	cwd             string
	execStarted     bool
	argsComplete    bool
	startedAt       time.Time
	duration        time.Duration
	rendererState   map[string]any
	callComponent   extensions.Component
	resultComponent extensions.Component

	callFailed, resultFailed bool
	// drawn is set once the display is first built, at the first render: a
	// long conversation opens with thousands of tools, most never on screen.
	drawn bool
	// faded caches the resting look of the last rendered lines.
	fadedFrom, faded []string
	// headed sits right under the reasoning that led to it.
	headed bool
	// nested marks a tool rendered inside an expanded toolActivityGroup.
	nested bool
	// moreRow is the rendered row index of the "click to show all" hint, or -1.
	moreRow int
}

// restingOpacity is how a settled tool block sits behind the conversation;
// hovering, expanding, running or failing brings it to full opacity.
const restingOpacity = .55

// fade returns lines at resting opacity, reusing the last result while the
// lines are unchanged.
func fade(lines []string, from, faded *[]string) []string {
	if slices.Equal(lines, *from) {
		return *faded
	}
	*from, *faded = lines, theme.Current().Fade(lines, restingOpacity)
	return *faded
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
		moreRow:       -1,
	}
	return c
}

func (c *ToolExecutionComponent) UpdateArgs(args any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.args = args
	c.refresh()
}

func (c *ToolExecutionComponent) MarkExecutionStarted() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execStarted = true
	if c.startedAt.IsZero() {
		c.startedAt = time.Now()
	}
	c.refresh()
}

func (c *ToolExecutionComponent) SetArgsComplete() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.argsComplete = true
	c.refresh()
}

func (c *ToolExecutionComponent) UpdateResult(content ai.ToolResultContent, isError bool, details any, partial bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.result = &toolResult{Content: content, IsError: isError, Details: details}
	c.isPartial = partial
	if !partial && !c.startedAt.IsZero() && c.duration == 0 {
		c.duration = time.Since(c.startedAt)
	}
	c.refresh()
}

func (c *ToolExecutionComponent) SetExpanded(expanded bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expanded = expanded
	c.showAll = expanded
	c.refresh()
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
			if c.expanded && !c.showAll && c.moreRow >= 0 && event.Row == c.moreRow {
				c.showAll = true
			} else {
				c.expanded = !c.expanded
				c.showAll = false
			}
			c.refresh()
			changed = true
		}
	}
	c.mu.Unlock()
	if changed && c.ui != nil {
		c.ui.RequestRender()
	}
	return changed
}

// refresh redraws a change once the tool is on screen; before, the first
// render draws the latest state.
func (c *ToolExecutionComponent) refresh() {
	if c.drawn {
		c.updateDisplay()
	}
}

func (c *ToolExecutionComponent) updateDisplay() {
	c.renderTheme = theme.Current().Palette()
	c.contentBox.Clear()
	if ex, ok := toolExchange(c); ok {
		c.contentBox.AddChild(NewExchangeComponent(ex, 0, c.expanded))
		return
	}

	rightStat := c.rightStatLocked()
	// Tool call header
	if c.callFailed {
		c.contentBox.AddChild(c.rendererFallback(false))
	} else if c.toolDef != nil && c.toolDef.RenderCall != nil {
		var rendered extensions.Component
		rendererInvoke(&c.callFailed, func() {
			rendered = c.toolDef.RenderCall(c.args, themeAdapter{}, extensions.ToolRenderContext{
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
		})
		if c.callFailed {
			c.contentBox.AddChild(c.rendererFallback(false))
		} else if rendered != nil {
			c.callComponent = rendered
			if header, ok := rendered.(toolCallHeader); ok {
				header.rightStat = rightStat
				header.nested = c.nested
				rendered = header
			} else if toolActivityKind(c.toolName) != "" || strings.EqualFold(c.toolName, "bash") {
				rendered = toolCallHeader{inner: rendered, expanded: c.expanded, rightStat: rightStat, nested: c.nested}
			}
			c.contentBox.AddChild(&safeToolRenderer{source: c.callComponent, inner: rendered, failed: &c.callFailed, fallback: func() tui.Component { return c.rendererFallback(false) }})
		}
	} else {
		title, detailFrom := fallbackToolTitle(c.toolName, c.args)
		_, detail, _ := strings.Cut(tui.StripANSI(title), " ")
		c.contentBox.AddChild(toolCallHeader{inner: tui.NewText(title, 0, 0, nil), expanded: c.expanded, title: title, verb: c.toolName, detail: detail, keepTailFrom: detailFrom, rightStat: rightStat, nested: c.nested})
	}

	// Tool result
	if c.result != nil {
		bgRole := "toolSuccessBg"
		if c.result.IsError {
			bgRole = "toolErrorBg"
		}
		if c.resultFailed {
			c.contentBox.AddChild(c.rendererFallback(true))
		} else if c.toolDef != nil && c.toolDef.RenderResult != nil {
			var rendered extensions.Component
			rendererInvoke(&c.resultFailed, func() {
				rendered = c.toolDef.RenderResult(
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
			})
			if c.resultFailed {
				c.contentBox.AddChild(c.rendererFallback(true))
			} else if rendered != nil {
				c.resultComponent = rendered
				_, plainOutput := rendered.(*toolOutputPreview)
				_, diffOutput := rendered.(*editDiffView)
				if (!plainOutput && !diffOutput) || c.showOutput() {
					c.contentBox.AddChild(&safeToolRenderer{
						source: rendered,
						inner: toolResultClip{
							inner:    rendered,
							expanded: c.expanded,
							showAll:  c.showAll,
							bgRole:   bgRole,
							isDiff:   diffOutput,
						},
						failed:   &c.resultFailed,
						fallback: func() tui.Component { return c.rendererFallback(true) },
					})
				}
			}
		} else if c.showOutput() {
			output := c.getTextOutput()
			if output != "" {
				c.contentBox.AddChild(toolResultClip{
					inner: newToolOutputPreview(
						output,
						extensions.ToolRenderResultOptions{Expanded: c.expanded, IsPartial: c.isPartial},
						themeAdapter{},
					),
					expanded: c.expanded,
					showAll:  c.showAll,
					bgRole:   bgRole,
				})
			}
		}
	}
}

// All renderer boundaries run under the owning execution's mutex (or during construction).
// Keep the original components in LastComponent; only the display tree uses wrappers.
func rendererInvoke(failed *bool, invoke func()) {
	if *failed {
		return
	}
	defer func() {
		if recover() != nil {
			*failed = true
		}
	}()
	invoke()
}

type safeToolRenderer struct {
	source   tui.Component
	inner    tui.Component
	failed   *bool
	fallback func() tui.Component
}

func (r *safeToolRenderer) Render(width int) (lines []string) {
	rendererInvoke(r.failed, func() { lines = r.inner.Render(width) })
	if *r.failed {
		return r.fallback().Render(width)
	}
	return lines
}

func (r *safeToolRenderer) Invalidate() {
	if component, ok := r.source.(tui.Invalidatable); ok {
		rendererInvoke(r.failed, component.Invalidate)
	}
}

func (c *ToolExecutionComponent) rendererFallback(result bool) tui.Component {
	box := tui.NewBox(0, 0, nil)
	if result {
		box.AddChild(toolResultClip{inner: newToolOutputPreview(c.getTextOutput(), extensions.ToolRenderResultOptions{Expanded: c.expanded, IsPartial: c.isPartial}, themeAdapter{}), expanded: c.expanded})
	} else {
		title, detailFrom := fallbackToolTitle(c.toolName, c.args)
		box.AddChild(toolCallHeader{inner: tui.NewText(title, 0, 0, nil), expanded: c.expanded, title: title, keepTailFrom: detailFrom})
	}
	box.AddChild(tui.NewText(c.toolName+": custom renderer failed; using built-in output", 0, 0, nil))
	return box
}

func (c *ToolExecutionComponent) showOutput() bool {
	kind := toolActivityKind(c.toolName)
	return c.expanded || c.result != nil && c.result.IsError || c.isPartial && (kind == "" || kind == "command")
}

func (c *ToolExecutionComponent) rightStatLocked() string {
	kind := strings.ToLower(c.toolName)
	if c.result != nil && c.result.IsError {
		if kind == "bash" {
			if code := extractExitCode(c.getTextOutput()); code != "" {
				return theme.FG("error", "× exit "+code)
			}
		}
		return theme.FG("error", "× failed")
	}
	if kind == "edit" {
		diff := ""
		if c.result != nil {
			diff = editResultDiff(c.result.Details)
		}
		if diff == "" {
			diff = loadEditPreview(c.rendererState).diff
		}
		if diff != "" {
			added, removed := DiffStats(diff)
			var parts []string
			if added > 0 {
				parts = append(parts, theme.FG("toolDiffAdded", fmt.Sprintf("+%d", added)))
			}
			if removed > 0 {
				parts = append(parts, theme.FG("toolDiffRemoved", fmt.Sprintf("-%d", removed)))
			}
			if len(parts) > 0 {
				return strings.Join(parts, " ")
			}
		}
	}
	if kind == "write" {
		if values, ok := c.args.(map[string]any); ok {
			if content, ok := values["content"].(string); ok && content != "" {
				return theme.Faint(fmt.Sprintf("+%d ln", strings.Count(strings.TrimRight(content, "\n"), "\n")+1))
			}
		}
	}
	if c.result != nil && !c.isPartial {
		if out := strings.TrimSpace(c.getTextOutput()); out != "" {
			lines := strings.Count(out, "\n") + 1
			if kind == "read" || kind == "grep" || kind == "find" || kind == "ls" || kind == "bash" || kind == "web_search" || kind == "fetch_content" || lines > 1 {
				return theme.Faint(fmt.Sprintf("%d ln", lines))
			}
		}
		if dur := formatDurationShort(c.duration); dur != "" {
			return theme.Faint(dur)
		}
	}
	return ""
}

func extractExitCode(text string) string {
	for _, prefix := range []string{"Command exited with code ", "exited with code ", "exit status "} {
		if idx := strings.LastIndex(text, prefix); idx >= 0 {
			rest := text[idx+len(prefix):]
			end := 0
			for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
				end++
			}
			if end > 0 {
				return rest[:end]
			}
		}
	}
	return ""
}

type toolCallHeader struct {
	inner        tui.Component
	expanded     bool
	title        string
	verb         string
	detail       string
	keepTailFrom int
	rightStat    string
	nested       bool
}

func (header toolCallHeader) Invalidate() {
	if component, ok := header.inner.(tui.Invalidatable); ok {
		component.Invalidate()
	}
}

func (header toolCallHeader) Render(width int) []string {
	title := header.title
	keepTailFrom := header.keepTailFrom
	if header.nested && header.verb != "" {
		padded := padVerb(header.verb)
		if header.detail != "" {
			title = theme.FG("toolTitle", padded) + theme.FG("muted", header.detail)
			if keepTailFrom > 0 {
				keepTailFrom = tui.VisibleWidth(padded)
			}
		} else {
			title = theme.FG("toolTitle", header.verb)
		}
	}
	if !header.expanded && title != "" {
		firstLine, _, multiline := strings.Cut(title, "\n")
		if multiline && header.rightStat == "" {
			budget := max(0, width-2)
			if keepTailFrom > 0 && tui.VisibleWidth(firstLine) > budget {
				firstLine = middleCut(firstLine, keepTailFrom, budget)
			}
			return []string{tui.TruncateToWidth(firstLine, budget, "…", false) + theme.Faint(tui.TruncateToWidth(" …", width, "", false))}
		}
		return []string{formatRowWithRight(firstLine, header.rightStat, width, keepTailFrom)}
	}
	lines := header.inner.Render(width)
	if len(lines) == 0 {
		return lines
	}
	if header.expanded {
		if header.nested && header.verb != "" && len(lines) == 1 {
			return []string{formatRowWithRight(title, header.rightStat, width, 0)}
		}
		if header.rightStat != "" && len(lines) == 1 {
			return []string{formatRowWithRight(strings.TrimRight(lines[0], " "), header.rightStat, width, 0)}
		}
		return lines
	}
	first := strings.TrimRight(lines[0], " ")
	if len(lines) > 1 && header.rightStat == "" {
		return []string{tui.TruncateToWidth(first, max(0, width-2), "…", false) + theme.Faint(tui.TruncateToWidth(" …", width, "", false))}
	}
	return []string{formatRowWithRight(first, header.rightStat, width, 0)}
}

// middleCut fits a title whose tail from column from is a path or URL into
// budget columns by dropping its middle at slashes: the host or first folder
// stays, then as many final segments as fit.
func middleCut(title string, from, budget int) string {
	full := tui.VisibleWidth(title)
	room := budget - from - 1
	if room < 8 {
		return title
	}
	detail := tui.StripANSI(tui.SliceByColumn(title, from, full-from, true))
	head := 0
	scheme := strings.Index(detail, "://")
	if scheme >= 0 {
		if slash := strings.IndexByte(detail[scheme+3:], '/'); slash >= 0 {
			head = tui.VisibleWidth(detail[:scheme+3+slash+1])
		}
	} else if slash := strings.IndexByte(detail[1:], '/'); slash >= 0 {
		head = tui.VisibleWidth(detail[:slash+2])
	}
	if head == 0 || head > room/2 {
		head = room / 3
	}
	tail := room - head
	fitted := false
	for index := range len(detail) {
		if detail[index] == '/' && index > 0 {
			if width := tui.VisibleWidth(detail[index:]); width <= tail {
				tail, fitted = width, true
				break
			}
		}
	}
	// A file's name matters more than its first folder; a URL keeps its host.
	if last := strings.LastIndexByte(detail, '/'); !fitted && last > 0 && scheme < 0 {
		if width := tui.VisibleWidth(detail[last:]); width <= room-4 {
			head, tail = room-width, width
		}
	}
	return tui.SliceByColumn(title, 0, from+head, true) + "…" + tui.SliceByColumn(title, full-tail, tail, true)
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
	showAll  bool
	bgRole   string
	isDiff   bool
}

func (preview toolResultClip) Render(width int) []string {
	indent := 0
	contentW := width
	if preview.bgRole != "" && !preview.isDiff && width > 2 {
		contentW = width - 2
	}
	lines := preview.inner.Render(max(1, contentW))
	for len(lines) > 0 && !tui.IsImageLine(lines[0]) && strings.TrimSpace(tui.StripANSI(lines[0])) == "" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return nil
	}
	_, alreadyClipped := preview.inner.(*toolOutputPreview)
	count := len(lines)
	hint := ""
	if !preview.expanded && !alreadyClipped {
		limit := toolPreviewLines
		if preview.isDiff {
			limit = editPreviewLines
		}
		if count > limit {
			count = limit
			hint = fmt.Sprintf("… %d more · click to expand", len(lines)-count)
		}
	} else if preview.expanded && !preview.showAll && count > toolExpandedPreviewLines {
		count = toolExpandedPreviewLines
		if preview.isDiff {
			hint = fmt.Sprintf("… %d more diff lines · click to show all", len(lines)-count)
		} else {
			hint = fmt.Sprintf("… %d more lines · click to show all", len(lines)-count)
		}
	}
	visible := append([]string(nil), lines[:count]...)
	if hint != "" {
		visible = append(visible, theme.Faint(hint))
	}
	return renderTonalTray(visible, width, indent, preview.bgRole)
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
	style := func(output string) string {
		lines := strings.Split(output, "\n")
		for index := range lines {
			lines[index] = preview.palette.FG("toolOutput", lines[index])
		}
		return strings.Join(lines, "\n")
	}
	if preview.options.Expanded {
		return tui.NewText(style(output), 0, 0, nil).Render(width)
	}
	output, earlier := lastOutputLines(output, toolPreviewLines)
	truncated := tui.TruncateToVisualLines(style(output), toolPreviewLines, width, 0)
	lines := append([]string(nil), truncated.VisualLines...)
	if earlier || truncated.SkippedCount > 0 {
		hint := theme.Faint("… earlier output · click to expand")
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

func (c *ToolExecutionComponent) Invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.contentBox.Invalidate()
}

func (c *ToolExecutionComponent) renderNested(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	wasNested := c.nested
	c.nested = true
	if !c.drawn || !wasNested || c.renderTheme != theme.Current().Palette() {
		if c.drawn && c.renderTheme != theme.Current().Palette() {
			c.callComponent, c.resultComponent = nil, nil
		}
		c.drawn = true
		c.updateDisplay()
	}
	// Render children directly at width without outer band padding (the group rail supplies it).
	var lines []string
	c.moreRow = -1
	children := c.contentBox.Children
	for i, child := range children {
		rendered := child.Render(width)
		if i == 0 && len(rendered) > 0 {
			rendered = append([]string(nil), rendered...)
			rendered[0] = c.alignNestedHeaderLocked(rendered[0], width)
		}
		lines = append(lines, rendered...)
	}
	if c.expanded && !c.showAll && len(lines) > 1 && strings.Contains(tui.StripANSI(lines[len(lines)-1]), "click to show all") {
		c.moreRow = len(lines) - 1
	}
	return lines
}

func (c *ToolExecutionComponent) alignNestedHeaderLocked(line string, width int) string {
	if c.isPartial {
		var elapsed time.Duration
		if !c.startedAt.IsZero() {
			elapsed = time.Since(c.startedAt)
		}
		dot := theme.FG("muted", "◌")
		if c.execStarted || c.argsComplete {
			dot = theme.PulseDot("accent", elapsed)
		}
		return tui.TruncateToWidth(dot+" "+line, width, "…", false)
	}
	if c.result != nil && c.result.IsError {
		return tui.TruncateToWidth(theme.FG("error", "×")+" "+line, width, "…", false)
	}
	return line
}

func (c *ToolExecutionComponent) Render(width int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	wasNested := c.nested
	c.nested = false
	if !c.drawn || wasNested || c.renderTheme != theme.Current().Palette() {
		if c.drawn && c.renderTheme != theme.Current().Palette() {
			c.callComponent, c.resultComponent = nil, nil
		}
		c.drawn = true
		c.updateDisplay()
	}
	var marker string
	isErr := c.result != nil && c.result.IsError
	switch {
	case c.isPartial:
		if c.execStarted || c.argsComplete {
			var elapsed time.Duration
			if !c.startedAt.IsZero() {
				elapsed = time.Since(c.startedAt)
			}
			marker = theme.PulseDot("accent", elapsed)
		} else {
			marker = theme.FG("muted", "◌")
		}
	case isErr:
		marker = theme.FG("error", "×")
	case c.expanded:
		marker = theme.FG("muted", "⌄")
	case c.hovered:
		marker = theme.FG("toolTitle", "›")
	default:
		marker = theme.Faint("›")
	}

	var lines []string
	c.moreRow = -1
	headerPad := min(3, max(0, width-1))
	bodyWidth := max(1, width-headerPad)
	children := c.contentBox.Children
	for i, child := range children {
		if i == 0 {
			rendered := child.Render(bodyWidth)
			for j, row := range rendered {
				if j == 0 && width > 2 {
					lines = append(lines, " "+marker+" "+row)
				} else if width > 3 {
					lines = append(lines, " "+theme.Rail()+" "+row)
				} else {
					lines = append(lines, row)
				}
			}
			continue
		}
		// Body lines (soft tray / diff) sit behind a whisper-quiet hairline rail
		// aligned at column 1, directly below the header marker.
		railWidth := max(1, width-4)
		rendered := child.Render(railWidth)
		railPrefix := " " + theme.Rail() + "  "
		if width <= 4 {
			railPrefix = ""
		}
		for _, row := range rendered {
			if railPrefix != "" {
				lines = append(lines, railPrefix+row)
			} else {
				lines = append(lines, row)
			}
		}
	}
	if !c.hovered && !c.expanded && !c.isPartial && !isErr {
		lines = fade(lines, &c.fadedFrom, &c.faded)
	}
	offset := 0
	if !c.headed {
		lines = append([]string{""}, lines...)
		offset = 1
	}
	if c.expanded && !c.showAll && len(lines) > offset+1 && strings.Contains(tui.StripANSI(lines[len(lines)-1]), "click to show all") {
		c.moreRow = len(lines) - 1
	}
	return lines
}

type toolActivityGroup struct {
	mu    sync.Mutex
	tools []*ToolExecutionComponent
	// items keeps tools and the reasoning folded between them in order.
	items     []tui.Component
	expanded  bool
	hovered   bool
	hover     *ToolExecutionComponent
	rows      []toolActivityRow
	ui        tui.RenderRequester
	startedAt time.Time
	// faded caches the header's resting look.
	fadedFrom, faded []string
	// headed sits right under the reasoning that led to it.
	headed bool
}

type toolActivityRow struct {
	tool               *ToolExecutionComponent
	start, end, indent int
}

// fallbackToolTitle names a tool without a renderer by its most telling
// argument, and returns where that argument starts when it is a path or URL,
// whose ends matter more than its middle.
func fallbackToolTitle(name string, args any) (string, int) {
	title := theme.FG("toolTitle", theme.Bold(name))
	values, _ := args.(map[string]any)
	if strings.EqualFold(name, "memory") && len(values) > 0 {
		if summary := memoryToolSummary(values); summary != "" {
			return title + theme.FG("muted", " "+summary), 0
		}
	}
	for _, key := range []string{"file_path", "path", "command", "url", "urls", "query", "pattern", "description", "input"} {
		value, more := values[key], 0
		if list, ok := value.([]any); ok && len(list) > 0 {
			value, more = list[0], len(list)-1
		}
		text, ok := value.(string)
		if !ok || strings.TrimSpace(text) == "" {
			continue
		}
		text = strings.Join(strings.Fields(text), " ")
		detailFrom := 0
		if !strings.Contains(text, " ") {
			detailFrom = tui.VisibleWidth(name) + 1
		}
		if more > 0 {
			text += fmt.Sprintf(" +%d", more)
		}
		return title + theme.FG("muted", " "+text), detailFrom
	}
	return title, 0
}

// toolActivityKind names what a quiet tool does; consecutive quiet tools
// share one row. Edits, writes and anything else stand alone.
func toolActivityKind(name string) string {
	switch strings.ToLower(name) {
	case "read":
		return "read"
	case "grep", "find", "glob":
		return "search"
	case "ls":
		return "listing"
	case "bash":
		return "command"
	case "web_search", "websearch":
		return "web search"
	case "fetch_content", "webfetch", "web_fetch":
		return "page"
	case "memory":
		return "memory"
	}
	return ""
}

func memoryToolSummary(values map[string]any) string {
	action, _ := values["action"].(string)
	action = strings.TrimSpace(action)
	if action == "" {
		return ""
	}
	var detail string
	switch action {
	case "search":
		if q, ok := values["query"].(string); ok {
			detail = strings.Join(strings.Fields(q), " ")
		}
	case "zoom":
		if ref, ok := values["ref"].(string); ok && strings.TrimSpace(ref) != "" {
			detail = strings.TrimSpace(ref)
		} else if id, ok := intArg(values["id"]); ok {
			span, ok := intArg(values["n"])
			if !ok || span < 1 {
				span = 1
			}
			detail = fmt.Sprintf("%d+%d", id, span)
		}
	case "profile":
		if op, ok := values["op"].(string); ok {
			detail = strings.TrimSpace(op)
		}
	}
	if detail == "" {
		return action
	}
	return action + " " + detail
}

func intArg(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}

// activityKinds orders a group's counts; plurals are irregular for two.
var activityKinds = []struct{ kind, plural string }{
	{"command", "commands"}, {"read", "reads"}, {"search", "searches"}, {"listing", "listings"},
	{"web search", "web searches"}, {"page", "pages"}, {"memory", "memories"},
}

// activityVerb names a group by what it holds, as done or ongoing.
func activityVerb(counts map[string]int, total int, active bool) string {
	verbs := [2]string{"explored", "exploring"}
	switch web := counts["web search"] + counts["page"]; {
	case web == total:
		verbs = [2]string{"searched the web", "searching the web"}
	case counts["command"] == total:
		verbs = [2]string{"ran", "running"}
	case counts["command"] > 0:
		verbs = [2]string{"worked", "working"}
	}
	if active {
		return verbs[1]
	}
	return verbs[0]
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
	for _, item := range group.items {
		if invalidator, ok := item.(tui.Invalidatable); ok {
			invalidator.Invalidate()
		}
	}
}

// add appends a tool, or reasoning that came between the group's tools.
func (group *toolActivityGroup) add(item tui.Component) {
	group.mu.Lock()
	defer group.mu.Unlock()
	if group.startedAt.IsZero() {
		group.startedAt = time.Now()
	}
	if tool, ok := item.(*ToolExecutionComponent); ok {
		group.tools = append(group.tools, tool)
	}
	group.items = append(group.items, item)
}

func (group *toolActivityGroup) Render(width int) []string {
	group.mu.Lock()
	defer group.mu.Unlock()
	group.rows = group.rows[:0]
	var lines []string
	if len(group.tools) > 1 {
		counts := map[string]int{}
		active, running := false, ""
		failed := 0
		var totalDuration time.Duration
		for _, tool := range group.tools {
			counts[toolActivityKind(tool.toolName)]++
			tool.mu.Lock()
			if tool.isPartial {
				active = true
				title, _ := fallbackToolTitle(tool.toolName, tool.args)
				if argument, ok := strings.CutPrefix(tui.StripANSI(title), tool.toolName+" "); ok {
					running = argument
				}
			} else {
				totalDuration += tool.duration
				if tool.result != nil && tool.result.IsError {
					failed++
				}
			}
			tool.mu.Unlock()
		}
		var labels []string
		for _, kind := range activityKinds {
			if count := counts[kind.kind]; count == 1 {
				labels = append(labels, "1 "+kind.kind)
			} else if count > 1 {
				labels = append(labels, fmt.Sprintf("%d %s", count, kind.plural))
			}
		}
		verbText := activityVerb(counts, len(group.tools), active)
		var elapsed time.Duration
		if !group.startedAt.IsZero() {
			elapsed = time.Since(group.startedAt)
		}
		var prefix string
		switch {
		case active:
			prefix = " " + theme.PulseDot("accent", elapsed) + " " + theme.Bold(theme.Shimmer(verbText, elapsed))
		case group.expanded:
			prefix = " " + theme.FG("muted", "⌄") + " " + theme.FG("toolTitle", theme.Bold(verbText))
		case group.hovered:
			prefix = " " + theme.FG("toolTitle", "›") + " " + theme.FG("toolTitle", theme.Bold(verbText))
		default:
			prefix = " " + theme.Faint("›") + " " + theme.FG("muted", theme.Bold(verbText))
		}
		header := prefix + theme.FG("muted", " · "+strings.Join(labels, " · "))
		// While it works, the header says what it is doing now: a closed group keeps its height
		// instead of showing each running tool below and then swallowing it.
		if step := cmp.Or(group.stepLocked(), running); active && !group.expanded && step != "" {
			header += theme.FG("muted", " · "+theme.Italic(step))
		}
		right := ""
		if failed > 0 {
			right = theme.FG("error", fmt.Sprintf("%d failed", failed))
		} else if !active {
			if dur := formatDurationShort(totalDuration); dur != "" {
				right = theme.Faint(dur)
			}
		}
		row := []string{formatRowWithRight(header, right, width, 0)}
		if !group.hovered && !group.expanded && !active && failed == 0 {
			row = fade(row, &group.fadedFrom, &group.faded)
		}
		lines = row
		if !group.headed {
			lines = append([]string{""}, row...)
		}
	}
	// Under an expanded group header, every nested row sits behind a hairline left rail.
	grouped := len(group.tools) > 1
	indent := 0
	railPrefix := ""
	if grouped && width > groupIndent+2 {
		indent = groupIndent
		railPrefix = " " + theme.Rail() + "  "
	}
	nest := func(rendered []string) []string {
		if indent == 0 {
			return rendered
		}
		for index, line := range rendered {
			if line != "" {
				rendered[index] = railPrefix + line
			}
		}
		return rendered
	}
	tight := grouped || group.headed
	for _, item := range group.items {
		switch item := item.(type) {
		case *ToolExecutionComponent:
			item.mu.Lock()
			isErr := item.result != nil && item.result.IsError
			visible := !grouped || group.expanded || isErr
			item.mu.Unlock()
			if visible {
				var rendered []string
				if grouped {
					rendered = item.renderNested(max(1, width-indent))
				} else {
					rendered = item.Render(max(1, width-indent))
					if tight && len(rendered) > 0 && rendered[0] == "" {
						rendered = rendered[1:]
					}
				}
				start := len(lines)
				lines = append(lines, nest(rendered)...)
				group.rows = append(group.rows, toolActivityRow{tool: item, start: start, end: len(lines), indent: indent})
				tight = grouped
			}
		case *AssistantMessageComponent:
			if group.expanded && grouped {
				if rendered := item.renderGroupChild(max(1, width-indent)); len(rendered) > 0 {
					lines = append(lines, nest(rendered)...)
					tight = true
				}
			} else if !grouped {
				if rendered := item.renderUnfolded(width); len(rendered) > 0 {
					lines = append(lines, rendered...)
					tight = true
				}
			}
		}
	}
	return lines
}

// groupIndent sets a group's actions in under its header (" │  " = 4 cells).
const groupIndent = 4

// headerRow is the header's line in the group's render.
func (group *toolActivityGroup) headerRow() int {
	if group.headed {
		return 0
	}
	return 1
}

// stepLocked is the title of the latest reasoning folded into the group.
func (group *toolActivityGroup) stepLocked() string {
	for index := len(group.items) - 1; index >= 0; index-- {
		if note, ok := group.items[index].(*AssistantMessageComponent); ok {
			return note.reasoningTitle()
		}
	}
	return ""
}

func (group *toolActivityGroup) HandleMouse(event tui.MouseEvent) bool {
	group.mu.Lock()
	var target *ToolExecutionComponent
	local := event
	for _, row := range group.rows {
		if event.Row >= row.start && event.Row < row.end {
			target = row.tool
			local.Row -= row.start
			local.Column -= row.indent
			break
		}
	}
	changed := false
	previous := group.hover
	if event.Type == tui.MouseMove {
		hovered := len(group.tools) > 1 && event.Row == group.headerRow()
		changed = hovered != group.hovered
		group.hovered, group.hover = hovered, target
	} else if event.Type == tui.MouseRelease && (event.Button == 0 || event.Button == 3) && len(group.tools) > 1 && event.Row == group.headerRow() {
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
func (adapter themeAdapter) Colors() map[string]string {
	if adapter.value != nil {
		return adapter.value.Colors()
	}
	return theme.Current().Colors()
}
func (adapter themeAdapter) Appearance() string {
	if adapter.value != nil {
		return adapter.value.Appearance()
	}
	return theme.Current().Appearance()
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
