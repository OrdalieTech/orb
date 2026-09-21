package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/tui"
)

func bridgeExtension(args CLIArgs, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		profile := args.BridgeProfile
		if profile == "" {
			profile = "personal"
		}
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, c extensions.Context) (any, error) {
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
			message := "Bridge · " + profile
			c.UI().SetStatus("bridge", &message)
			return nil, nil
		})
		api.RegisterCommand("bridge", extensions.Command{Description: "Manage paired devices, shared instances, and discovery scopes", Handler: func(ctx context.Context, _ string, c extensions.CommandContext) error {
			if c.Mode() != extensions.ModeTUI {
				return fmt.Errorf("bridge administration requires the local TUI")
			}
			return bridgeSettingsWindow(ctx, c, args, settings, profile)
		}})
		return nil
	}
}

type bridgeSettingsStatus struct {
	PeerID    string              `json:"peer_id"`
	Groups    map[string]string   `json:"groups"`
	Scopes    map[string][]string `json:"scopes"`
	Instances []bridge.Instance   `json:"instances"`
	Pending   []bridge.Invitation `json:"pending"`
	Peers     []string            `json:"peers"`
	Grants    []bridge.Grant      `json:"grants"`
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
		if !running && page != "" {
			page = ""
		}
		result, ok, menuErr := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			rows := bridgeSettingsRows(page, running, args.BridgeProfile != "" || settings.GetPlugins()["bridge"], settings.GetPlugins()["bridge-agent-calls"], status, th)
			if notice != "" {
				for i := range rows {
					if !rows[i].Header {
						rows[i].Detail = append([]string{th.FG("warning", notice)}, rows[i].Detail...)
					}
				}
			}
			return newBridgeSettingsPanel(profile, page, selected, rows, th, host.Height, done), nil
		}, &extensions.CustomOptions{Overlay: true, StaticOverlayOptions: &extensions.OverlayOptions{Width: "80%", MinWidth: 32, MaxHeight: "90%", Backdrop: true}})
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
		actionErr := func() error {
			if client != nil {
				defer func() { _ = client.Close() }()
			}
			if next, found := strings.CutPrefix(action, "page:"); found {
				page, selected = next, ""
				return nil
			}
			switch action {
			case "refresh":
				return nil
			case "Start":
				if err := setBridgeSetting(settings, "bridge", true); err != nil {
					return err
				}
				if err := startBridge(ctx, profile, true); err != nil {
					return err
				}
				if err := args.bridgeLink.configureBridge(true); err != nil {
					return err
				}
				message := "Bridge · " + profile
				ui.SetStatus("bridge", &message)
				selected = "Stop"
				return nil
			case "Stop":
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
				select {
				case <-client.Done():
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(2 * time.Second):
					return fmt.Errorf("bridge is still stopping; press r to refresh")
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
	pending := 0
	for _, inv := range status.Pending {
		if inv.Claimant != "" && inv.Status != "approved" {
			pending++
		}
	}
	switch page {
	case "devices":
		rows := []tui.GridRow{
			row("Invite device", "Pair a new device", "Create invitation", "Choose what to share, then send a private invitation."),
			row("Join device", "Use an invitation", "Paste invitation", "Connect to another device using its private invitation."),
		}
		if pending > 0 {
			rows = append(rows, row("Approve pairing", "Pairing requests", fmt.Sprintf("%d pending", pending), "Verify the device fingerprint and approve its exact access."))
		}
		for _, peer := range status.Peers {
			rows = append(rows, row("peer:"+peer, "Device "+peer[:min(12, len(peer))], "Open", "Fingerprint: "+peer))
		}
		if len(status.Peers) == 0 {
			rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG("dim", "No paired devices yet")}})
		}
		return rows
	case "access":
		return []tui.GridRow{
			row("Grant access", "Share conversations", "View or control", "Choose a device, resource group, and permitted controls."),
			row("Revoke access", "Manage grants", fmt.Sprintf("%d grants", len(status.Grants)), "Remove a grant to revoke its access immediately."),
			row("Instances", "Local instances", fmt.Sprintf("%d registered", len(status.Instances)), "Assign an instance to a resource group."),
			row("Groups", "Create a group", fmt.Sprintf("%d groups", len(status.Groups)), "Group instances to share them with the same devices."),
		}
	case "advanced":
		return []tui.GridRow{
			row("Discovery scopes", "Discovery scopes", fmt.Sprintf("%d scopes", len(status.Scopes)), "Choose which devices may exchange contacts. Discovery grants no control."),
			row("Operation status", "Look up an operation", "Receipt", "Check the durable outcome of a remote command."),
			row("Status", "Device identity", "Fingerprint", status.PeerID),
		}
	}
	if !running {
		return []tui.GridRow{
			row("Start", "Enable Bridge", "○ Off", "Connect your Orb devices. The service keeps running after Orb closes."),
			{Header: true, Cells: []string{th.FG("dim", "Pair devices and continue conversations from another Orb.")}},
		}
	}
	calls := "Off"
	if agentCalls {
		calls = "On"
	}
	service := row("Stop", "Bridge", th.FG("success", "● Running"), "Turn off Bridge and disconnect remote access. Local work continues.")
	if !enabled {
		service = row("Start", "Enable Bridge", "Service running", "Connect this Orb to the running service and enable it for future launches.")
	}
	return []tui.GridRow{
		service,
		row("page:devices", "Devices", fmt.Sprintf("%d paired · %d pending", len(status.Peers), pending), "Pair a device, approve requests, or open a shared conversation."),
		row("page:access", "Sharing & access", fmt.Sprintf("%d grants", len(status.Grants)), "Manage shared instances, groups, and permissions."),
		row("agent-calls", "Agent calls", calls, "Let agents call shared instances. Both devices must grant access."),
		row("page:advanced", "Advanced", "Open", "Discovery scopes, operation receipts, and this device's identity."),
	}
}

type bridgeSettingsPanel struct {
	*tui.Frame
	mu      sync.Mutex
	list    *tui.GridList
	height  func() int
	pending func()
}

func newBridgeSettingsPanel(profile, page, selected string, rows []tui.GridRow, th extensions.Theme, height func() int, done extensions.CustomDone) *bridgeSettingsPanel {
	list := tui.NewGridList(rows, 10, tui.GridListTheme{
		SelectedBg: func(s string) string { return th.BG("selectedBg", s) },
		Detail:     func(s string) string { return th.FG("dim", s) },
		ScrollInfo: func(s string) string { return th.FG("muted", s) },
		Cursor:     th.FG("accent", "› "),
	})
	list.DetailHeight = 2
	for i, row := range rows {
		if row.Value == selected {
			list.ListSelectRow(i)
			break
		}
	}
	panel := &bridgeSettingsPanel{list: list, height: height}
	list.OnConfirm = func(value string) { panel.pending = func() { done(value) } }
	list.OnCancel = func() { panel.pending = func() { done(nil) } }
	list.OnKey = func(key tui.KeyEvent, _ string) bool {
		if key.Raw == "r" {
			panel.pending = func() { done("refresh") }
			return true
		}
		return false
	}
	title := "Bridge · " + profile
	if page != "" {
		title += " / " + map[string]string{"devices": "Devices", "access": "Sharing & access", "advanced": "Advanced"}[page]
	}
	footer := "enter select · r refresh · esc"
	if page != "" {
		footer += " back"
	}
	frame := tui.NewFrame(title, footer, func(s string) string { return th.FG("border", s) }, func(s string) string { return th.FG("dim", s) }, list)
	frame.TitleStyle = func(s string) string { return th.Bold(th.FG("accent", s)) }
	panel.Frame = frame
	return panel
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

func bridgeSettingsAction(ctx context.Context, ui extensions.UI, profile, action string, client *protocol.Conn, status bridgeSettingsStatus) error {
	input := func(title string) (string, error) {
		v, ok, e := ui.Input(ctx, title, nil, nil)
		if !ok && e == nil {
			e = context.Canceled
		}
		return v, e
	}
	group := func() (string, error) {
		labels := []string{}
		ids := map[string]string{}
		for id, name := range status.Groups {
			label := name + " · " + id
			labels = append(labels, label)
			ids[label] = id
		}
		slices.Sort(labels)
		v, ok, e := ui.Select(ctx, "Resource group", labels, nil)
		if !ok && e == nil {
			e = context.Canceled
		}
		return ids[v], e
	}
	choosePeer := func() (string, error) {
		if len(status.Peers) == 0 {
			return "", fmt.Errorf("pair a device first in Devices")
		}
		v, ok, e := ui.Select(ctx, "Paired device", status.Peers, nil)
		if !ok && e == nil {
			e = context.Canceled
		}
		return v, e
	}
	selectGrant := func() (bridge.Grant, error) {
		g := bridge.Grant{}
		var e error
		g.GroupID, e = group()
		if e != nil {
			return g, e
		}
		mode, ok, e := ui.Select(ctx, "Access", []string{"View conversations", "Control conversations"}, nil)
		if e != nil || !ok {
			return g, context.Canceled
		}
		g.Permissions = []string{"instance.list", "instance.inspect"}
		if mode == "Control conversations" {
			g.Permissions = append(g.Permissions, "instance.prompt", "instance.steer", "instance.follow_up", "instance.cancel", "instance.session.manage")
		}
		g.IncludeFuture, e = ui.Confirm(ctx, "Future instances", "Also allow this device to access future instances assigned to this group?", nil)
		return g, e
	}
	peer, isPeer := strings.CutPrefix(action, "peer:")
	if isPeer {
		action = "Peers"
	}
	switch action {
	case "Status":
		ui.Notify(fmt.Sprintf("%s\n%d instances · %d peers · %d grants", status.PeerID, len(status.Instances), len(status.Peers), len(status.Grants)), extensions.NotifyInfo)
	case "Invite device":
		g, e := selectGrant()
		if e != nil {
			return e
		}
		var inv bridge.Invitation
		if e = client.Call(ctx, "invite", map[string]any{"grants": []bridge.Grant{g}}, &inv); e != nil {
			return e
		}
		_, _, e = ui.Editor(ctx, "Private invitation · share only with the intended device", stringValue(string(connect.JSON(inv))))
		return e
	case "Join device":
		text, e := input("Paste the private invitation")
		if e != nil {
			return e
		}
		var inv bridge.Invitation
		if e = protocol.Decode([]byte(text), &inv); e != nil {
			return e
		}
		ok, e := ui.Confirm(ctx, "Pair device", inv.PeerID+"\nVerify this fingerprint with the other device.", nil)
		if e != nil || !ok {
			return e
		}
		var result bridge.Invitation
		if e = client.Call(ctx, "join", inv, &result); e != nil {
			return e
		}
		ui.Notify("Claimed. The other device must approve "+status.PeerID, extensions.NotifyInfo)
	case "Approve pairing":
		choices := []string{}
		claims := map[string]bridge.Invitation{}
		for _, i := range status.Pending {
			if i.Claimant != "" && i.Status != "approved" {
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
		details := "Claimant: " + inv.Claimant
		for _, g := range inv.Grants {
			details += "\n" + status.Groups[g.GroupID] + ": " + strings.Join(g.Permissions, ", ")
			if g.IncludeFuture {
				details += " (including future instances)"
			}
		}
		ok, e = ui.Confirm(ctx, "Approve these exact grants?", details, nil)
		if e != nil || !ok {
			return e
		}
		return client.Call(ctx, "approve", map[string]string{"invitation_id": inv.ID, "claimant": inv.Claimant}, nil)
	case "Peers":
		choice, ok, e := ui.Select(ctx, peer, []string{"Open conversation", "Block device"}, nil)
		if e != nil || !ok {
			return e
		}
		if choice == "Block device" {
			yes, e := ui.Confirm(ctx, "Block device", "End current access and deny new calls from "+peer+"?", nil)
			if e != nil || !yes {
				return e
			}
			return client.Call(ctx, "block", map[string]string{"peer_id": peer}, nil)
		}
		var catalog struct {
			Items []bridge.Instance `json:"items"`
		}
		if e = client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": "instances.list", "params": struct{}{}}, &catalog); e != nil {
			return e
		}
		labels := []string{}
		for _, r := range catalog.Items {
			labels = append(labels, r.Alias+" · "+r.ID)
		}
		if len(labels) == 0 {
			return fmt.Errorf("this device has no shared conversations available")
		}
		choice, ok, e = ui.Select(ctx, "Shared conversations", labels, nil)
		if e != nil || !ok {
			return e
		}
		_, instance, _ := strings.Cut(choice, " · ")
		return openBridgeView(ctx, ui, profile, peer, instance)
	case "Grant access":
		peer, e := choosePeer()
		if e != nil {
			return e
		}
		g, e := selectGrant()
		if e != nil {
			return e
		}
		g.Principal = connect.Principal{PeerID: peer, Subject: connect.Subject{Kind: "controller"}}
		return client.Call(ctx, "grant", g, nil)
	case "Revoke access":
		if len(status.Grants) == 0 {
			return fmt.Errorf("no access grants to remove")
		}
		labels := []string{}
		ids := map[string]string{}
		for _, g := range status.Grants {
			label := g.Principal.PeerID + " · " + strings.Join(g.Permissions, ", ") + " · " + g.ID
			labels = append(labels, label)
			ids[label] = g.ID
		}
		label, ok, e := ui.Select(ctx, "Revoke grant", labels, nil)
		if e != nil || !ok {
			return e
		}
		return client.Call(ctx, "revoke", map[string]string{"grant_id": ids[label]}, nil)
	case "Instances":
		if len(status.Instances) == 0 {
			return fmt.Errorf("no local instances are attached")
		}
		choices := []string{}
		for _, r := range status.Instances {
			availability := "unavailable"
			if r.Available {
				availability = "connected"
			}
			choices = append(choices, r.Alias+" · "+r.ID+" · "+availability)
		}
		choice, ok, e := ui.Select(ctx, "Local instances", choices, nil)
		if e != nil || !ok {
			return e
		}
		parts := strings.Split(choice, " · ")
		id, e := group()
		if e != nil {
			return e
		}
		return client.Call(ctx, "assign", map[string]string{"instance_id": parts[1], "group_id": id}, nil)
	case "Groups":
		name, e := input("New resource group name")
		if e != nil {
			return e
		}
		return client.Call(ctx, "group", map[string]string{"name": name}, nil)
	case "Discovery scopes":
		id, e := input("Scope ID (leave empty to create)")
		if e != nil {
			return e
		}
		if id == "" {
			id = protocol.NewID()
		}
		peers, e := input("Allowed PeerIDs, separated by commas")
		if e != nil {
			return e
		}
		members := []string{}
		for _, p := range strings.Split(peers, ",") {
			members = append(members, strings.TrimSpace(p))
		}
		if e = client.Call(ctx, "scope", map[string]any{"scope_id": id, "peers": members}, nil); e != nil {
			return e
		}
		return client.Call(ctx, "publish", map[string]any{"scope_id": id, "display_name": profile, "withdrawn": false}, nil)
	case "Operation status":
		peer, e := choosePeer()
		if e != nil {
			return e
		}
		instance, e := input("Instance ID")
		if e != nil {
			return e
		}
		operation, e := input("Operation ID")
		if e != nil {
			return e
		}
		var result json.RawMessage
		e = client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": "operations.get", "params": map[string]string{"instance_id": instance, "operation_id": operation}}, &result)
		if e != nil {
			return e
		}
		ui.Notify(string(result), extensions.NotifyInfo)
	}
	return nil
}
