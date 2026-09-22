package bridge

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

// Admin is deliberately absent from the remote routing table. Hosts expose it
// only after local-owner authentication.
func (b *Bridge) Admin(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "status", "peers", "grants", "groups", "scopes", "instances":
		var p struct{}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		instances := []Instance{}
		for _, r := range b.state.Instances {
			r.Credential = ""
			_, r.Available = b.active[r.ID]
			instances = append(instances, r)
		}
		pending := []Invitation{}
		for _, i := range b.state.Invitations {
			pending = append(pending, i.Invitation)
		}
		peers := []string{}
		for id := range b.state.Peers {
			peers = append(peers, id)
		}
		for _, g := range b.state.Grants {
			if !slices.Contains(peers, g.Principal.PeerID) {
				peers = append(peers, g.Principal.PeerID)
			}
		}
		slices.Sort(peers)
		states := make(map[string]string, len(peers))
		for _, peer := range peers {
			states[peer] = "disconnected"
			if b.state.Blocked[peer] {
				states[peer] = "blocked"
			} else if b.connectionLocked(peer) != nil {
				states[peer] = "connected"
			}
		}
		return connect.JSON(struct {
			SupportsFullAccess bool                `json:"supports_full_access"`
			PeerStates         map[string]string   `json:"peer_states"`
			PeerID             string              `json:"peer_id"`
			BootID             string              `json:"bridge_boot_id"`
			Groups             map[string]string   `json:"groups"`
			Scopes             map[string][]string `json:"scopes"`
			Grants             []Grant             `json:"grants"`
			Instances          []Instance          `json:"instances"`
			Pending            []Invitation        `json:"pending"`
			Peers              []string            `json:"peers"`
		}{true, states, b.PeerID(), b.boot, b.state.Groups, b.state.Scopes, b.state.Grants, instances, pending, peers}), nil
	case "enroll":
		var p struct {
			Alias string `json:"alias"`
			Group string `json:"group_id,omitempty"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.Group == "" {
			p.Group = b.PersonalGroup()
		}
		r, token, err := b.Enroll(p.Alias, p.Group)
		return connect.JSON(struct {
			Instance   Instance `json:"instance"`
			Credential string   `json:"credential"`
		}{r, token}), err
	case "invite":
		var p struct {
			Grants []Grant `json:"grants"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, err := b.Invite(p.Grants)
		return connect.JSON(r), err
	case "approve":
		var p struct {
			ID       string `json:"invitation_id"`
			Claimant string `json:"claimant"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return connect.JSON(struct{}{}), b.Approve(p.ID, p.Claimant)
	case "block":
		var p struct {
			Peer string `json:"peer_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if _, err := ParsePeerID(p.Peer); err != nil {
			return nil, err
		}
		return connect.JSON(struct{}{}), b.Block(p.Peer)
	case "grant":
		var p Grant
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return connect.JSON(struct{}{}), b.AddGrant(p)
	case "revoke":
		var p struct {
			ID string `json:"grant_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if !protocol.ValidID(p.ID) {
			return nil, connect.Fail("not_found")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.state.Grants = slices.DeleteFunc(b.state.Grants, func(g Grant) bool { return g.ID == p.ID })
		return connect.JSON(struct{}{}), b.save()
	case "scope":
		var p struct {
			ID    string   `json:"scope_id"`
			Peers []string `json:"peers"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return connect.JSON(struct{}{}), b.SetScope(p.ID, p.Peers)
	case "group":
		var p struct {
			ID   string `json:"group_id,omitempty"`
			Name string `json:"name"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if p.ID == "" {
			p.ID = protocol.NewID()
		}
		if !protocol.ValidID(p.ID) || len(p.Name) > 128 || p.Name == "" {
			return nil, connect.Fail("not_found")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if len(b.state.Groups) >= 128 {
			return nil, connect.Fail("resource_exhausted")
		}
		b.state.Groups[p.ID] = p.Name
		return connect.JSON(p), b.save()
	case "assign":
		var p struct {
			Instance string `json:"instance_id"`
			Group    string `json:"group_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		r, ok := b.state.Instances[p.Instance]
		if !ok || b.state.Groups[p.Group] == "" {
			return nil, connect.Fail("not_found")
		}
		r.Group = p.Group
		b.state.Instances[p.Instance] = r
		return connect.JSON(struct{}{}), b.save()
	case "takeover":
		var p struct {
			Instance string `json:"instance_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		b.mu.Lock()
		r := b.active[p.Instance]
		delete(b.active, p.Instance)
		b.mu.Unlock()
		if r.endpoint != nil {
			_ = r.endpoint.Close()
		}
		return connect.JSON(struct{}{}), nil
	default:
		return nil, &protocol.RPCError{Code: -32601, Message: "method_not_found"}
	}
}
func (b *Bridge) TransportState() json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append(json.RawMessage(nil), b.state.Transport...)
}
func (b *Bridge) SaveTransportState(raw json.RawMessage) error {
	if _, err := protocol.Canonical(raw); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Transport = append(json.RawMessage(nil), raw...)
	return b.save()
}
func (b *Bridge) SavePeer(peer, locator string) error {
	if _, err := ParsePeerID(peer); err != nil {
		return err
	}
	if len(locator) > 8192 {
		return connect.Fail("resource_exhausted")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Peers) >= 128 {
		return connect.Fail("resource_exhausted")
	}
	b.state.Peers[peer] = locator
	return b.save()
}
func (b *Bridge) PeerLocator(peer string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	locator := b.state.Peers[peer]
	for _, record := range b.state.Records {
		contact, err := record.Verify()
		if err == nil && contact.Peer == peer && !contact.Withdrawn && len(b.state.Scopes[contact.Scope]) != 0 {
			for _, endpoint := range contact.Endpoints {
				if endpoint.Transport == "tailcat/1" {
					locator = endpoint.Locator
					break
				}
			}
		}
	}
	if b.closed || b.failed || locator == "" || b.state.Blocked[peer] {
		return "", connect.Fail("not_found")
	}
	return locator, nil
}
func (b *Bridge) Outbound(instance, peer string, call connect.Call) (connect.Subject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := connect.Principal{PeerID: b.PeerID(), Subject: connect.Subject{Kind: "instance", InstanceID: instance}}
	if !b.allowed(p, call.InstanceID, permission(call.Method), peer) {
		return connect.Subject{}, connect.Fail("unauthorized")
	}
	if _, ok := b.active[instance]; !ok {
		return connect.Subject{}, connect.Fail("unavailable")
	}
	return p.Subject, nil
}
func (b *Bridge) Authorize(r connect.Request) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowed(r.Principal, r.Call.InstanceID, permission(r.Call.Method), "")
}
