package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

func bridgeExtension(args CLIArgs, settings *config.SettingsManager) extensions.Factory {
	return func(api extensions.API) error {
		profile := args.BridgeProfile
		if profile == "" {
			profile = "personal"
		}
		api.On(extensions.EventSessionStart, func(ctx context.Context, _ extensions.Event, c extensions.Context) (any, error) {
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
			ui := c.UI()
			action, ok, err := ui.Select(ctx, "Bridge · "+profile, []string{"Status", "Start", "Stop", "Invite device", "Join device", "Approve pairing", "Peers", "Grant access", "Revoke access", "Instances", "Groups", "Discovery scopes", "Operation status"}, nil)
			if err != nil || !ok {
				return err
			}
			if action == "Start" {
				return startBridge(ctx, profile, true)
			}
			client, err := bridgeAdmin(ctx, profile)
			if err != nil {
				return err
			}
			defer func() { _ = client.Close() }()
			var status struct {
				PeerID    string              `json:"peer_id"`
				Groups    map[string]string   `json:"groups"`
				Scopes    map[string][]string `json:"scopes"`
				Instances []bridge.Instance   `json:"instances"`
				Pending   []bridge.Invitation `json:"pending"`
				Peers     []string            `json:"peers"`
				Grants    []bridge.Grant      `json:"grants"`
			}
			if err = client.Call(ctx, "status", struct{}{}, &status); err != nil {
				return err
			}
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
				v, ok, e := ui.Select(ctx, "Resource group", labels, nil)
				if !ok && e == nil {
					e = context.Canceled
				}
				return ids[v], e
			}
			choosePeer := func() (string, error) {
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
			switch action {
			case "Status":
				ui.Notify(fmt.Sprintf("%s\n%d instances · %d peers · %d grants", status.PeerID, len(status.Instances), len(status.Peers), len(status.Grants)), extensions.NotifyInfo)
			case "Stop":
				return client.Call(ctx, "stop", struct{}{}, nil)
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
				peer, e := choosePeer()
				if e != nil {
					return e
				}
				choice, ok, e := ui.Select(ctx, peer, []string{"Shared instances", "Block device"}, nil)
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
				choice, ok, e = ui.Select(ctx, "Shared instances", labels, nil)
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
		}})
		return nil
	}
}
