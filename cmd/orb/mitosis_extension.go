package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"path"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent/clipboard"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/internal/qr"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/native/mitosis/daemon"
	"github.com/OrdalieTech/orb/tui"
)

func mitosisExtension(args CLIArgs, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		profile := args.MitosisProfile
		if profile == "" {
			profile = "personal"
		}
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, c extensions.Context) (any, error) {
			if args.native != nil {
				ctx = context.WithValue(ctx, nativeStateKey{}, args.native)
			}
			// The footer dot is TUI chrome; other modes get no status frames.
			status := func(on bool) {
				if c.Mode() == extensions.ModeTUI {
					setMitosisStatus(c.UI(), on)
				}
			}
			if args.MitosisProfile == "" && !settings.GetPlugins()["mitosis"] {
				status(false)
				return nil, nil
			}
			if err := startMitosis(ctx, profile, false); err != nil {
				status(false)
				return nil, nil
			}
			if err := args.mitosisLink.configureMitosis(true); err != nil {
				return nil, err
			}
			status(true)
			return nil, nil
		})
		api.RegisterCommand("mitosis", extensions.Command{SettingsLabel: "Mitosis", Description: "Connect devices and open their conversations", Handler: func(ctx context.Context, _ string, c extensions.CommandContext) error {
			if args.native != nil {
				ctx = context.WithValue(ctx, nativeStateKey{}, args.native)
			}
			if c.Mode() != extensions.ModeTUI {
				return fmt.Errorf("mitosis administration requires the local TUI")
			}
			return mitosisSettingsWindow(ctx, c, args, settings, profile)
		}})
		return nil
	}
}

// ownerMitosis is how the agents plugin reaches Mitosis: as its owner does from
// /mitosis, while Mitosis is on, without ever starting it.
type ownerMitosis struct {
	args     CLIArgs
	settings *config.SettingsManager
}

func (b ownerMitosis) admin(ctx context.Context) (*protocol.Conn, error) {
	if b.args.native == nil || b.args.MitosisProfile == "" && !b.settings.GetPlugins()["mitosis"] {
		return nil, mitosis.Fail("unavailable")
	}
	return daemon.Admin(ctx, b.args.native, cmp.Or(b.args.MitosisProfile, "personal"))
}

func (b ownerMitosis) Machines(ctx context.Context) ([]string, error) {
	admin, err := b.admin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = admin.Close() }()
	var status mitosisSettingsStatus
	if err := admin.Call(ctx, "status", struct{}{}, &status); err != nil {
		return nil, err
	}
	machines := []string{status.PeerID}
	for peer, state := range status.PeerStates {
		if state == "connected" {
			machines = append(machines, peer)
		}
	}
	slices.Sort(machines[1:])
	return machines, nil
}

func (b ownerMitosis) Call(ctx context.Context, peer, method string, params, result any) error {
	admin, err := b.admin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = admin.Close() }()
	return admin.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": method, "params": params}, result)
}

func (b ownerMitosis) Self() string { return b.args.mitosisLink.self() }

// others is what this agent's other conversations on Mitosis are doing, counted as the apps'
// summary counts them: "1 asks · 2 working", "" when they are quiet or this Orb is not on Mitosis.
func (b ownerMitosis) others(ctx context.Context) string {
	self := b.Self()
	if self == "" {
		return ""
	}
	admin, err := b.admin(ctx)
	if err != nil {
		return ""
	}
	defer func() { _ = admin.Close() }()
	var status struct {
		PeerID string `json:"peer_id"`
	}
	var agent struct {
		Items []mitosis.Conversation `json:"items"`
	}
	if admin.Call(ctx, "status", struct{}{}, &status) != nil || admin.Call(ctx, "remote", map[string]any{"peer_id": status.PeerID, "method": "agent.describe", "params": struct{}{}}, &agent) != nil {
		return ""
	}
	asks, working := 0, 0
	for _, c := range agent.Items {
		switch {
		case c.SessionID == self:
		case c.State == "asking":
			asks++
		case c.State == "running":
			working++
		}
	}
	var parts []string
	if asks > 0 {
		parts = append(parts, fmt.Sprintf("%d asks", asks))
	}
	if working > 0 {
		parts = append(parts, fmt.Sprintf("%d working", working))
	}
	return strings.Join(parts, " · ")
}

// setMitosisStatus shows Mitosis as one footer dot. The status key names the
// command, so clicking the dot opens /mitosis.
func setMitosisStatus(ui extensions.UI, on bool) {
	dot := "○"
	if on {
		dot = ui.Theme().FG("success", "●")
	}
	ui.SetStatus("mitosis", &dot)
}

type mitosisSettingsStatus struct {
	PeerStates map[string]string    `json:"peer_states"`
	PeerID     string               `json:"peer_id"`
	Pending    []mitosis.Invitation `json:"pending"`
	Peers      []string             `json:"peers"`
	Grants     []mitosis.Grant      `json:"grants"`
}

func setMitosisSetting(settings *config.SettingsManager, name string, enabled bool) error {
	if settings.ProjectDefinesPlugin(name) {
		return fmt.Errorf("%s is configured in project settings; edit .orb/settings.json", name)
	}
	settings.SetPluginEnabled(name, enabled)
	for _, err := range settings.DrainErrors() {
		return err
	}
	return nil
}

func mitosisSettingsWindow(ctx context.Context, c extensions.CommandContext, args CLIArgs, settings *config.SettingsManager, profile string) (err error) {
	ui := c.UI()
	page, selected, notice := "", "", ""
	reload := false
	defer func() {
		if reload {
			err = errors.Join(err, c.Reload(ctx))
		}
	}()
	for ctx.Err() == nil {
		client, status, probeErr := probeMitosis(ctx, profile)
		running := probeErr == nil
		if client != nil {
			_ = client.Close()
		}
		result, ok, menuErr := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			enabled, agentCalls := args.MitosisProfile != "" || settings.GetPlugins()["mitosis"], settings.GetPlugins()["mitosis-agent-calls"]
			rows := mitosisSettingsRows(page, running, enabled, agentCalls, status, th)
			panel := newMitosisSettingsPanel(profile, page, selected, rows, th, host.Height, done)
			page, notice, status := page, notice, status
			panel.watch(ctx, host, func(ctx context.Context) []tui.GridRow {
				client, current, err := probeMitosis(ctx, profile)
				if client != nil {
					_ = client.Close()
				}
				if err == nil {
					status = current
				}
				rows := mitosisSettingsRows(page, err == nil, enabled, agentCalls, status, th)
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
		client, status, probeErr = probeMitosis(ctx, profile)
		running = probeErr == nil
		actionErr := func() error {
			defer func() {
				if client != nil {
					_ = client.Close()
				}
			}()
			enable := func() error {
				if err := setMitosisSetting(settings, "mitosis", true); err != nil {
					return err
				}
				ui.Notify("Starting Mitosis…", extensions.NotifyInfo)
				if err := startMitosis(ctx, profile, true); err != nil {
					return err
				}
				if err := args.mitosisLink.configureMitosis(true); err != nil {
					return err
				}
				setMitosisStatus(ui, true)
				return nil
			}
			if action == "Invite device" || action == "Join device" || action == "SSH" {
				if !running || args.MitosisProfile == "" && !settings.GetPlugins()["mitosis"] {
					var err error
					if running && (args.MitosisProfile != "" || settings.GetPlugins()["mitosis"]) {
						err = startMitosis(ctx, profile, true)
					} else {
						err = enable()
					}
					if err != nil {
						return err
					}
					if client != nil {
						_ = client.Close()
					}
					client, err = mitosisAdmin(ctx, profile)
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
					return fmt.Errorf("mitosis is already offline")
				}
				if err := setMitosisSetting(settings, "mitosis", false); err != nil {
					return err
				}
				if err := client.Call(ctx, "stop", struct{}{}, nil); err != nil {
					return err
				}
				if err := args.mitosisLink.configureMitosis(false); err != nil {
					return err
				}
				setMitosisStatus(ui, false)
				if err := daemon.WaitStopped(ctx, client); err != nil {
					return err
				}
				selected = "Start"
				return nil
			case "agent-calls":
				if err := setMitosisSetting(settings, "mitosis-agent-calls", !settings.GetPlugins()["mitosis-agent-calls"]); err != nil {
					return err
				}
				reload = true
				notice = "Agent tools update when you close Mitosis."
				return nil
			default:
				if !running {
					return fmt.Errorf("mitosis is offline; start it to reconnect")
				}
				return mitosisSettingsAction(ctx, ui, profile, action, client, status)
			}
		}()
		if actionErr != nil && !errors.Is(actionErr, context.Canceled) {
			notice = actionErr.Error()
		}
	}
	return ctx.Err()
}

// probeMitosis asks the running Mitosis for its status, giving up after a second.
func probeMitosis(ctx context.Context, profile string) (*protocol.Conn, mitosisSettingsStatus, error) {
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	var status mitosisSettingsStatus
	client, err := mitosisAdmin(probe, profile)
	if err == nil {
		err = client.Call(probe, "status", struct{}{}, &status)
	}
	return client, status, err
}

func mitosisSettingsRows(page string, running, enabled, agentCalls bool, status mitosisSettingsStatus, th extensions.Theme) []tui.GridRow {
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
		rows := []tui.GridRow{row("agent-calls", "Agent calls", calls, "Let agents list, read and message your other conversations, here and on connected devices.")}
		if running {
			rows = append(rows, row("Status", "This device's fingerprint", "", status.PeerID))
		}
		return rows
	}
	service := row("Start", "Enable Mitosis", "○ Off", "Connect your devices to share conversations. Local work continues when Mitosis is off.")
	if running && !enabled {
		service = row("Start", "Share this conversation", "Mitosis on", "Attach this conversation and enable Mitosis for future launches.")
	}
	if running && enabled {
		service = row("Stop", "Mitosis", th.FG("success", "● On"), "Turn off remote access. Your devices stay saved for next time.")
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
		rows = append(rows, row("peer:"+peer, mitosisDeviceLabel(peer), state, "Open this device's conversations. Availability updates automatically."))
	}
	return append(rows, row("page:advanced", "Advanced", "", "Agent tools and this device's fingerprint."))
}

func mitosisDeviceLabel(peer string) string {
	fingerprint := strings.TrimPrefix(peer, "orb:ed25519:")
	return "Device " + fingerprint[:min(10, len(fingerprint))]
}

type mitosisSettingsPanel struct {
	*tui.Frame
	mu      sync.Mutex
	list    *tui.GridList
	height  func() int
	pending func()
	cancel  context.CancelFunc
}

func newMitosisSettingsPanel(profile, page, selected string, rows []tui.GridRow, th extensions.Theme, height func() int, done extensions.CustomDone) *mitosisSettingsPanel {
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
	panel := &mitosisSettingsPanel{list: list, height: height}
	list.OnConfirm = func(value string) { panel.pending = func() { done(value) } }
	list.OnCancel = func() { panel.pending = func() { done(nil) } }
	title := "Mitosis"
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
func (p *mitosisSettingsPanel) watch(ctx context.Context, host extensions.UIHost, load func(context.Context) []tui.GridRow) {
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

func (p *mitosisSettingsPanel) Render(width int) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.list.SetMaxVisible(max(1, min(10, p.height()-12)))
	return p.Frame.Render(width)
}
func (p *mitosisSettingsPanel) HandleInput(key tui.KeyEvent) {
	p.mu.Lock()
	defer p.unlockAndDispatch()
	p.Frame.HandleInput(key)
}
func (p *mitosisSettingsPanel) HandleMouse(event tui.MouseEvent) bool {
	p.mu.Lock()
	defer p.unlockAndDispatch()
	return p.Frame.HandleMouse(event)
}
func (p *mitosisSettingsPanel) SetFocused(focused bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Frame.SetFocused(focused)
}

func (p *mitosisSettingsPanel) unlockAndDispatch() {
	action := p.pending
	p.pending = nil
	p.mu.Unlock()
	if action != nil {
		action()
	}
}

func (p *mitosisSettingsPanel) Dispose() {
	if p.cancel != nil {
		p.cancel()
	}
}

func mitosisSettingsAction(ctx context.Context, ui extensions.UI, profile, action string, client *protocol.Conn, status mitosisSettingsStatus) error {
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
		var inv mitosis.Invitation
		if e := client.Call(ctx, "invite", map[string]any{"grants": []mitosis.Grant{mitosis.FullGrant("")}}, &inv); e != nil {
			return e
		}

		return shareMitosisInvitation(ctx, ui, client, inv)
	case "Join device":
		text, e := input("Connect to a device · paste its invitation")
		if e != nil {
			return e
		}
		inv, e := parseMitosisCode(ctx, text)
		if e != nil {
			return e
		}
		ok, e := ui.Confirm(ctx, "Trust this Orb?", inv.PeerID+"\nAllow control of your current and future conversations.\nVerify this fingerprint on the sharing Orb.", nil)
		if e != nil || !ok {
			return e
		}
		var claimed mitosis.Invitation
		if e = client.Call(ctx, "join", inv, &claimed); e != nil {
			return e
		}
		inv.Claimant = status.PeerID
		_, e = waitMitosisPairing(ctx, ui, "Connect to a device", "Waiting for approval on the sharing device.", inv, "", func(ctx context.Context) (mitosis.Invitation, error) {
			var result mitosis.Invitation
			err := client.Call(ctx, "remote", map[string]any{"peer_id": inv.PeerID, "method": "pair.status", "params": map[string]string{"invitation_id": inv.ID}}, &result)
			return result, err
		}, func(i mitosis.Invitation) bool { return i.Status == "approved" })
		if e != nil {
			return e
		}
		if e = trustMitosisPeer(ctx, client, profile, inv.PeerID, false); e != nil {
			return e
		}
		if err := client.Call(ctx, "sync", struct{}{}, nil); mitosis.Code(err) == "not_found" {
			_ = client.Call(ctx, "sync", map[string]string{"join": inv.PeerID}, nil)
		}
		ui.Notify("Device connected. Choose a shared conversation.", extensions.NotifyInfo)
		return openSharedMitosisConversation(ctx, ui, profile, inv.PeerID)
	case "SSH":
		target, e := input("Connect using SSH · user@host or SSH alias")
		if e != nil {
			return e
		}
		ok, e := ui.Confirm(ctx, "Trust "+target+"?", "Share full control of current and future conversations, and starting Orb\nin any folder, in both directions. Orb will be installed or updated on the server if needed.", nil)
		if e != nil || !ok {
			return e
		}
		ui.Notify("Setting up Orb on "+target+"…", extensions.NotifyInfo)
		peer, e := connectMitosisSSH(ctx, client, profile, status.PeerID, target, "personal", "orb")
		if e != nil && mitosis.Code(e) != "unauthorized" {
			return e
		}
		ui.Notify("Server paired through SSH.", extensions.NotifyInfo)
		return openSharedMitosisConversation(ctx, ui, profile, peer)

	case "Approve pairing":
		choices := []string{}
		claims := map[string]mitosis.Invitation{}
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
		return approveMitosisPairing(ctx, ui, client, claims[choice])
	case "Peers":
		if status.PeerStates[peer] == "blocked" {
			return fmt.Errorf("this device is blocked.\nIts access has been revoked")
		}
		return openSharedMitosisConversation(ctx, ui, profile, peer)
	}
	return nil
}

func mitosisConversationRows(ctx context.Context, client *protocol.Conn, peer string, th extensions.Theme) ([]tui.GridRow, error) {
	rows := []tui.GridRow{}
	cursor, count := "", 0
	for count < 4096 {
		var catalog struct {
			Items  []mitosis.Conversation `json:"items"`
			Cursor string                 `json:"cursor"`
		}
		if err := client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": "agent.describe", "params": map[string]string{"cursor": cursor}}, &catalog); err != nil {
			return nil, err
		}
		count += len(catalog.Items)
		if count > 4096 || len(catalog.Items) == 0 && catalog.Cursor != "" {
			return nil, mitosis.Fail("resource_exhausted")
		}
		for _, c := range catalog.Items {
			label := peerText(cmp.Or(c.Title, path.Base(c.CWD)), false)
			if c.Title == "" && c.CWD == "" {
				label = "Conversation"
			}
			rows = append(rows, tui.GridRow{Value: c.SessionID, Cells: []string{th.FG("text", label)}, Detail: []string{"Open conversation"}})
		}
		if catalog.Cursor == "" {
			if len(rows) == 0 {
				rows = append(rows, tui.GridRow{Header: true, Cells: []string{th.FG("muted", "No conversations open yet")}}, tui.GridRow{Header: true, Cells: []string{th.FG("dim", "Open Orb on this device with Mitosis enabled.")}})
			}
			return rows, nil
		}
		if catalog.Cursor == cursor {
			break
		}
		cursor = catalog.Cursor
	}
	return nil, mitosis.Fail("resource_exhausted")
}

func openSharedMitosisConversation(ctx context.Context, ui extensions.UI, profile, peer string) error {
	cache, closeCache, cacheErr := daemon.Cache(ctx, stateFromContext(ctx), profile)
	if cacheErr == nil {
		defer closeCache()
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
			rows = append(rows, tui.GridRow{Value: "cached:" + string(mitosis.JSON([2]string{entry.Namespace, entry.ID})), Cells: []string{th.FG("muted", peerText(name, false))}, Detail: []string{detail}})
		}
		return rows
	}
	selected := ""
	for ctx.Err() == nil {
		result, ok, err := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			panel := newMitosisSettingsPanel(profile, "", "", []tui.GridRow{{Header: true, Cells: []string{"Connecting…"}}}, th, host.Height, done)
			panel.Title = mitosisDeviceLabel(peer)
			if rows := cachedRows(th, nil); len(rows) > 0 {
				panel.list.SetRows(rows)
			}
			preferred := selected
			panel.watch(ctx, host, func(ctx context.Context) []tui.GridRow {
				probe, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				client, err := mitosisAdmin(probe, profile)
				var rows []tui.GridRow
				if err == nil {
					rows, err = mitosisConversationRows(probe, client, peer, th)
					_ = client.Close()
				}
				if err != nil {
					if mitosis.Code(err) == "unauthorized" && cache != nil {
						_ = cache.Forget(ctx, peer)
					}
					message, detail := "Reconnecting…", "Keep Mitosis on at both devices. Retrying automatically."
					switch mitosis.Code(err) {
					case "unauthorized":
						message, detail = "Access unavailable", "Check that this device has granted access to your Orb."
					case "resource_exhausted":
						message, detail = "Too many conversations", "This device's conversation list exceeds the Mitosis limit."
					}
					rows = []tui.GridRow{{Header: true, Cells: []string{th.FG("warning", message)}}, {Header: true, Cells: []string{th.FG("dim", detail)}}}
					if mitosis.Code(err) != "unauthorized" {
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
				rows = append(rows, tui.GridRow{Value: "folder", Cells: []string{"Open a folder…"}, Detail: []string{"Start Orb in a folder on this device: a new thread, or one of its past threads."}},
					tui.GridRow{Value: "disconnect", Cells: []string{th.FG("muted", "Block device")}, Detail: []string{"Block this device and revoke its access to your conversations."}})
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
			client, err := mitosisAdmin(ctx, profile)
			if err != nil {
				return err
			}
			err = client.Call(ctx, "block", map[string]string{"peer_id": peer}, nil)
			_ = client.Close()
			return err
		}
		if selected == "folder" {
			session, err := openMitosisFolder(ctx, ui, profile, peer)
			if err != nil {
				ui.Notify(err.Error(), extensions.NotifyError)
			} else if session != "" {
				if err := openMitosisView(ctx, ui, remoteLink{profile: profile, peer: peer, session: session}); err != nil {
					return err
				}
			}
			continue
		}
		if id, ok := strings.CutPrefix(selected, "cached:"); ok {
			if cache != nil {
				entries, err := cache.List(ctx, peer)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					if string(mitosis.JSON([2]string{entry.Namespace, entry.ID})) == id {
						if err := openMitosisView(ctx, ui, remoteLink{profile: profile, peer: peer, session: entry.Instance}, entry); err != nil {
							return err
						}
						break
					}
				}
			}
			continue
		}
		if selected != "" {
			if err := openMitosisView(ctx, ui, remoteLink{profile: profile, peer: peer, session: selected}); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func shareMitosisInvitation(ctx context.Context, ui extensions.UI, client *protocol.Conn, inv mitosis.Invitation) error {
	claimed, err := waitMitosisPairing(ctx, ui, "Create invitation", "Paste in Mitosis → Add device → Paste an invitation on the other Orb.", inv, mitosis.InvitationCode(inv), func(ctx context.Context) (mitosis.Invitation, error) {
		var status mitosisSettingsStatus
		if err := client.Call(ctx, "status", struct{}{}, &status); err != nil {
			return mitosis.Invitation{}, err
		}
		for _, item := range status.Pending {
			if item.ID == inv.ID {
				return item, nil
			}
		}
		return mitosis.Invitation{}, fmt.Errorf("invitation expired; create a new one")
	}, func(i mitosis.Invitation) bool { return i.Claimant != "" })
	if err != nil {
		return err
	}
	return approveMitosisPairing(ctx, ui, client, claimed)
}

func approveMitosisPairing(ctx context.Context, ui extensions.UI, client *protocol.Conn, claimed mitosis.Invitation) error {
	details := "Verify this fingerprint on the joining device:\n" + claimed.Claimant
	for _, g := range claimed.Grants {
		if slices.Equal(g.Permissions, mitosis.FullGrant("").Permissions) {
			details += "\nAllow full control of this agent's conversations,\nand starting Orb in any folder on this machine."
			continue
		}
		details += "\n" + strings.Join(g.Permissions, ", ")
	}
	yes, err := ui.Confirm(ctx, "Trust this Orb?", details, nil)
	if err != nil || !yes {
		return err
	}
	if err = client.Call(ctx, "approve", map[string]string{"invitation_id": claimed.ID, "claimant": claimed.Claimant}, nil); err != nil {
		return err
	}
	if err = client.Call(ctx, "sync", map[string]string{"add": claimed.Claimant}, nil); err != nil && mitosis.Code(err) != "unauthorized" && mitosis.Code(err) != "unavailable" {
		ui.Notify("Device paired, but sync group update failed: "+err.Error(), extensions.NotifyWarning)
		return nil
	}
	ui.Notify("Device paired. It can now open the conversations you shared.", extensions.NotifyInfo)
	return nil
}

func waitMitosisPairing(ctx context.Context, ui extensions.UI, title, instruction string, inv mitosis.Invitation, code string, poll func(context.Context) (mitosis.Invitation, error), ready func(mitosis.Invitation) bool) (mitosis.Invitation, error) {
	ctx, cancel := context.WithDeadline(ctx, time.Unix(inv.Expires, 0))
	defer cancel()
	notice := "Invitations expire after 10 minutes. Escape closes this screen."
	var shown []string // the QR code, once asked for: a phone scans it straight off the terminal
	for ctx.Err() == nil {
		result, ok, err := ui.Custom(ctx, func(host extensions.UIHost, th extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
			rows := []tui.GridRow{}
			if code != "" {
				rows = append(rows, tui.GridRow{Value: "qr", Cells: []string{map[bool]string{true: "Hide QR code", false: "Show QR code"}[shown != nil]}, Detail: []string{"Scan it with the Orb app.", notice}},
					tui.GridRow{Value: "copy", Cells: []string{"Copy invitation"}, Detail: []string{instruction, notice}}, tui.GridRow{Value: "show", Cells: []string{"Show invitation"}, Detail: []string{instruction, notice}})
			} else {
				rows = append(rows, tui.GridRow{Value: "fingerprint", Cells: []string{"Show this device's fingerprint"}, Detail: []string{instruction, inv.Claimant}})
			}
			panel := newMitosisSettingsPanel(title, "", "", rows, th, host.Height, done)
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
			if shown != nil {
				return &qrPanel{panel, shown}, nil
			}
			return panel, nil
		}, extensions.ModalOptions())
		if err != nil {
			return mitosis.Invitation{}, err
		}
		if !ok || result == nil {
			return mitosis.Invitation{}, context.Canceled
		}
		switch value := result.(type) {
		case mitosis.Invitation:
			return value, nil
		case error:
			return mitosis.Invitation{}, value
		case string:
			switch value {
			case "qr":
				if shown != nil {
					shown = nil
				} else if q, err := qr.Encode([]byte(code)); err == nil {
					shown = strings.Split(strings.TrimSuffix(q.Terminal(), "\n"), "\n")
				}
			case "copy":
				if err := clipboard.CopyToClipboard(code); err != nil {
					notice = "Copy unavailable. Choose Show invitation to copy it manually."
				} else {
					notice = "Copied. Paste it in Add device → Paste an invitation on the other Orb."
				}
			case "fingerprint":
				if _, _, err := ui.Editor(ctx, "Your fingerprint · verify on the sharing device", &inv.Claimant); err != nil {
					return mitosis.Invitation{}, err
				}
			case "show":
				if _, _, err := ui.Editor(ctx, "Private invitation · copy the complete text", &code); err != nil {
					return mitosis.Invitation{}, err
				}
			default:
				return mitosis.Invitation{}, context.Canceled
			}
		}
	}
	return mitosis.Invitation{}, fmt.Errorf("invitation expired or pairing was cancelled; create a new invitation to retry")
}

// qrPanel shows a QR code under a panel, or says how wide the terminal needs to be.
type qrPanel struct {
	*mitosisSettingsPanel
	lines []string
}

func (p *qrPanel) Render(width int) []string {
	lines := p.mitosisSettingsPanel.Render(width)
	if need := tui.VisibleWidth(p.lines[0]); need > width {
		return append(lines, fmt.Sprintf("Widen the terminal to %d columns to show the QR code.", need))
	}
	return append(lines, p.lines...)
}

// openMitosisFolder picks a folder on the peer — one its threads live in, or any other — then a
// new thread or a past one there, and starts Orb on it (host.launch). It returns that thread.
func openMitosisFolder(ctx context.Context, ui extensions.UI, profile, peer string) (string, error) {
	client, err := mitosisAdmin(ctx, profile)
	if err != nil {
		return "", err
	}
	defer func() { _ = client.Close() }()
	remote := func(method string, params, result any) error {
		err := client.Call(ctx, "remote", map[string]any{"peer_id": peer, "method": method, "params": params}, result)
		if mitosis.Code(err) == "unauthorized" {
			var status mitosisSettingsStatus
			_ = client.Call(ctx, "status", struct{}{}, &status)
			return fmt.Errorf("this device has not allowed starting Orb on it.\nOn it, run: orb mitosis trust %s", status.PeerID)
		}
		return err
	}
	type thread struct {
		ID       string `json:"session_id"`
		Name     string `json:"name"`
		CWD      string `json:"cwd"`
		Modified int64  `json:"modified"`
		First    string `json:"first"`
	}
	var threads []thread
	cursor := ""
	for range 32 {
		var page struct {
			Items  []thread `json:"items"`
			Cursor string   `json:"cursor"`
		}
		params := map[string]string{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		if err := remote("host.sessions", params, &page); err != nil {
			return "", err
		}
		threads = append(threads, page.Items...)
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	// Folders in the order they were last worked in.
	slices.SortFunc(threads, func(a, b thread) int { return cmp.Compare(b.Modified, a.Modified) })
	folders, count := []string{}, map[string]int{}
	for _, t := range threads {
		if count[t.CWD]++; count[t.CWD] == 1 {
			folders = append(folders, t.CWD)
		}
	}
	choices := []string{}
	for _, f := range folders {
		choices = append(choices, fmt.Sprintf("%s · %d thread%s", peerText(f, false), count[f], map[bool]string{true: "", false: "s"}[count[f] == 1]))
	}
	choices = append(choices, "Other folder…")
	choice, ok, err := ui.Select(ctx, "Open a folder on "+mitosisDeviceLabel(peer), choices, nil)
	if err != nil || !ok {
		return "", err
	}
	folder := ""
	if i := slices.Index(choices, choice); i < len(folders) {
		folder = folders[i]
	} else if folder, ok, err = ui.Input(ctx, "Folder on that device · an absolute path or ~/…", nil, nil); err != nil || !ok || strings.TrimSpace(folder) == "" {
		return "", err
	}
	folder = strings.TrimSpace(folder)
	options, ids := []string{"New thread"}, []string{""}
	for _, t := range threads {
		if t.CWD != folder {
			continue
		}
		title := peerText(cmp.Or(t.Name, strings.Join(strings.Fields(t.First), " "), t.ID), false)
		options = append(options, tui.TruncateToWidth(title, 70, "…", false)+" · "+time.UnixMilli(t.Modified).Format("Jan 2 15:04"))
		ids = append(ids, t.ID)
	}
	choice, ok, err = ui.Select(ctx, peerText(folder, false), options, nil)
	if err != nil || !ok {
		return "", err
	}
	params := map[string]string{"cwd": folder}
	if id := ids[slices.Index(options, choice)]; id != "" {
		params = map[string]string{"session_id": id}
	}
	ui.Notify("Starting Orb in "+peerText(folder, false)+"…", extensions.NotifyInfo)
	var launched struct {
		SessionID string `json:"session_id"`
	}
	if err := remote("host.launch", params, &launched); err != nil {
		return "", err
	}
	return launched.SessionID, nil
}
