package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/clipboard"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/plugins/bridge"
	"github.com/OrdalieTech/orb/storage/sqlite"
	"github.com/OrdalieTech/orb/tui"
)

func bridgeExtension(args CLIArgs, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		profile := args.BridgeProfile
		if profile == "" {
			profile = "personal"
		}
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, c extensions.Context) (any, error) {
			if args.native != nil {
				ctx = context.WithValue(ctx, nativeStateKey{}, args.native)
			}
			if args.BridgeProfile == "" && !settings.GetPlugins()["bridge"] {
				return nil, nil
			}
			if err := startBridge(ctx, profile, false); err != nil {
				message := "Bridge disconnected"
				c.UI().SetStatus("bridge", &message)
				return nil, nil
			}
			if err := args.bridgeLink.configureBridge(true); err != nil {
				return nil, err
			}
			c.UI().SetStatus("bridge", nil)
			return nil, nil
		})
		api.RegisterCommand("bridge", extensions.Command{SettingsLabel: "Bridge", Description: "Connect devices and open their conversations", Handler: func(ctx context.Context, _ string, c extensions.CommandContext) error {
			if args.native != nil {
				ctx = context.WithValue(ctx, nativeStateKey{}, args.native)
			}
			if c.Mode() != extensions.ModeTUI {
				return fmt.Errorf("bridge administration requires the local TUI")
			}
			return bridgeSettingsWindow(ctx, c, args, settings, profile)
		}})
		return nil
	}
}

type bridgeSettingsStatus struct {
	SupportsFullAccess bool                `json:"supports_full_access"`
	PeerStates         map[string]string   `json:"peer_states"`
	PeerID             string              `json:"peer_id"`
	Groups             map[string]string   `json:"groups"`
	Pending            []bridge.Invitation `json:"pending"`
	Peers              []string            `json:"peers"`
	Grants             []bridge.Grant      `json:"grants"`
}

func setBridgeSetting(settings *config.SettingsManager, name string, enabled bool) error {
	if settings.ProjectDefinesPlugin(name) {
		return fmt.Errorf("%s is configured in project settings; edit .pi/settings.json", name)
	}
	settings.SetPluginEnabled(name, enabled)
	for _, err := range settings.DrainErrors() {
		return err
	}
	return nil
}

func bridgeSettingsWindow(ctx context.Context, c extensions.CommandContext, args CLIArgs, settings *config.SettingsManager, profile string) (err error) {
	ui := c.UI()
	page, selected, notice := "", "", ""
	reload := false
	defer func() {
		if reload {
			err = errors.Join(err, c.Reload(ctx))
		}
	}()
	for ctx.Err() == nil {
		var status bridgeSettingsStatus
		probe, cancel := context.WithTimeout(ctx, time.Second)
		client, probeErr := bridgeAdmin(probe, profile)
		if probeErr == nil {
			probeErr = client.Call(probe, "status", struct{}{}, &status)
		}
		cancel()
		running := probeErr == nil
		if client != nil {
			_ = client.Close()
		}
		result, ok, menuErr := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			enabled, agentCalls := args.BridgeProfile != "" || settings.GetPlugins()["bridge"], settings.GetPlugins()["bridge-agent-calls"]
			rows := bridgeSettingsRows(page, running, enabled, agentCalls, status, th)
			panel := newBridgeSettingsPanel(profile, page, selected, rows, th, host.Height, done)
			page, notice, status := page, notice, status
			panel.watch(ctx, host, func(ctx context.Context) []tui.GridRow {
				probe, cancel := context.WithTimeout(ctx, time.Second)
				defer cancel()
				var current bridgeSettingsStatus
				client, err := bridgeAdmin(probe, profile)
				if err == nil {
					err = client.Call(probe, "status", struct{}{}, &current)
					_ = client.Close()
				}
				if err == nil {
					status = current
				}
				rows := bridgeSettingsRows(page, err == nil, enabled, agentCalls, status, th)
				if notice != "" {
					for i := range rows {
						rows[i].Detail = []string{th.FG("warning", notice)}
					}
				}
				return rows
			})
			return panel, nil
		}, extensions.ModalOptions())
		action, _ := result.(string)
		if menuErr != nil || !ok || action == "" {
			if client != nil {
				_ = client.Close()
			}
			if menuErr != nil {
				return menuErr
			}
			if page != "" {
				page, selected = "", "page:"+page
				continue
			}
			return nil
		}
		selected, notice = action, ""
		status = bridgeSettingsStatus{}
		probe, cancel = context.WithTimeout(ctx, time.Second)
		client, probeErr = bridgeAdmin(probe, profile)
		if probeErr == nil {
			probeErr = client.Call(probe, "status", struct{}{}, &status)
		}
		cancel()
		running = probeErr == nil
		actionErr := func() error {
			defer func() {
				if client != nil {
					_ = client.Close()
				}
			}()
			enable := func() error {
				if err := setBridgeSetting(settings, "bridge", true); err != nil {
					return err
				}
				ui.Notify("Starting Bridge…", extensions.NotifyInfo)
				if err := startBridge(ctx, profile, true); err != nil {
					return err
				}
				if err := args.bridgeLink.configureBridge(true); err != nil {
					return err
				}
				ui.SetStatus("bridge", nil)
				return nil
			}
			if action == "Invite device" || action == "Join device" || action == "SSH" {
				if !running || !status.SupportsFullAccess || args.BridgeProfile == "" && !settings.GetPlugins()["bridge"] {
					var err error
					if running && (args.BridgeProfile != "" || settings.GetPlugins()["bridge"]) {
						err = startBridge(ctx, profile, true)
					} else {
						err = enable()
					}
					if err != nil {
						return err
					}
					if client != nil {
						_ = client.Close()
					}
					client, err = bridgeAdmin(ctx, profile)
					if err != nil {
						return err
					}
					if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
						return err
					}
					running = true
				}
			}
			if next, found := strings.CutPrefix(action, "page:"); found {
				page, selected = next, ""
				return nil
			}
			switch action {
			case "Start":
				selected = "Stop"
				return enable()
			case "Stop":
				if !running {
					return fmt.Errorf("bridge is already offline")
				}
				if err := setBridgeSetting(settings, "bridge", false); err != nil {
					return err
				}
				if err := client.Call(ctx, "stop", struct{}{}, nil); err != nil {
					return err
				}
				if err := args.bridgeLink.configureBridge(false); err != nil {
					return err
				}
				ui.SetStatus("bridge", nil)
				if err := waitBridgeStopped(ctx, client); err != nil {
					return err
				}
				selected = "Start"
				return nil
			case "agent-calls":
				if err := setBridgeSetting(settings, "bridge-agent-calls", !settings.GetPlugins()["bridge-agent-calls"]); err != nil {
					return err
				}
				reload = true
				notice = "Agent tools update when you close Bridge."
				return nil
			default:
				if !running {
					return fmt.Errorf("bridge is offline; start it to reconnect")
				}
				return bridgeSettingsAction(ctx, ui, profile, action, client, status)
			}
		}()
		if actionErr != nil && !errors.Is(actionErr, context.Canceled) {
			notice = actionErr.Error()
		}
	}
	return ctx.Err()
}

func bridgeSettingsRows(page string, running, enabled, agentCalls bool, status bridgeSettingsStatus, th extensions.Theme) []tui.GridRow {
	row := func(value, label, state, detail string) tui.GridRow {
		return tui.GridRow{Value: value, Cells: []string{th.FG("text", label), th.FG("muted", state)}, Detail: []string{detail}}
	}
	if page == "add" {
		return []tui.GridRow{
			row("SSH", "Connect a server", "SSH", "Enter user@host. Orb handles installation and pairing."),
			row("Join device", "Paste an invitation", "", "Connect using an invitation from another Orb."),
			row("Invite device", "Create an invitation", "", "Invite another device. Both Orbs share all current and future conversations."),
		}
	}
	if page == "advanced" {
		calls := "Off"
		if agentCalls {
			calls = "On"
		}
		rows := []tui.GridRow{row("agent-calls", "Agent calls", calls, "Allow agents to use Bridge tools. Requires separate grants on both devices.")}
		if running {
			rows = append(rows, row("Status", "This device's fingerprint", "", status.PeerID))
		}
		return rows
	}
	service := row("Start", "Enable Bridge", "○ Off", "Connect your devices to share conversations. Local work continues when Bridge is off.")
	if running && !enabled {
		service = row("Start", "Share this conversation", "Bridge on", "Attach this conversation and enable Bridge for future launches.")
	}
	if running && enabled {
		service = row("Stop", "Bridge", th.FG("success", "● On"), "Turn off remote access. Your devices stay saved for next time.")
	}
	rows := []tui.GridRow{service, row("page:add", "Add device", "", "Connect a server through SSH or exchange an invitation.")}
	for _, inv := range status.Pending {
		if running && inv.Claimant != "" && inv.Status != "approved" && inv.Expires > time.Now().Unix() {
			rows = append(rows, row("Approve pairing", "Connection request", "Review", "Verify the other device before allowing access."))
			break
		}
	}
	if len(status.Peers) > 0 {
		rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG("dim", "Devices")}})
	}
	for _, peer := range status.Peers {
		state := "Offline"
		if running {
			switch status.PeerStates[peer] {
			case "connected":
				state = "Connected"
			case "blocked":
				state = "Blocked"
			}
		}
		rows = append(rows, row("peer:"+peer, bridgeDeviceLabel(peer), state, "Open this device's conversations. Availability updates automatically."))
	}
	return append(rows, row("page:advanced", "Advanced", "", "Agent tools and this device's fingerprint."))
}

func bridgeDeviceLabel(peer string) string {
	fingerprint := strings.TrimPrefix(peer, "orb:ed25519:")
	return "Device " + fingerprint[:min(10, len(fingerprint))]
}

type bridgeSettingsPanel struct {
	*tui.Frame
	mu      sync.Mutex
	list    *tui.GridList
	height  func() int
	pending func()
	cancel  context.CancelFunc
}

func newBridgeSettingsPanel(profile, page, selected string, rows []tui.GridRow, th extensions.Theme, height func() int, done extensions.CustomDone) *bridgeSettingsPanel {
	list := tui.NewGridList(rows, 10, tui.GridListTheme{
		SelectedBg: func(s string) string { return th.BG("selectedBg", s) },
		Detail:     func(s string) string { return th.FG("dim", s) },
		ScrollInfo: func(s string) string { return th.FG("muted", s) },
		Cursor:     th.FG("accent", "› "),
	})
	list.DetailHeight = 2
	list.WrapDetail = true
	for i, row := range rows {
		if row.Value == selected {
			list.ListSelectRow(i)
			break
		}
	}
	panel := &bridgeSettingsPanel{list: list, height: height}
	list.OnConfirm = func(value string) { panel.pending = func() { done(value) } }
	list.OnCancel = func() { panel.pending = func() { done(nil) } }
	title := "Bridge"
	if profile != "personal" {
		title += " · " + profile
	}
	if page != "" {
		title += " / " + map[string]string{"add": "Add device", "advanced": "Advanced"}[page]
	}
	footer := "enter select · esc back"
	frame := tui.NewPanel(title, footer,
		func(s string) string { return th.Bold(th.FG("text", s)) },
		func(s string) string { return th.FG("dim", s) },
		func() string { return th.BGANSI("toolPendingBg") }, list)
	panel.Frame = frame
	return panel
}
func (p *bridgeSettingsPanel) watch(ctx context.Context, host extensions.UIHost, load func(context.Context) []tui.GridRow) {
	ctx, p.cancel = context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var previous []tui.GridRow
		for {
			rows := load(ctx)
			if ctx.Err() != nil {
				return
			}
			if !reflect.DeepEqual(rows, previous) {
				p.mu.Lock()
				p.list.SetRows(rows)
				p.mu.Unlock()
				previous = rows
				host.Invalidate()
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (p *bridgeSettingsPanel) Render(width int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list.SetMaxVisible(max(1, min(10, p.height()-12)))
	return p.Frame.Render(width)
}
func (p *bridgeSettingsPanel) HandleInput(key tui.KeyEvent) {
	p.mu.Lock()
	defer p.unlockAndDispatch()
	p.Frame.HandleInput(key)
}
func (p *bridgeSettingsPanel) HandleMouse(event tui.MouseEvent) bool {
	p.mu.Lock()
	defer p.unlockAndDispatch()
	return p.Frame.HandleMouse(event)
}
func (p *bridgeSettingsPanel) SetFocused(focused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Frame.SetFocused(focused)
}

func (p *bridgeSettingsPanel) unlockAndDispatch() {
	action := p.pending
	p.pending = nil
	p.mu.Unlock()
	if action != nil {
		action()
	}
}

func (p *bridgeSettingsPanel) Dispose() {
	if p.cancel != nil {
		p.cancel()
	}
}

func bridgeSettingsAction(ctx context.Context, ui extensions.UI, profile, action string, client *protocol.Conn, status bridgeSettingsStatus) error {
	input := func(title string) (string, error) {
		v, ok, e := ui.Input(ctx, title, nil, nil)
		if !ok && e == nil {
			e = context.Canceled
		}
		return v, e
	}
	peer, isPeer := strings.CutPrefix(action, "peer:")
	if isPeer {
		action = "Peers"
	}
	switch action {
	case "Status":
		ui.Notify(status.PeerID, extensions.NotifyInfo)
	case "Invite device":
		var inv bridge.Invitation
		if e := client.Call(ctx, "invite", map[string]any{"grants": []bridge.Grant{fullBridgeGrant("")}}, &inv); e != nil {
			return e
		}

		return shareBridgeInvitation(ctx, ui, client, inv, status.Groups)
	case "Join device":
		text, e := input("Connect to a device · paste its invitation")
		if e != nil {
			return e
		}
		inv, e := parseBridgeInvitation(text)
		if e != nil {
			return e
		}
		ok, e := ui.Confirm(ctx, "Trust this Orb?", inv.PeerID+"\nAllow full control of your current and future conversations.\nVerify this fingerprint on the sharing Orb.", nil)
		if e != nil || !ok {
			return e
		}
		var claimed bridge.Invitation
		if e = client.Call(ctx, "join", inv, &claimed); e != nil {
			return e
		}
		inv.Claimant = status.PeerID
		_, e = waitBridgePairing(ctx, ui, "Connect to a device", "Waiting for approval on the sharing device.", inv, "", func(ctx context.Context) (bridge.Invitation, error) {
			var result bridge.Invitation
			err := client.Call(ctx, "remote", map[string]any{"peer_id": inv.PeerID, "method": "pair.status", "params": map[string]string{"invitation_id": inv.ID}}, &result)
			return result, err
		}, func(i bridge.Invitation) bool { return i.Status == "approved" })
		if e != nil {
			return e
		}
		if e = trustBridgePeer(ctx, client, inv.PeerID); e != nil {
			return e
		}
		ui.Notify("Device connected. Choose a shared conversation.", extensions.NotifyInfo)
		return openSharedBridgeConversation(ctx, ui, profile, inv.PeerID)
	case "SSH":
		target, e := input("Connect using SSH · user@host or SSH alias")
		if e != nil {
			return e
		}
		ok, e := ui.Confirm(ctx, "Trust "+target+"?", "Share full control of current and future conversations in both directions.\nOrb will be installed or updated on the server if needed.", nil)
		if e != nil || !ok {
			return e
		}
		ui.Notify("Setting up Orb on "+target+"…", extensions.NotifyInfo)
		peer, e := connectBridgeSSH(ctx, client, status.PeerID, target, "personal", "orb")
		if e != nil {
			return e
		}
		ui.Notify("Server paired through SSH.", extensions.NotifyInfo)
		return openSharedBridgeConversation(ctx, ui, profile, peer)

	case "Approve pairing":
		choices := []string{}
		claims := map[string]bridge.Invitation{}
		for _, i := range status.Pending {
			if i.Claimant != "" && i.Status != "approved" && i.Expires > time.Now().Unix() {
				label := i.Claimant + " · " + i.ID
				choices = append(choices, label)
				claims[label] = i
			}
		}
		choice, ok, e := ui.Select(ctx, "Verify the claimant fingerprint", choices, nil)
		if e != nil || !ok {
			return e
		}
		inv := claims[choice]
		return approveBridgePairing(ctx, ui, client, inv, status.Groups)
	case "Peers":
		if status.PeerStates[peer] == "blocked" {
			return fmt.Errorf("this device is blocked.\nIts access has been revoked")
		}
		return openSharedBridgeConversation(ctx, ui, profile, peer)
	}
	return nil
}

func bridgeInvitationCode(inv bridge.Invitation) string {
	return "orb-bridge:v1:" + base64.RawURLEncoding.EncodeToString(connect.JSON(inv))
}

func parseBridgeInvitation(text string) (bridge.Invitation, error) {
	var inv bridge.Invitation
	text = strings.TrimSpace(text)
	if len(text) > protocol.MaxFrame {
		return inv, fmt.Errorf("invitation is too large")
	}
	raw := []byte(text)
	if code, ok := strings.CutPrefix(text, "orb-bridge:v1:"); ok {
		var err error
		raw, err = base64.RawURLEncoding.Strict().DecodeString(code)
		if err != nil {
			return inv, fmt.Errorf("invalid invitation; copy it again from Create an invitation")
		}
	}
	if err := protocol.Decode(raw, &inv); err != nil {
		return inv, fmt.Errorf("invalid invitation; paste the complete invitation")
	}
	if _, err := bridge.ParsePeerID(inv.PeerID); err != nil || !protocol.ValidID(inv.ID) || inv.Token == "" || inv.Locator == "" {
		return inv, fmt.Errorf("incomplete invitation; create a new one on the other device")
	}
	if inv.Expires <= time.Now().Unix() {
		return inv, fmt.Errorf("invitation expired; create a new one on the other device")
	}
	return inv, nil
}

func bridgeConversationRows(ctx context.Context, client *protocol.Conn, peer string, th extensions.Theme) ([]tui.GridRow, error) {
	rows := []tui.GridRow{}
	cursor, count := "", 0
	for count < 4096 {
		var catalog struct {
			Items  []bridge.Instance `json:"items"`
			Cursor string            `json:"cursor"`
		}
		if err := client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": "instances.list", "params": map[string]string{"cursor": cursor}}, &catalog); err != nil {
			return nil, err
		}
		count += len(catalog.Items)
		if count > 4096 || len(catalog.Items) == 0 && catalog.Cursor != "" {
			return nil, connect.Fail("resource_exhausted")
		}
		for _, instance := range catalog.Items {
			if !instance.Available {
				continue
			}
			label := instance.Alias
			if label == "" {
				label = "Conversation"
			}
			rows = append(rows, tui.GridRow{Value: instance.ID, Cells: []string{th.FG("text", label)}, Detail: []string{"Open conversation"}})
		}
		if catalog.Cursor == "" {
			if len(rows) == 0 {
				rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG("muted", "No conversations open yet")}}, tui.GridRow{Header: true, Cells: []string{th.FG("dim", "Open Orb on this device with Bridge enabled.")}})
			}
			return rows, nil
		}
		if catalog.Cursor == cursor {
			break
		}
		cursor = catalog.Cursor
	}
	return nil, connect.Fail("resource_exhausted")
}

func openSharedBridgeConversation(ctx context.Context, ui extensions.UI, profile, peer string) error {
	db, cacheErr := openBridgeCache(ctx, profile)
	var cache *sqlite.Foreign
	if cacheErr == nil {
		defer func() { _ = db.Close() }()
		cache = db.Foreign(profile)
	}
	cachedRows := func(th extensions.Theme, active map[string]bool) []tui.GridRow {
		if cache == nil {
			return nil
		}
		entries, err := cache.List(ctx, peer)
		if err != nil {
			return nil
		}
		rows := []tui.GridRow{}
		for _, entry := range entries {
			if active[entry.Instance] {
				continue
			}
			name := entry.Name
			if name == "" {
				name = entry.ID
			}
			detail := "Cached · " + entry.RefreshedAt.Format(time.RFC822) + " · read-only until connected"
			rows = append(rows, tui.GridRow{Value: "cached:" + string(connect.JSON([2]string{entry.Namespace, entry.ID})), Cells: []string{th.FG("muted", tui.StripANSI(name))}, Detail: []string{detail}})
		}
		return rows
	}
	selected := ""
	for ctx.Err() == nil {
		result, ok, err := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			panel := newBridgeSettingsPanel(profile, "", "", []tui.GridRow{{Header: true, Cells: []string{"Connecting…"}}}, th, host.Height, done)
			panel.Title = bridgeDeviceLabel(peer)
			if rows := cachedRows(th, nil); len(rows) > 0 {
				panel.list.SetRows(rows)
			}
			preferred := selected
			panel.watch(ctx, host, func(ctx context.Context) []tui.GridRow {
				probe, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				client, err := bridgeAdmin(probe, profile)
				var rows []tui.GridRow
				if err == nil {
					rows, err = bridgeConversationRows(probe, client, peer, th)
					_ = client.Close()
				}
				if err != nil {
					if connect.Code(err) == "unauthorized" && cache != nil {
						_ = cache.Forget(ctx, peer)
					}
					message, detail := "Reconnecting…", "Keep Bridge on at both devices. Retrying automatically."
					switch connect.Code(err) {
					case "unauthorized":
						message, detail = "Access unavailable", "Check that this device has granted access to your Orb."
					case "resource_exhausted":
						message, detail = "Too many conversations", "This device's conversation list exceeds the Bridge limit."
					}
					rows = []tui.GridRow{{Header: true, Cells: []string{th.FG("warning", message)}}, {Header: true, Cells: []string{th.FG("dim", detail)}}}
					if connect.Code(err) != "unauthorized" {
						rows = append(rows, cachedRows(th, nil)...)
					}
				}
				if err == nil {
					active := map[string]bool{}
					for _, row := range rows {
						active[row.Value] = true
					}
					rows = append(rows, cachedRows(th, active)...)
				}
				rows = append(rows, tui.GridRow{Value: "disconnect", Cells: []string{th.FG("muted", "Block device")}, Detail: []string{"Block this device and revoke its access to your conversations."}})
				if preferred != "" {
					panel.mu.Lock()
					for i, row := range rows {
						if row.Value == preferred {
							panel.list.SetRows(rows)
							panel.list.ListSelectRow(i)
							break
						}
					}
					panel.mu.Unlock()
					preferred = ""
				}
				return rows
			})
			return panel, nil
		}, extensions.ModalOptions())
		if err != nil || !ok || result == nil {
			return err
		}
		selected, _ = result.(string)
		if selected == "disconnect" {
			yes, err := ui.Confirm(ctx, "Block device?", "Revoke access to your conversations from this device.\n"+peer, nil)
			if err != nil {
				return err
			}
			if !yes {
				continue
			}
			client, err := bridgeAdmin(ctx, profile)
			if err != nil {
				return err
			}
			err = client.Call(ctx, "block", map[string]string{"peer_id": peer}, nil)
			_ = client.Close()
			return err
		}
		if id, ok := strings.CutPrefix(selected, "cached:"); ok {
			if cache != nil {
				entries, err := cache.List(ctx, peer)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if string(connect.JSON([2]string{entry.Namespace, entry.ID})) == id {
						if err := openBridgeView(ctx, ui, profile, peer, entry.Instance, entry); err != nil {
							return err
						}
						break
					}
				}
			}
			continue
		}
		if selected != "" {
			if err := openBridgeView(ctx, ui, profile, peer, selected); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func shareBridgeInvitation(ctx context.Context, ui extensions.UI, client *protocol.Conn, inv bridge.Invitation, groups map[string]string) error {
	claimed, err := waitBridgePairing(ctx, ui, "Create invitation", "Paste in Bridge → Add device → Paste an invitation on the other Orb.", inv, bridgeInvitationCode(inv), func(ctx context.Context) (bridge.Invitation, error) {
		var status bridgeSettingsStatus
		if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return bridge.Invitation{}, err
		}
		for _, item := range status.Pending {
			if item.ID == inv.ID {
				return item, nil
			}
		}
		return bridge.Invitation{}, fmt.Errorf("invitation expired; create a new one")
	}, func(i bridge.Invitation) bool { return i.Claimant != "" })
	if err != nil {
		return err
	}
	return approveBridgePairing(ctx, ui, client, claimed, groups)
}

func approveBridgePairing(ctx context.Context, ui extensions.UI, client *protocol.Conn, claimed bridge.Invitation, groups map[string]string) error {
	details := "Verify this fingerprint on the joining device:\n" + claimed.Claimant
	for _, g := range claimed.Grants {
		if g.GroupID == "*" && g.IncludeFuture && slices.Equal(g.Permissions, fullBridgeGrant("").Permissions) {
			details += "\nAllow full control of all current and future conversations."
			continue
		}
		scope := "current instances only"
		if g.IncludeFuture {
			scope = "current and future instances"
		}
		details += "\n" + groups[g.GroupID] + " · " + scope + "\n" + strings.Join(g.Permissions, ", ")
	}
	yes, err := ui.Confirm(ctx, "Trust this Orb?", details, nil)
	if err != nil || !yes {
		return err
	}
	if err = client.Call(ctx, "approve", map[string]string{"invitation_id": claimed.ID, "claimant": claimed.Claimant}, nil); err != nil {
		return err
	}
	ui.Notify("Device paired. It can now open the conversations you shared.", extensions.NotifyInfo)
	return nil
}

func waitBridgePairing(ctx context.Context, ui extensions.UI, title, instruction string, inv bridge.Invitation, code string, poll func(context.Context) (bridge.Invitation, error), ready func(bridge.Invitation) bool) (bridge.Invitation, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Unix(inv.Expires, 0))
	defer cancel()
	notice := "Invitations expire after 10 minutes. Escape closes this screen."
	for ctx.Err() == nil {
		result, ok, err := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			rows := []tui.GridRow{}
			if code != "" {
				rows = append(rows, tui.GridRow{Value: "copy", Cells: []string{"Copy invitation"}, Detail: []string{instruction, notice}}, tui.GridRow{Value: "show", Cells: []string{"Show invitation"}, Detail: []string{instruction, notice}})
			} else {
				rows = append(rows, tui.GridRow{Value: "fingerprint", Cells: []string{"Show this device's fingerprint"}, Detail: []string{instruction, inv.Claimant}})
			}
			panel := newBridgeSettingsPanel(title, "", "", rows, th, host.Height, done)
			panel.Title = title
			panel.Footer = "enter select · esc close"
			panel.list.OnKey = nil
			waiting, stop := context.WithCancel(ctx)
			panel.cancel = stop
			go func() {
				ticker := time.NewTicker(time.Second)
				defer ticker.Stop()
				for {
					result, err := poll(waiting)
					if waiting.Err() != nil {
						return
					}
					if err != nil {
						done(err)
						return
					}
					if result.ID != inv.ID || result.PeerID != inv.PeerID {
						done(fmt.Errorf("pairing identity changed"))
						return
					}
					if ready(result) {
						done(result)
						return
					}
					select {
					case <-waiting.Done():
						return
					case <-ticker.C:
					}
				}
			}()
			return panel, nil
		}, extensions.ModalOptions())
		if err != nil {
			return bridge.Invitation{}, err
		}
		if !ok || result == nil {
			return bridge.Invitation{}, context.Canceled
		}
		switch value := result.(type) {
		case bridge.Invitation:
			return value, nil
		case error:
			return bridge.Invitation{}, value
		case string:
			switch value {
			case "copy":
				if err := clipboard.CopyToClipboard(code); err != nil {
					notice = "Copy unavailable. Choose Show invitation to copy it manually."
				} else {
					notice = "Copied. Paste it in Add device → Paste an invitation on the other Orb."
				}
			case "fingerprint":
				if _, _, err := ui.Editor(ctx, "Your fingerprint · verify on the sharing device", &inv.Claimant); err != nil {
					return bridge.Invitation{}, err
				}
			case "show":
				if _, _, err := ui.Editor(ctx, "Private invitation · copy the complete text", &code); err != nil {
					return bridge.Invitation{}, err
				}
			default:
				return bridge.Invitation{}, context.Canceled
			}
		}
	}
	return bridge.Invitation{}, fmt.Errorf("invitation expired or pairing was cancelled; create a new invitation to retry")
}
