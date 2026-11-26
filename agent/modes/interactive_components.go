package modes

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

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
	osc133ZoneStart = "\x1b]133;A\x07"
	osc133ZoneEnd   = "\x1b]133;B\x07"
	osc133ZoneFinal = "\x1b]133;C\x07"
)

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
// and extension paths reduced to their basename. Same-name collisions are
// already resolved (the first definition wins), so they are not shown. Full
// texts stay available where they originate (/reload output and stderr in
// print modes).
type startupWarnings struct{ lines []string }

func newStartupWarnings(diagnostics []StartupDiagnostic) *startupWarnings {
	lines := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		switch diagnostic.Kind {
		case StartupDiagnosticCollision:
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
	// skill is set for a skill invocation; onChange re-renders after a click toggle.
	skill    *skillMessageBody
	onChange func()
}

func NewUserMessageComponent(text string, mdTheme tui.MarkdownTheme, outputPad int, transformers []extensions.MarkdownTransformer) *UserMessageComponent {
	return newUserMessageBand(newUserMarkdown(text, mdTheme, newMarkdownTransform("user", false, transformers)), outputPad)
}

func newUserMarkdown(text string, mdTheme tui.MarkdownTheme, transform func(string, int) string) *tui.Markdown {
	return tui.NewMarkdown(text, 0, 0, mdTheme, &tui.DefaultTextStyle{
		Color: func(t string) string { return theme.FG("userMessageText", t) },
	}, &tui.MarkdownOptions{
		PreserveOrderedListMarkers: true,
		PreserveBackslashEscapes:   true,
		Transform:                  transform,
	})
}

func newUserMessageBand(body tui.Component, outputPad int) *UserMessageComponent {
	box := tui.NewBox(outputPad+1, 1, func(t string) string { return theme.BG("userMessageBg", t) })
	box.AddChild(body)
	return &UserMessageComponent{box: box}
}

// SetExpanded shows or hides the skill body; plain messages ignore it.
func (c *UserMessageComponent) SetExpanded(expanded bool) {
	if c.skill != nil {
		c.skill.setExpanded(expanded)
		c.box.Invalidate()
	}
}

// HandleMouse toggles the skill body on a click at or below the footer. Row 0
// is the leading blank line and row 1 the band's top padding.
func (c *UserMessageComponent) HandleMouse(event tui.MouseEvent) bool {
	if c.skill == nil || event.Type != tui.MouseRelease || event.Button != 0 && event.Button != 3 {
		return false
	}
	if !c.skill.toggleAt(event.Row - 2) {
		return false
	}
	c.box.Invalidate()
	if c.onChange != nil {
		c.onChange()
	}
	return true
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
// FooterComponent
// ─────────────────────────────────────────────────────────────

type statusHit struct {
	row, start, end int
	key             string
	action          func()
}

type FooterComponent struct {
	hitMu      sync.Mutex
	hits       []statusHit
	hover      string // key of the hit under the pointer
	hoverTimer *time.Timer
	hoverEpoch uint64
	// tooltip shows label above footer columns [start, end); "" hides it.
	tooltip            func(label string, start, end int)
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
func modelFooterForms(display engine.AgentDisplayState, meter string, providerCount int) []string {
	if !display.HasModel {
		return []string{"no-model"}
	}
	thinking := ""
	if meter != "" {
		thinking = " " + meter
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

// thinkingMeter is the apps' reasoning meter: one bar per level the model takes, rising, lit up
// to level ("off" never lights). Unlit bars are faint, so it reads without color too.
func thinkingMeter(levels []ai.ModelThinkingLevel, level ai.ModelThinkingLevel) string {
	bars := []rune("▁▂▃▄▅▆▇")
	now := slices.Index(levels, level)
	var meter strings.Builder
	for index, each := range levels {
		bar := string(bars[index*(len(bars)-1)/max(1, len(levels)-1)])
		if index <= now && each != ai.ModelThinkingOff {
			meter.WriteString(theme.FG("text", bar))
		} else {
			meter.WriteString("\x1b[2m" + bar + "\x1b[22m")
		}
	}
	return meter.String()
}

// thinkingLevels are the levels the session's model takes, without copying its transcript.
func (f *FooterComponent) thinkingLevels() []ai.ModelThinkingLevel {
	if session, ok := f.session.(interface {
		AvailableThinkingLevels() []ai.ModelThinkingLevel
	}); ok {
		return session.AvailableThinkingLevels()
	}
	return ai.SupportedThinkingLevels(f.session.State().Model)
}

func compactFooterLine(display engine.AgentDisplayState, meter string, context *harness.ContextUsage, statuses []string, width int, indicators ...string) string {
	tail := strings.Join(indicators, " ")
	if tail != "" && width > tui.VisibleWidth(tail)+1 {
		return compactFooterLine(display, meter, context, statuses, width-tui.VisibleWidth(tail)-1) + " " + tail
	}
	model := display.ModelID
	if !display.HasModel {
		model = "Choose model"
	}
	left := model
	if meter != "" {
		left += " " + meter
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
	meter := ""
	if display.Reasoning {
		meter = thinkingMeter(f.thinkingLevels(), display.ThinkingLevel)
	}
	statuses := f.provider.Statuses()
	keys := slices.Sorted(maps.Keys(statuses))
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, strings.Join(strings.Fields(statuses[key]), " "))
	}
	if !f.verbose {
		f.hitMu.Lock()
		hover := f.hover
		f.hitMu.Unlock()
		// One-glyph statuses are indicators pinned to the far right. The
		// hovered item brightens one step; its label floats above (tooltip).
		var texts, indicators []string
		for index, value := range values {
			if keys[index] == hover {
				value = theme.FG("muted", value)
			}
			if tui.VisibleWidth(value) == 1 {
				indicators = append(indicators, value)
			} else {
				texts = append(texts, value)
			}
		}
		if cwd := f.cwd(); cwd != "" {
			path := shortenSessionPath(cwd)
			model := display.ModelID
			if !display.HasModel {
				model = "Choose model"
			}
			if meter != "" {
				model += " " + meter
			}
			available := width - tui.VisibleWidth(model) - tui.VisibleWidth(strings.Join(texts, " · ")) - 2
			if len(texts) > 0 {
				available -= 3
			}
			if len(indicators) > 0 {
				available -= tui.VisibleWidth(strings.Join(indicators, " ")) + 1
			}
			// The cwd is a reminder, not an address: past 32 cells only its
			// last segment is shown.
			if tui.VisibleWidth(path) > min(available, 32) {
				path = "…/" + filepath.Base(cwd)
			}
			texts = append(texts, path)
		}
		line := compactFooterLine(display, meter, stats.ContextUsage, texts, width, indicators...)
		f.recordStatusHits(line, 0, keys, values)
		f.recordThinkingHit(line, 0, display, meter)
		// A colored status ends in a foreground reset; restore dim after it.
		return []string{theme.FG("dim", strings.ReplaceAll(line, "\x1b[39m", theme.FGANSI("dim")))}
	}

	contextSummary := footerContextSummary(display, stats.ContextUsage)
	modelForms := modelFooterForms(display, meter, providerCount)
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

	f.recordThinkingHit(lines[1], 1, display, meter)
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
		if tui.VisibleWidth(value) == 1 {
			// Indicators sit at the far right; a same glyph may appear earlier.
			start = strings.LastIndex(text, value)
		}
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
		hits = append(hits, statusHit{row: row, start: column, end: min(tui.VisibleWidth(text)+1, column+tui.VisibleWidth(value)), key: key, action: action})
	}
	f.hitMu.Lock()
	f.hits = append(f.hits, hits...)
	f.hitMu.Unlock()
}

func (f *FooterComponent) recordThinkingHit(text string, row int, display engine.AgentDisplayState, meter string) {
	provider, ok := f.provider.(interface{ StatusAction(string) func() })
	if meter == "" || !ok {
		return
	}
	text, meter = tui.StripANSI(text), tui.StripANSI(meter)
	label := display.ModelID + " " + meter
	start := strings.Index(text, label)
	action := provider.StatusAction("orb:thinking")
	if start < 0 || action == nil {
		return
	}
	end := tui.VisibleWidth(text[:start]) + tui.VisibleWidth(label) + 1
	f.hitMu.Lock()
	f.hits = append(f.hits, statusHit{row: row, start: end - tui.VisibleWidth(meter), end: end, key: "orb:thinking", action: action})
	f.hitMu.Unlock()
}

func (f *FooterComponent) statusLabel(key string) string {
	if provider, ok := f.provider.(interface{ StatusLabel(string) string }); ok {
		return provider.StatusLabel(key)
	}
	return ""
}

// hitAtLocked is the status hit under the pointer, or the zero hit.
func (f *FooterComponent) hitAtLocked(event tui.MouseEvent) statusHit {
	for _, hit := range f.hits {
		if event.Row == hit.row && event.Column >= hit.start && event.Column < hit.end {
			return hit
		}
	}
	return statusHit{}
}

func (f *FooterComponent) HandleMouse(event tui.MouseEvent) bool {
	if event.Type == tui.MouseMove {
		f.hitMu.Lock()
		defer f.hitMu.Unlock()
		hovered := f.hitAtLocked(event)
		changed := hovered.key != f.hover
		if !changed {
			return false
		}
		f.hover = hovered.key
		f.hoverEpoch++
		if f.hoverTimer != nil {
			f.hoverTimer.Stop()
			f.hoverTimer = nil
		}
		if f.tooltip != nil {
			label := ""
			if hovered.key != "" {
				label = f.statusLabel(hovered.key)
			}
			f.tooltip(label, hovered.start, hovered.end)
			if label != "" {
				epoch := f.hoverEpoch
				// Terminals send no mouse-leave when the pointer exits the pane.
				f.hoverTimer = time.AfterFunc(2*time.Second, func() {
					f.hitMu.Lock()
					defer f.hitMu.Unlock()
					if epoch == f.hoverEpoch {
						f.hover, f.hoverTimer = "", nil
						f.tooltip("", 0, 0)
					}
				})
			}
		}
		return true
	}
	if event.Type != tui.MousePress || event.Button != 0 {
		return false
	}
	f.hitMu.Lock()
	clicked := f.hitAtLocked(event)
	tooltip := f.tooltip
	f.hitMu.Unlock()
	if clicked.action == nil {
		return false
	}
	if event.Clicks < 2 {
		clicked.action()
		// A click can change what the label says (the thinking level).
		if tooltip != nil {
			tooltip(f.statusLabel(clicked.key), clicked.start, clicked.end)
		}
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
// Summary messages (compaction and branch)
// ─────────────────────────────────────────────────────────────

type SummaryMessageComponent struct {
	box       *tui.Box
	expanded  bool
	label     string
	header    string // expanded markdown heading
	collapsed string // collapsed text before the expand hint
	summary   string
	mdTheme   tui.MarkdownTheme
}

func NewCompactionSummaryMessage(summary string, tokensBefore int64, mdTheme tui.MarkdownTheme) *SummaryMessageComponent {
	tokens := formatInteger(tokensBefore)
	return newSummaryMessage("[compaction]", "Compacted from "+tokens+" tokens", "Compacted from "+tokens+" tokens", summary, mdTheme)
}

func NewBranchSummaryMessage(summary string, mdTheme tui.MarkdownTheme) *SummaryMessageComponent {
	return newSummaryMessage("[branch]", "Branch Summary", "Branch summary", summary, mdTheme)
}

func newSummaryMessage(label, header, collapsed, summary string, mdTheme tui.MarkdownTheme) *SummaryMessageComponent {
	c := &SummaryMessageComponent{
		box:   tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) }),
		label: label, header: header, collapsed: collapsed, summary: summary, mdTheme: mdTheme,
	}
	c.updateDisplay()
	return c
}

func (c *SummaryMessageComponent) SetExpanded(expanded bool) {
	c.expanded = expanded
	c.updateDisplay()
}

func (c *SummaryMessageComponent) updateDisplay() {
	c.box.Clear()
	c.box.AddChild(tui.NewText(theme.FG("customMessageLabel", theme.Bold(c.label)), 0, 0, nil))
	c.box.AddChild(tui.NewSpacer(1))
	if c.expanded {
		c.box.AddChild(tui.NewMarkdown("**"+c.header+"**\n\n"+c.summary, 0, 0, c.mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
		return
	}
	c.box.AddChild(tui.NewText(
		theme.FG("customMessageText", c.collapsed+" (")+
			theme.FG("dim", KeyText("app.tools.expand"))+
			theme.FG("customMessageText", " to expand)"),
		0, 0, nil,
	))
}

func (c *SummaryMessageComponent) Invalidate() { c.box.Invalidate() }
func (c *SummaryMessageComponent) Render(width int) []string {
	return renderBand(c.box, width, "")
}

// ─────────────────────────────────────────────────────────────
// CustomMessageComponent
// ─────────────────────────────────────────────────────────────

type CustomMessageComponent struct{ container *tui.Container }

func NewCustomMessageComponent(customType string, content any, mdTheme tui.MarkdownTheme) *CustomMessageComponent {
	container := &tui.Container{}
	container.AddChild(tui.NewSpacer(1))
	box := tui.NewBox(chatBandPad, 1, func(t string) string { return theme.BG("customMessageBg", t) })
	container.AddChild(&chatBand{inner: box})
	label := theme.FG("customMessageLabel", theme.Bold(fmt.Sprintf("[%s]", customType)))
	box.AddChild(tui.NewText(label, 0, 0, nil))
	box.AddChild(tui.NewSpacer(1))
	if text := customMessageText(content); text != "" {
		box.AddChild(tui.NewMarkdown(text, 0, 0, mdTheme,
			&tui.DefaultTextStyle{Color: func(t string) string { return theme.FG("customMessageText", t) }}, nil))
	}
	return &CustomMessageComponent{container: container}
}

// customMessageText joins the text blocks of block-array content, as the HTML
// export does; any other content prints as is.
func customMessageText(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case ai.UserContent:
		if typed.Text != nil {
			return *typed.Text
		}
		return customBlocksText(typed.Blocks)
	case ai.UserContentBlocks:
		return customBlocksText(typed)
	case []ai.UserContentBlock:
		return customBlocksText(typed)
	case []*ai.TextContent:
		var texts []string
		for _, block := range typed {
			if block != nil {
				texts = append(texts, block.Text)
			}
		}
		return strings.Join(texts, "\n")
	case []any:
		var texts []string
		for _, block := range typed {
			switch value := block.(type) {
			case *ai.TextContent:
				if value != nil {
					texts = append(texts, value.Text)
				}
			case ai.TextContent:
				texts = append(texts, value.Text)
			case map[string]any:
				if value["type"] == "text" {
					if text, ok := value["text"].(string); ok {
						texts = append(texts, text)
					}
				}
			}
		}
		return strings.Join(texts, "\n")
	default:
		return fmt.Sprintf("%v", content)
	}
}

func customBlocksText(blocks []ai.UserContentBlock) string {
	var texts []string
	for _, block := range blocks {
		if text, ok := block.(*ai.TextContent); ok && text != nil {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func (c *CustomMessageComponent) Invalidate()               { c.container.Invalidate() }
func (c *CustomMessageComponent) Render(width int) []string { return c.container.Render(width) }
