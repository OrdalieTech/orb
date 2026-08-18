package plugins

import (
	"context"
	"fmt"
	"slices"
	"strings"

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

// pluginGridRows builds one row per bundled plugin with a structured-config
// summary as the expanded detail, so the window explains what each plugin is
// configured to do, not just whether it is on.
func pluginGridRows(settings *config.SettingsManager, th extensions.Theme) []tui.GridRow {
	enabled := settings.GetPlugins()
	rows := make([]tui.GridRow, 0, len(names))
	for _, name := range names {
		// Disabled plugins recede: the whole row goes dim, not just the pill.
		nameStyle, descriptionStyle := "text", "muted"
		if !enabled[name] {
			nameStyle, descriptionStyle = "dim", "dim"
		}
		rows = append(rows, tui.GridRow{
			Value:  name,
			Cells:  []string{statePill(th, enabled[name]), th.FG(nameStyle, name), th.FG(descriptionStyle, descriptions[name])},
			Detail: pluginDetail(name, settings),
		})
	}
	return rows
}

func pluginDetail(name string, settings *config.SettingsManager) []string {
	configured := settings.GetPluginSettings(name)
	text := func(key string) string {
		value, _ := configured[key].(string)
		return value
	}
	switch name {
	case "subagents":
		var external []string
		if raw, ok := configured["external"].(map[string]any); ok {
			for cli := range raw {
				external = append(external, cli)
			}
			slices.Sort(external)
		}
		detail := "external CLIs: none — add plugins.subagents.external {name: command}"
		if len(external) > 0 {
			detail = "external CLIs: " + strings.Join(external, ", ")
		}
		return []string{detail, "children: scout · worker · reviewer + externals — task on stdin, 10 min cap"}
	case "permissions":
		parts := []string{}
		if preset := text("preset"); preset != "" {
			parts = append(parts, "preset "+preset)
		}
		sandboxMode := text("sandbox")
		if sandboxMode == "" && text("preset") == "workspace-write" {
			sandboxMode = "workspace-write"
		}
		if sandboxMode != "" {
			parts = append(parts, "sandbox "+sandboxMode)
		} else {
			parts = append(parts, "sandbox off")
		}
		mode := text("mode")
		if mode == "" {
			if text("preset") == "workspace-write" {
				mode = "enforce"
			} else {
				mode = "log"
			}
		}
		parts = append(parts, "mode "+mode)
		if rules, ok := configured["rules"].([]any); ok && len(rules) > 0 {
			parts = append(parts, fmt.Sprintf("%d rules", len(rules)))
		}
		return []string{strings.Join(parts, " · "), "presets: workspace-write · danger-full-access — /permissions for details"}
	case "memory":
		return []string{"remember / recall / replace / forget — bounded store under the agent dir"}
	case "tasks":
		return []string{"todo tool + live task widget; /tasks lists everything"}
	case "websearch":
		return []string{"web_search and web_fetch tools with readable extraction"}
	}
	return nil
}

// pluginsWindow is the /plugins TUI: toggle plugins on the grid, then reload
// once on close if anything changed.
func pluginsWindow(ctx context.Context, command extensions.CommandContext, settings *config.SettingsManager) error {
	dirty := false
	_, _, err := command.UI().Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		list := tui.NewGridList(pluginGridRows(settings, th), 12, gridListTheme(th))
		list.DetailHeight = 3
		list.OnConfirm = func(name string) {
			if name == "" {
				return
			}
			settings.SetPluginEnabled(name, !settings.GetPlugins()[name])
			dirty = true
			list.SetRows(pluginGridRows(settings, th))
			host.Invalidate()
		}
		list.OnCancel = func() { done(nil) }
		return configFrame(th, "Plugins", "␣ toggle · esc close · docs/plugins.md", list), nil
	}, configWindowOptions())
	if err != nil || !dirty {
		return err
	}
	for _, settingsError := range settings.DrainErrors() {
		if strings.TrimSpace(settingsError.Error()) != "" {
			return settingsError
		}
	}
	return command.Reload(ctx)
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
	value := func(id, label, detail string, note ...string) tui.GridRow {
		return tui.GridRow{Value: id, Cells: []string{th.FG("text", label), th.FG("accent", detail)}, Detail: note}
	}
	sandboxMode := "off (danger-full-access)"
	policy.mu.Lock()
	if policy.Sandbox != "" {
		sandboxMode = string(policy.Sandbox)
	}
	policy.mu.Unlock()
	modeNote := "enforce: deny rules block, ask rules prompt"
	if mode != "enforce" {
		modeNote = "log: decisions are recorded as would-allow/would-deny, nothing blocks (guards still deny)"
	}
	rows := []tui.GridRow{
		header("Policy"),
		value("mode", "Mode", mode, modeNote, bashCaveat),
		value("sandbox", "Sandbox", sandboxMode, "bash filesystem containment — plugins.permissions.sandbox"),
		value("askFallback", "Ask fallback", string(fallback), "resolution when no UI can prompt"),
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
		list.DetailHeight = 2
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
		return configFrame(th, "Permissions", "m toggle mode · esc close", list), nil
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
