package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/clipboard"
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
	PeerStates map[string]string   `json:"peer_states"`
	PeerID     string              `json:"peer_id"`
	Groups     map[string]string   `json:"groups"`
	Scopes     map[string][]string `json:"scopes"`
	Instances  []bridge.Instance   `json:"instances"`
	Pending    []bridge.Invitation `json:"pending"`
	Peers      []string            `json:"peers"`
	Grants     []bridge.Grant      `json:"grants"`
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
		}, &extensions.CustomOptions{Overlay: true, StaticOverlayOptions: &extensions.OverlayOptions{Width: "80%", MinWidth: 40, MaxHeight: "85%", Backdrop: true}})
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
				message := "Bridge · " + profile
				ui.SetStatus("bridge", &message)
				return nil
			}
			if action == "Invite device" || action == "Join device" || action == "SSH" {
				if !running || args.BridgeProfile == "" && !settings.GetPlugins()["bridge"] {
					if err := enable(); err != nil {
						return err
					}
					if client != nil {
						_ = client.Close()
					}
					var err error
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
			case "refresh":
				return nil
			case "Start":
				selected = "Stop"
				return enable()
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
	pending := 0
	for _, inv := range status.Pending {
		if inv.Claimant != "" && inv.Status != "approved" {
			pending++
		}
	}
	switch page {
	case "devices":
		rows := []tui.GridRow{
			row("Invite device", "Share this Orb", "Create invitation", "Connect trusted Orbs with full access to current and future conversations."),
			row("Join device", "Connect to a device", "Paste invitation", "Paste an invitation from the Orb you want to control."),
		}
		if pending > 0 {
			rows = append(rows, row("Approve pairing", "Pairing requests", fmt.Sprintf("%d pending", pending), "Verify the device fingerprint and approve its exact access."))
		}
		for _, peer := range status.Peers {
			state := map[string]string{"connected": "Connected", "blocked": "Blocked"}[status.PeerStates[peer]]
			if state == "" {
				state = "Not connected"
			}
			fingerprint := strings.TrimPrefix(peer, "orb:ed25519:")
			rows = append(rows, row("peer:"+peer, "Device "+fingerprint[:min(10, len(fingerprint))], state, "Fingerprint: "+peer))
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
	connectRows := []tui.GridRow{
		row("Invite device", "Share this Orb", "Create invitation", "Connect trusted Orbs with full access to current and future conversations."),
		row("Join device", "Connect to a device", "Paste invitation", "Paste an invitation from another Orb. Starts Bridge if needed."),
		row("SSH", "Connect using SSH", "user@host", "Pair a server using your SSH login, then open its shared conversations."),
	}
	if !running {
		return append([]tui.GridRow{
			row("Start", "Enable Bridge", "○ Off", "The service keeps running after Orb closes. Pairings are saved when stopped."),
		}, connectRows...)
	}
	calls := "Off"
	if agentCalls {
		calls = "On"
	}
	service := row("Stop", "Bridge", th.FG("success", "● Running"), "Turn off Bridge and disconnect remote access. Local work continues.")
	if !enabled {
		service = row("Start", "Enable Bridge", "Service running", "Connect this Orb to the running service and enable it for future launches.")
	}
	return append(connectRows, []tui.GridRow{
		service,
		row("page:devices", "Devices", fmt.Sprintf("%d paired · %d pending", len(status.Peers), pending), "Pair a device, approve requests, or open a shared conversation."),
		row("page:access", "Sharing & access", fmt.Sprintf("%d grants", len(status.Grants)), "Manage shared instances, groups, and permissions."),
		row("agent-calls", "Agent calls", calls, "Let agents call shared instances. Both devices must grant access."),
		row("page:advanced", "Advanced", "Open", "Discovery scopes, operation receipts, and this device's identity."),
	}...)
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
	frame := tui.NewPanel(title, footer,
		func(s string) string { return th.Bold(th.FG("text", s)) },
		func(s string) string { return th.FG("dim", s) },
		func() string { return th.BGANSI("toolPendingBg") }, list)
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
	group := func() (string, error) {
		if len(status.Groups) == 1 {
			for id := range status.Groups {
				return id, nil
			}
		}
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
		count := 0
		for _, instance := range status.Instances {
			if instance.Group == g.GroupID {
				count++
			}
		}
		mode, ok, e := ui.Select(ctx, fmt.Sprintf("Share %s · %d current instances", status.Groups[g.GroupID], count), []string{"View conversations", "Control conversations"}, nil)
		if e != nil || !ok {
			return g, context.Canceled
		}
		g.Permissions = []string{"instance.list", "instance.inspect"}
		if mode == "Control conversations" {
			g.Permissions = append(g.Permissions, "instance.prompt", "instance.steer", "instance.follow_up", "instance.cancel", "instance.session.manage")
		}
		scope, ok, e := ui.Select(ctx, "Which conversations can this device access?", []string{"Current conversations only", "Current and future conversations"}, nil)
		if e != nil {
			return g, e
		}
		if !ok {
			return g, context.Canceled
		}
		g.IncludeFuture = scope == "Current and future conversations"
		return g, nil
	}
	peer, isPeer := strings.CutPrefix(action, "peer:")
	if isPeer {
		action = "Peers"
	}
	switch action {
	case "Status":
		ui.Notify(fmt.Sprintf("%s\n%d instances · %d peers · %d grants", status.PeerID, len(status.Instances), len(status.Peers), len(status.Grants)), extensions.NotifyInfo)
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
		return openSharedBridgeConversation(ctx, ui, profile, inv.PeerID, client)
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
		return openSharedBridgeConversation(ctx, ui, profile, peer, client)

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
		return approveBridgePairing(ctx, ui, client, inv, status.Groups)
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
		return openSharedBridgeConversation(ctx, ui, profile, peer, client)
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
			return inv, fmt.Errorf("invalid invitation; copy it again from Share this Orb")
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

func openSharedBridgeConversation(ctx context.Context, ui extensions.UI, profile, peer string, client *protocol.Conn) error {
	var catalog struct {
		Items []bridge.Instance `json:"items"`
	}
	if e := client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": "instances.list", "params": struct{}{}}, &catalog); e != nil {
		return e
	}
	labels := []string{}
	for _, r := range catalog.Items {
		labels = append(labels, r.Alias+" · "+r.ID)
	}
	if len(labels) == 0 {
		ui.Notify("Orb connected. Enable Bridge in a conversation on the other device to share it.", extensions.NotifyInfo)
		return nil
	}
	choice, ok, e := ui.Select(ctx, "Shared conversations", labels, nil)
	if e != nil || !ok {
		return e
	}
	_, instance, _ := strings.Cut(choice, " · ")
	return openBridgeView(ctx, ui, profile, peer, instance)
}

func shareBridgeInvitation(ctx context.Context, ui extensions.UI, client *protocol.Conn, inv bridge.Invitation, groups map[string]string) error {
	claimed, err := waitBridgePairing(ctx, ui, "Share this Orb", "Paste in Bridge → Connect to a device on the other Orb.", inv, bridgeInvitationCode(inv), func(ctx context.Context) (bridge.Invitation, error) {
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
		}, &extensions.CustomOptions{Overlay: true, StaticOverlayOptions: &extensions.OverlayOptions{Width: "85%", MinWidth: 32, MaxHeight: "85%", Backdrop: true}})
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
					notice = "Copied. Paste it in Connect to a device on the other Orb."
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
