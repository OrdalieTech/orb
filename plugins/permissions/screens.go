package permissions

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
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

func configWindowOptions() *extensions.CustomOptions {
	return &extensions.CustomOptions{
		Overlay: true,
		StaticOverlayOptions: &extensions.OverlayOptions{
			Width: "80%", MinWidth: 40, MaxHeight: "85%", Backdrop: true,
		},
	}
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
			switch mode {
			case "enforce":
				next = "auto"
			case "auto":
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
