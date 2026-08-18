package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/tui"
)

// statusWindow is the /mcp TUI: one grid row per configured server with its
// live state, target, and registered tools, plus in-place reconnection. Rows
// are rebuilt whenever the live status fingerprint changes, so a reconnect
// started with r updates the window as the connection progresses.
func (manager *Manager) statusWindow(ctx context.Context, command extensions.CommandContext) error {
	_, _, err := command.UI().Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		list := tui.NewGridList(nil, 12, tui.GridListTheme{
			SelectedBg: func(text string) string { return th.BG("selectedBg", text) },
			Detail:     func(text string) string { return th.FG("dim", text) },
			ScrollInfo: func(text string) string { return th.FG("muted", text) },
			Cursor:     th.FG("accent", "› "),
		})
		list.OnCancel = func() { done(nil) }
		reconnect := func(name string) {
			go func() {
				_ = manager.Reconnect(context.Background(), name)
				host.Invalidate()
			}()
			host.Invalidate()
		}
		list.OnKey = func(event tui.KeyEvent, value string) bool {
			switch event.Raw {
			case "r":
				if value != "" {
					reconnect(value)
				}
				return true
			case "R":
				reconnect("")
				return true
			}
			return false
		}
		panel := &mcpPanel{manager: manager, theme: th, list: list}
		panel.frame = tui.NewFrame("MCP servers", "r reconnect · R reconnect all · esc close · orb mcp --help",
			func(text string) string { return th.FG("borderMuted", text) },
			func(text string) string { return th.FG("dim", text) },
			panelChild{panel})
		return panel, nil
	}, &extensions.CustomOptions{
		Overlay:              true,
		StaticOverlayOptions: &extensions.OverlayOptions{Width: "75%", MinWidth: 64, MaxHeight: "75%"},
	})
	return err
}

// mcpPanel refreshes the grid from the manager on render; Status() is a pure
// in-memory snapshot, so polling it every frame is free.
type mcpPanel struct {
	manager     *Manager
	theme       extensions.Theme
	list        *tui.GridList
	frame       *tui.Frame
	fingerprint string
}

// panelChild lets the frame render the list after the panel has refreshed it.
type panelChild struct{ panel *mcpPanel }

func (child panelChild) Render(width int) []string { return child.panel.list.Render(width) }

func (panel *mcpPanel) Render(width int) []string {
	status := panel.manager.Status()
	fingerprint := fmt.Sprintf("%v", status)
	if fingerprint != panel.fingerprint {
		panel.fingerprint = fingerprint
		panel.list.SetRows(panel.rows(status))
	}
	return panel.frame.Render(width)
}

func (panel *mcpPanel) rows(status []ServerStatus) []tui.GridRow {
	th := panel.theme
	rows := make([]tui.GridRow, 0, len(status))
	for _, server := range status {
		stateColor := map[ServerState]string{
			ServerConnected: "success", ServerConnecting: "warning", ServerError: "error", ServerStopped: "dim",
		}[server.State]
		if stateColor == "" {
			stateColor = "muted"
		}
		detail := []string{th.FG("dim", server.Transport+" · "+server.Target)}
		if len(server.Tools) > 0 {
			detail = append(detail, th.FG("dim", fmt.Sprintf("%d tools: %s", len(server.Tools), strings.Join(server.Tools, ", "))))
		}
		if server.Error != "" {
			detail = append(detail, th.FG("error", server.Error))
		}
		summary := fmt.Sprintf("%d tools", len(server.Tools))
		if server.Error != "" {
			summary = server.Error
		}
		rows = append(rows, tui.GridRow{
			Value: server.Name,
			Cells: []string{
				th.FG(stateColor, string(server.State)),
				th.FG("text", server.Name),
				th.FG("muted", server.Transport),
				th.FG("dim", summary),
			},
			Detail: detail,
		})
	}
	return rows
}

func (panel *mcpPanel) HandleInput(event tui.KeyEvent) { panel.frame.HandleInput(event) }
func (panel *mcpPanel) SetFocused(focused bool)        { panel.frame.SetFocused(focused) }
func (panel *mcpPanel) WantsMouseMotion() bool         { return panel.frame.WantsMouseMotion() }
func (panel *mcpPanel) HandleMouse(event tui.MouseEvent) bool {
	return panel.frame.HandleMouse(event)
}

var _ tui.Focusable = (*mcpPanel)(nil)
