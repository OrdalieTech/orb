package assembly

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/plugins/subagents"
	"github.com/OrdalieTech/orb/tui"
)

func gridListTheme(th extensions.Theme) tui.GridListTheme {
	return tui.GridListTheme{
		SelectedBg: func(text string) string { return th.BG("selectedBg", text) },
		Detail:     func(text string) string { return th.FG("dim", text) },
		ScrollInfo: func(text string) string { return th.FG("muted", text) },
		Query:      func(text string) string { return th.FG("text", text) },
		Cursor:     th.FG("accent", "› "),
	}
}

func configFrame(th extensions.Theme, title, footer string, child tui.Component) *tui.Frame {
	return tui.NewPanel(title, footer,
		func(text string) string { return th.Bold(th.FG("text", text)) },
		func(text string) string { return th.FG("dim", text) },
		func() string { return th.BGANSI("toolPendingBg") }, child)
}

func statePill(th extensions.Theme, on bool) string {
	if on {
		return th.FG("success", "● on ")
	}
	return th.FG("dim", "○ off")
}

// pluginGridRows lays each plugin on the invisible grid: state pill, name,
// the one-line description, and — when the plugin actually carries
// configuration — its values in a last column.
func pluginGridRows(settings *config.SettingsManager, th extensions.Theme) []tui.GridRow {
	enabled := settings.GetPlugins()
	rows := make([]tui.GridRow, 0, len(names))
	for _, name := range names {
		if Paged(name) {
			continue
		}
		// Disabled plugins recede: the whole row goes dim, not just the pill.
		nameStyle, descriptionStyle, valueStyle := "text", "muted", "accent"
		if !enabled[name] {
			nameStyle, descriptionStyle, valueStyle = "dim", "dim", "dim"
		}
		cells := []string{statePill(th, enabled[name]), th.FG(nameStyle, name), th.FG(descriptionStyle, shortDescription(name))}
		if value := pluginConfigSummary(name, settings); value != "" {
			cells = append(cells, th.FG(valueStyle, value))
		}
		rows = append(rows, tui.GridRow{Value: name, Cells: cells})
	}
	return rows
}

// shortDescription is the editorial cut of the CLI description: the essence,
// short enough to sit as a grid column.
func shortDescription(name string) string {
	switch name {
	case "questions":
		return "choices and custom answers"
	case "tasks":
		return "task list and todo tool"
	case "websearch":
		return "web search and page fetching"
	case "subagents":
		return "child agents and external CLIs"
	case "permissions":
		return "rules, audit, and sandbox"
	case "memory":
		return "persistent memory tools"
	case "memtree":
		return "zoomable session history"
	}
	return descriptions[name]
}

// pluginConfigSummary compresses a plugin's structured settings into one
// data-only cell: configured external CLI names, the active sandbox and mode.
func pluginConfigSummary(name string, settings *config.SettingsManager) string {
	configured := settings.GetPluginSettings(name)
	if len(configured) == 0 {
		return ""
	}
	text := func(key string) string {
		value, _ := configured[key].(string)
		return value
	}
	switch name {
	case "subagents":
		if entries, err := subagents.ParseExternalEntries(configured); err == nil && len(entries) > 0 {
			external := make([]string, 0, len(entries))
			for cli, entry := range entries {
				if entry.Enabled {
					external = append(external, cli)
				}
			}
			slices.Sort(external)
			return strings.Join(external, " · ")
		}
	case "memtree":
		return text("mode")
	case "permissions":
		sandboxMode := text("sandbox")
		if sandboxMode == "" && text("preset") == "workspace-write" {
			sandboxMode = "workspace-write"
		}
		mode := text("mode")
		if mode == "" {
			if text("preset") == "danger-full-access" {
				mode = "log"
			} else {
				mode = "auto"
			}
		}
		parts := []string{}
		if sandboxMode != "" && sandboxMode != "danger-full-access" {
			parts = append(parts, sandboxMode)
		}
		parts = append(parts, mode)
		if rules, ok := configured["rules"].([]any); ok && len(rules) > 0 {
			parts = append(parts, fmt.Sprintf("%d rules", len(rules)))
		}
		return strings.Join(parts, " · ")
	}
	return ""
}

// pluginsWindowState is shared between the UI goroutines and the install
// goroutine; every access goes through the mutex, and the panel rebuilds its
// rows when revision moves.
type pluginsWindowState struct {
	mu         sync.Mutex
	status     string
	dirty      bool
	installing bool
	closed     bool
	revision   int
}

// pluginsWindow is the /plugins TUI: toggle plugins and external CLIs on one
// searchable grid, install packages, then reload once on close if anything
// changed.
func pluginsWindow(ctx context.Context, command extensions.CommandContext, cwd, agentDir string, settings *config.SettingsManager) error {
	state := &pluginsWindowState{}
	_, _, err := command.UI().Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		list := tui.NewGridList(nil, 16, gridListTheme(th))
		list.Searchable = true
		panel := &pluginsPanel{state: state, theme: th, settings: settings, list: list, installable: cwd != ""}
		panel.frame = configFrame(th, "Plugins", "␣ toggle · ⇥ mode · type to filter · esc", pluginsPanelChild{panel})
		bump := func(mutate func()) {
			state.mu.Lock()
			mutate()
			state.revision++
			state.mu.Unlock()
			host.Invalidate()
		}
		list.OnConfirm = func(value string) {
			switch {
			case value == "":
			case value == "install":
				go runPackageInstall(ctx, command, state, host, cwd, agentDir, settings)
			case strings.HasPrefix(value, "cli:"):
				bump(func() {
					if err := subagents.ToggleExternalCLI(settings, strings.TrimPrefix(value, "cli:")); err != nil {
						state.status = err.Error()
					} else {
						state.dirty = true
					}
				})
			default:
				settings.SetPluginEnabled(value, !settings.GetPlugins()[value])
				bump(func() { state.dirty = true })
			}
		}
		// Tab moves a plugin's choice (memtree's mode, permissions') to its next value.
		list.OnKey = func(event tui.KeyEvent, value string) bool {
			if event.Raw != "\t" || len(Choices(value)) == 0 {
				return false
			}
			c := Choices(value)[0]
			current, _ := settings.GetPluginSettings(value)[c.Key].(string)
			settings.SetPluginSetting(value, c.Key, c.Values[(slices.Index(c.Values, cmp.Or(current, c.Values[0]))+1)%len(c.Values)])
			bump(func() { state.dirty = true })
			return true
		}
		list.OnCancel = func() { done(nil) }
		return panel, nil
	}, extensions.ModalOptions())
	state.mu.Lock()
	state.closed = true
	dirty := state.dirty
	state.mu.Unlock()
	if err != nil || !dirty {
		return err
	}
	return drainAndReload(ctx, command, settings)
}

// drainAndReload applies the change once the running turn ends: a reload builds the session again,
// and a turn running then would be cut short.
func drainAndReload(ctx context.Context, command extensions.CommandContext, settings *config.SettingsManager) error {
	for _, settingsError := range settings.DrainErrors() {
		if strings.TrimSpace(settingsError.Error()) != "" {
			return settingsError
		}
	}
	if err := command.WaitForIdle(ctx); err != nil {
		return err
	}
	return command.Reload(ctx)
}

// runPackageInstall asks for a source, downloads it, and persists it. It only
// touches the shared state under its lock and repaints through Invalidate —
// never the list directly — so it cannot race the render or input goroutines.
// If the window closed while installing, it applies the reload itself.
func runPackageInstall(ctx context.Context, command extensions.CommandContext, state *pluginsWindowState, host extensions.UIHost, cwd, agentDir string, settings *config.SettingsManager) {
	state.mu.Lock()
	if state.installing || state.closed {
		state.mu.Unlock()
		return
	}
	state.installing = true
	state.mu.Unlock()
	defer func() {
		state.mu.Lock()
		state.installing = false
		state.mu.Unlock()
	}()
	source, ok, err := command.UI().Input(ctx, "Install package — npm:@scope/pkg, git:host/repo, or a path", nil, nil)
	source = strings.TrimSpace(source)
	if err != nil || !ok || source == "" {
		return
	}
	setStatus := func(text string) {
		state.mu.Lock()
		state.status = text
		state.revision++
		state.mu.Unlock()
		host.Invalidate()
	}
	setStatus("installing " + source + "…")
	installErr := agent.NewPackageManager(agent.PackageManagerOptions{
		CWD: cwd, AgentDir: agentDir, Settings: settings,
	}).InstallAndPersist(source, false)
	if installErr != nil {
		setStatus("install failed: " + installErr.Error())
		return
	}
	state.mu.Lock()
	closed := state.closed
	if !closed {
		state.dirty = true
	}
	state.status = "installed " + source
	state.revision++
	state.mu.Unlock()
	host.Invalidate()
	if closed {
		_ = drainAndReload(ctx, command, settings)
	}
}

// pluginsPanel rebuilds the grid at render time whenever the shared state
// moved — the same single-writer pattern as the MCP window.
type pluginsPanel struct {
	state       *pluginsWindowState
	theme       extensions.Theme
	settings    *config.SettingsManager
	list        *tui.GridList
	frame       *tui.Frame
	installable bool
	revision    int
	initialized bool
}

// pluginsPanelChild forwards the full component contract to the list so the
// frame's input/focus/mouse plumbing reaches it.
type pluginsPanelChild struct{ panel *pluginsPanel }

func (child pluginsPanelChild) Render(width int) []string { return child.panel.list.Render(width) }

func (child pluginsPanelChild) HandleInput(event tui.KeyEvent) { child.panel.list.HandleInput(event) }

func (child pluginsPanelChild) SetFocused(focused bool) { child.panel.list.SetFocused(focused) }

func (child pluginsPanelChild) WantsMouseMotion() bool { return child.panel.list.WantsMouseMotion() }

func (child pluginsPanelChild) HandleMouse(event tui.MouseEvent) bool {
	return child.panel.list.HandleMouse(event)
}

func (panel *pluginsPanel) Render(width int) []string {
	panel.state.mu.Lock()
	revision, status := panel.state.revision, panel.state.status
	panel.state.mu.Unlock()
	if !panel.initialized || revision != panel.revision {
		panel.initialized, panel.revision = true, revision
		panel.list.SetRows(pluginsWindowRows(panel.settings, panel.theme, panel.installable, status))
	}
	return panel.frame.Render(width)
}

func (panel *pluginsPanel) HandleInput(event tui.KeyEvent) { panel.frame.HandleInput(event) }

func (panel *pluginsPanel) SetFocused(focused bool) { panel.frame.SetFocused(focused) }

func (panel *pluginsPanel) WantsMouseMotion() bool { return panel.frame.WantsMouseMotion() }

func (panel *pluginsPanel) HandleMouse(event tui.MouseEvent) bool {
	return panel.frame.HandleMouse(event)
}

var _ tui.Focusable = (*pluginsPanel)(nil)

// pluginsWindowRows lays out the three sections: bundled plugins, external
// CLIs (configured and PATH-detected), and package actions.
func pluginsWindowRows(settings *config.SettingsManager, th extensions.Theme, installable bool, status string) []tui.GridRow {
	rows := pluginGridRows(settings, th)
	header := func(text string) tui.GridRow {
		return tui.GridRow{Header: true, Cells: []string{th.FG("muted", th.Bold(text))}}
	}
	rows = append(rows, tui.GridRow{Header: true, Cells: []string{""}}, header("External CLIs — sub-agents"))
	rows = append(rows, externalCLIRows(settings, th)...)
	if installable {
		rows = append(rows, tui.GridRow{Header: true, Cells: []string{""}}, header("Packages"))
		rows = append(rows, tui.GridRow{
			Value: "install",
			Cells: []string{th.FG("accent", "＋"), th.FG("text", "install package…"), th.FG("dim", "npm: · git: · path")},
		})
		if status != "" {
			style := "muted"
			if strings.HasPrefix(status, "install failed") {
				style = "error"
			}
			rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG(style, "  "+status)}})
		}
	}
	return rows
}

// externalCLIRows lists the configured external CLIs plus the known agent
// CLIs detected on PATH but not configured yet — one space press away.
func externalCLIRows(settings *config.SettingsManager, th extensions.Theme) []tui.GridRow {
	entries, err := subagents.ExternalEntries(settings)
	if err != nil {
		return []tui.GridRow{{Header: true, Cells: []string{th.FG("error", "  "+err.Error())}}}
	}
	names := slices.Sorted(maps.Keys(entries))
	rows := make([]tui.GridRow, 0, len(names)+len(subagents.KnownCLIs()))
	for _, name := range names {
		entry := entries[name]
		nameStyle, commandStyle := "text", "muted"
		if !entry.Enabled {
			nameStyle, commandStyle = "dim", "dim"
		}
		rows = append(rows, tui.GridRow{
			Value: "cli:" + name,
			Cells: []string{statePill(th, entry.Enabled), th.FG(nameStyle, name), th.FG(commandStyle, entry.Command)},
		})
	}
	for _, cli := range subagents.DetectCLIs(nil) {
		if _, configured := entries[cli.Name]; configured {
			continue
		}
		rows = append(rows, tui.GridRow{
			Value: "cli:" + cli.Name,
			Cells: []string{th.FG("dim", "○ off"), th.FG("dim", cli.Name), th.FG("dim", cli.Command+" — detected")},
		})
	}
	if len(rows) == 0 {
		rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG("dim", "  none configured or detected")}})
	}
	return rows
}

// legacyPluginsSelect keeps the plain Select loop for UIs that cannot host
// custom components (RPC-driven frontends).
func legacyPluginsSelect(ctx context.Context, command extensions.CommandContext, settings *config.SettingsManager) error {
	dirty := false
	for {
		enabled := settings.GetPlugins()
		choices := make([]string, 0, len(names)+1)
		choiceNames := make(map[string]string, len(names))
		for _, name := range names {
			if name == "bridge" || name == "bridge-agent-calls" || name == "provider-usage" {
				continue
			}
			mark := " "
			if enabled[name] {
				mark = "x"
			}
			label := fmt.Sprintf("[%s] %s — %s", mark, name, descriptions[name])
			choices = append(choices, label)
			choiceNames[label] = name
		}
		choices = append(choices, "Done")
		selected, ok, err := command.UI().Select(ctx, "Bundled plugins", choices, nil)
		if err != nil {
			return err
		}
		if !ok || selected == "Done" {
			break
		}
		name := choiceNames[selected]
		if name == "" {
			continue
		}
		settings.SetPluginEnabled(name, !enabled[name])
		dirty = true
	}
	if !dirty {
		return nil
	}
	return drainAndReload(ctx, command, settings)
}

// text is a run of text in the transcript, cut to one line when asked: how the terminal draws
// the results of plugins that leave their drawing to the host (tasks, questions).
func text(s string, oneLine bool) extensions.Component {
	if oneLine {
		return tui.NewTruncatedText(s, 0, 0)
	}
	return tui.NewText(s, 0, 0, nil)
}

// taskWidget is the task list above the editor: its summary, opened onto the list by a click.
type taskWidget struct {
	mu            sync.Mutex
	summary, list string
	expanded      bool
	host          extensions.UIHost
	theme         interface {
		FG(color, text string) string
	}
	cachedWidth    int
	cachedExpanded bool
	cachedLines    []string
	cacheRevision  uint64
}

func newTaskWidget(summary, list string, host extensions.UIHost, theme extensions.Theme) extensions.Component {
	return &taskWidget{summary: summary, list: list, host: host, theme: theme}
}

func (widget *taskWidget) Invalidate() {
	widget.mu.Lock()
	widget.cachedLines = nil
	widget.cacheRevision++
	widget.mu.Unlock()
}

func (widget *taskWidget) Render(width int) []string {
	if width <= 0 {
		return nil
	}
	widget.mu.Lock()
	expanded, revision := widget.expanded, widget.cacheRevision
	if widget.cachedLines != nil && widget.cachedWidth == width && widget.cachedExpanded == expanded {
		lines := append([]string(nil), widget.cachedLines...)
		widget.mu.Unlock()
		return lines
	}
	widget.mu.Unlock()
	marker := "▸ "
	if expanded {
		marker = "▾ "
	}
	headerText := marker + widget.summary
	if expanded && widget.theme != nil {
		headerText = widget.theme.FG("dim", headerText)
	}
	headerPadding := min(1, max(0, (width-1)/2))
	lines := tui.NewTruncatedText(headerText, headerPadding, 0).Render(width)
	if expanded {
		taskLines := strings.Split(widget.list, "\n")
		if widget.theme != nil {
			for index := range taskLines {
				taskLines[index] = widget.theme.FG("dim", taskLines[index])
			}
		}
		listPadding := min(4, max(0, (width-1)/2))
		lines = append(lines, tui.NewText(strings.Join(taskLines, "\n"), listPadding, 0, nil).Render(width)...)
	}
	widget.mu.Lock()
	if widget.expanded == expanded && widget.cacheRevision == revision {
		widget.cachedWidth, widget.cachedExpanded = width, expanded
		widget.cachedLines = append(widget.cachedLines[:0], lines...)
	}
	widget.mu.Unlock()
	return lines
}

func (widget *taskWidget) HandleMouse(event tui.MouseEvent) bool {
	if event.Type != tui.MousePress && event.Type != tui.MouseRelease || event.Button != 0 {
		return false
	}
	// A click toggles on release; a drag from the press selects text instead.
	if event.Type == tui.MousePress || event.Clicks > 1 {
		return true
	}
	widget.mu.Lock()
	widget.expanded = !widget.expanded
	widget.cachedLines = nil
	widget.cacheRevision++
	widget.mu.Unlock()
	if widget.host != nil {
		widget.host.Invalidate()
	}
	return true
}
