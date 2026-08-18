package plugins

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/tui"
)

// The configuration windows share one visual language: a framed overlay, an
// invisible grid of aligned columns, dim detail lines expanded under the
// selection, and key hints in the bottom border. Styling flows through the
// extensions.Theme adapter so the screens follow the active theme.

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
	frame := tui.NewFrame(title, footer,
		func(text string) string { return th.FG("border", text) },
		func(text string) string { return th.FG("dim", text) },
		child)
	frame.TitleStyle = func(text string) string { return th.Bold(th.FG("accent", text)) }
	return frame
}

func configWindowOptions() *extensions.CustomOptions {
	return &extensions.CustomOptions{
		Overlay: true,
		StaticOverlayOptions: &extensions.OverlayOptions{
			Width: "88%", MinWidth: 70, MaxHeight: "85%", Backdrop: true,
		},
	}
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
	}
	return descriptions[name]
}

// settingsObjectValue coerces a nested settings value to a plain map: cloned
// settings hold nested objects as the named config.Settings type.
func settingsObjectValue(value any) map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return typed
	case config.Settings:
		return typed
	}
	return nil
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
		if entries, err := parseExternalEntries(configured); err == nil && len(entries) > 0 {
			external := make([]string, 0, len(entries))
			for cli, entry := range entries {
				if entry.Enabled {
					external = append(external, cli)
				}
			}
			slices.Sort(external)
			return strings.Join(external, " · ")
		}
	case "permissions":
		sandboxMode := text("sandbox")
		if sandboxMode == "" && text("preset") == "workspace-write" {
			sandboxMode = "workspace-write"
		}
		mode := text("mode")
		if mode == "" {
			if text("preset") == "workspace-write" {
				mode = "enforce"
			} else {
				mode = "log"
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
		panel.frame = configFrame(th, "Plugins", "␣ toggle · type to filter · esc", pluginsPanelChild{panel})
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
					if err := toggleExternalCLI(settings, strings.TrimPrefix(value, "cli:")); err != nil {
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
		list.OnCancel = func() { done(nil) }
		return panel, nil
	}, configWindowOptions())
	state.mu.Lock()
	state.closed = true
	dirty := state.dirty
	state.mu.Unlock()
	if err != nil || !dirty {
		return err
	}
	return drainAndReload(ctx, command, settings)
}

func drainAndReload(ctx context.Context, command extensions.CommandContext, settings *config.SettingsManager) error {
	for _, settingsError := range settings.DrainErrors() {
		if strings.TrimSpace(settingsError.Error()) != "" {
			return settingsError
		}
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

func (child pluginsPanelChild) Render(width int) []string      { return child.panel.list.Render(width) }
func (child pluginsPanelChild) HandleInput(event tui.KeyEvent) { child.panel.list.HandleInput(event) }
func (child pluginsPanelChild) SetFocused(focused bool)        { child.panel.list.SetFocused(focused) }
func (child pluginsPanelChild) WantsMouseMotion() bool         { return child.panel.list.WantsMouseMotion() }
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
func (panel *pluginsPanel) SetFocused(focused bool)        { panel.frame.SetFocused(focused) }
func (panel *pluginsPanel) WantsMouseMotion() bool         { return panel.frame.WantsMouseMotion() }
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
	entries, err := externalSubagentEntries(settings)
	if err != nil {
		return []tui.GridRow{{Header: true, Cells: []string{th.FG("error", "  "+err.Error())}}}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	slices.Sort(names)
	rows := make([]tui.GridRow, 0, len(names)+len(knownCLIs))
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
	for _, cli := range detectCLIs(nil) {
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

// toggleExternalCLI flips one external CLI, preserving its command (the
// object form of plugins.subagents.external), or configures a detected CLI
// with its known invocation.
func toggleExternalCLI(settings *config.SettingsManager, name string) error {
	// The write below lands in the global scope; when project settings define
	// the plugin they shadow that whole object, so the toggle would silently
	// misfire — refuse with the remedy instead.
	if settings.ProjectDefinesPlugin("subagents") {
		return fmt.Errorf("plugins: subagents is configured in project settings — edit %s/settings.json", config.ConfigDirName)
	}
	entries, err := parseExternalEntries(settings.GlobalPluginSettings("subagents"))
	if err != nil {
		return err
	}
	if _, exists := entries[name]; !exists {
		command := ""
		for _, cli := range knownCLIs {
			if cli.Name == name {
				command = cli.Command
			}
		}
		if command == "" {
			return fmt.Errorf("plugins: unknown external CLI %q", name)
		}
		if entries == nil {
			entries = map[string]externalEntry{}
		}
		entries[name] = externalEntry{Command: command, Enabled: true}
	} else {
		entry := entries[name]
		entry.Enabled = !entry.Enabled
		entries[name] = entry
	}
	external := make(map[string]any, len(entries))
	for entryName, entry := range entries {
		if entry.Enabled {
			external[entryName] = entry.Command
		} else {
			external[entryName] = map[string]any{"command": entry.Command, "enabled": false}
		}
	}
	settings.SetPluginSetting("subagents", "external", external)
	return nil
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
	for _, settingsError := range settings.DrainErrors() {
		if strings.TrimSpace(settingsError.Error()) != "" {
			return settingsError
		}
	}
	return command.Reload(ctx)
}

// permissionRows lays the whole policy out as sections: the live policy
// values, the rules, and the recent decisions.
func permissionRows(policy *Policy, th extensions.Theme) []tui.GridRow {
	mode, fallback, rules, _, _ := policy.snapshot()
	header := func(text string) tui.GridRow {
		return tui.GridRow{Header: true, Cells: []string{th.FG("muted", th.Bold(text))}}
	}
	value := func(id, label, detail string) tui.GridRow {
		return tui.GridRow{Value: id, Cells: []string{th.FG("text", label), th.FG("accent", detail)}}
	}
	sandboxMode := "off"
	policy.mu.Lock()
	if policy.Sandbox != "" {
		sandboxMode = string(policy.Sandbox)
	}
	policy.mu.Unlock()
	rows := []tui.GridRow{
		header("Policy"),
		value("mode", "Mode", mode),
		value("sandbox", "Sandbox", sandboxMode),
		value("askFallback", "Ask fallback", string(fallback)),
		header(fmt.Sprintf("Rules (%d)", len(rules))),
	}
	if len(rules) == 0 {
		rows = append(rows, tui.GridRow{Value: "rules", Cells: []string{th.FG("dim", "(default allow)"), ""}})
	}
	for index, rule := range rules {
		rows = append(rows, tui.GridRow{
			Value: fmt.Sprintf("rule-%d", index+1),
			Cells: []string{th.FG("dim", fmt.Sprintf("%d.", index+1)), th.FG("text", formatRule(rule)), th.FG("accent", string(rule.Action))},
		})
	}
	rows = append(rows, header("Recent decisions"))
	decisions := policy.recent(10)
	if len(decisions) == 0 {
		rows = append(rows, tui.GridRow{Value: "decisions", Cells: []string{th.FG("dim", "(none)"), ""}})
	}
	for index, decision := range decisions {
		rows = append(rows, tui.GridRow{
			Value: fmt.Sprintf("decision-%d", index),
			Cells: []string{th.FG("text", decision.Tool), th.FG("muted", string(decision.Action)+" -> "+string(decision.Resolved)), th.FG("dim", decision.Resolution)},
		})
	}
	return rows
}

func permissionsWindow(ctx context.Context, command extensions.CommandContext, policy *Policy, settings *config.SettingsManager) error {
	_, _, err := command.UI().Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		list := tui.NewGridList(permissionRows(policy, th), 14, gridListTheme(th))
		toggle := func() {
			mode, _, _, _, _ := policy.snapshot()
			next := "enforce"
			if mode == "enforce" {
				next = "log"
			}
			policy.SetMode(next)
			if settings != nil {
				settings.SetPluginSetting("permissions", "mode", next)
			}
			list.SetRows(permissionRows(policy, th))
			host.Invalidate()
		}
		list.OnConfirm = func(value string) {
			if value == "mode" {
				toggle()
			}
		}
		list.OnKey = func(event tui.KeyEvent, _ string) bool {
			if event.Raw == "m" {
				toggle()
				return true
			}
			return false
		}
		list.OnCancel = func() { done(nil) }
		return configFrame(th, "Permissions", "m mode · esc", list), nil
	}, configWindowOptions())
	if err != nil {
		return err
	}
	if settings != nil {
		for _, settingsError := range settings.DrainErrors() {
			if strings.TrimSpace(settingsError.Error()) != "" {
				return settingsError
			}
		}
	}
	return nil
}
