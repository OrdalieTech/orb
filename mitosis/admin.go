package mitosis

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// Admin is deliberately absent from the remote routing table. Hosts expose it
// only after local-owner authentication.
func (b *Mitosis) Admin(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "status", "peers", "grants", "scopes", "instances":
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
			if i.Status != "approved" { // kept only so the claimant can read its approval
				pending = append(pending, i.Invitation)
			}
		}
		self := b.PeerID()
		peers := []string{}
		for id := range b.state.Peers {
			peers = append(peers, id)
		}
		for _, g := range b.state.Grants {
			if !slices.Contains(peers, g.Principal.PeerID) {
				peers = append(peers, g.Principal.PeerID)
			}
		}
		var names map[string]string
		if b.state.Group != "" {
			for _, id := range b.state.Scopes[b.state.Group] {
				if id == self {
					continue
				}
				if !slices.Contains(peers, id) {
					peers = append(peers, id)
				}
				if r, ok := b.state.Records[b.state.Group+"/"+id]; ok {
					if c, err := r.Verify(); err == nil && c.Name != "" {
						if names == nil {
							names = map[string]string{}
						}
						names[id] = c.Name
					}
				}
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
		return JSON(struct {
			PeerStates map[string]string   `json:"peer_states"`
			PeerNames  map[string]string   `json:"peer_names,omitempty"`
			PeerID     string              `json:"peer_id"`
			BootID     string              `json:"mitosis_boot_id"`
			Scopes     map[string][]string `json:"scopes"`
			Grants     []Grant             `json:"grants"`
			Instances  []Instance          `json:"instances"`
			Pending    []Invitation        `json:"pending"`
			Peers      []string            `json:"peers"`
		}{states, names, self, b.boot, b.state.Scopes, b.state.Grants, instances, pending, peers}), nil
	case "enroll":
		var p struct {
			Alias string `json:"alias"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, token, err := b.Enroll(p.Alias)
		return JSON(struct {
			Instance   Instance `json:"instance"`
			Credential string   `json:"credential"`
		}{r, token}), err
	case "retire":
		var p struct {
			IDs []string `json:"instance_ids,omitempty"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		removed, err := b.Retire(p.IDs)
		if err != nil {
			return nil, err
		}
		records := 0
		if len(p.IDs) == 0 {
			if records, err = b.PruneRecords(); err != nil {
				return nil, err
			}
		}
		res := map[string]int{"removed": removed}
		if records > 0 {
			res["records"] = records
		}
		return JSON(res), nil
	case "invite":
		var p struct {
			Grants []Grant `json:"grants"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		r, err := b.Invite(p.Grants)
		return JSON(r), err
	case "uninvite": // an invitation not yet approved, withdrawn (orb mitosis interrupted)
		var p struct {
			ID string `json:"invitation_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if i, ok := b.state.Invitations[p.ID]; ok && i.Status != "approved" {
			delete(b.state.Invitations, p.ID)
			return JSON(struct{}{}), b.save()
		}
		return JSON(struct{}{}), nil
	case "approve":
		var p struct {
			ID       string `json:"invitation_id"`
			Claimant string `json:"claimant"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return JSON(struct{}{}), b.Approve(p.ID, p.Claimant)
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
		return JSON(struct{}{}), b.Block(p.Peer)
	case "grant":
		var p Grant
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return JSON(struct{}{}), b.AddGrant(p)
	case "revoke":
		var p struct {
			ID string `json:"grant_id"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		if !protocol.ValidID(p.ID) {
			return nil, Fail("not_found")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		b.state.Grants = slices.DeleteFunc(b.state.Grants, func(g Grant) bool { return g.ID == p.ID })
		return JSON(struct{}{}), b.save()
	case "scope":
		var p struct {
			ID    string   `json:"scope_id"`
			Peers []string `json:"peers"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return JSON(struct{}{}), b.SetScope(p.ID, p.Peers)
	case "sync":
		return b.SyncAdmin(ctx, params, nil)
	case "notify":
		var p struct{}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		b.Notify()
		return JSON(struct{}{}), nil
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
		return JSON(struct{}{}), nil
	default:
		return nil, &protocol.RPCError{Code: -32601, Message: "method_not_found"}
	}
}
func (b *Mitosis) TransportState() json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append(json.RawMessage(nil), b.state.Transport...)
}
func (b *Mitosis) SaveTransportState(raw json.RawMessage) error {
	if err := protocol.Validate(raw); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state.Transport = append(json.RawMessage(nil), raw...)
	return b.save()
}
func (b *Mitosis) SavePeer(peer, locator string) error {
	if _, err := ParsePeerID(peer); err != nil {
		return err
	}
	if len(locator) > 8192 {
		return Fail("resource_exhausted")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Peers) >= MaxPeers {
		return Fail("resource_exhausted")
	}
	b.state.Peers[peer] = locator
	return b.save()
}
func (b *Mitosis) PeerLocator(peer string) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	locator := b.state.Peers[peer]
	// A Tailcat locator moves, so the peer's latest signed record wins over it; a WebSocket URL
	// it was paired at stays where it is, and may be the only way this device reaches it.
	webSocket := strings.HasPrefix(locator, "ws://") || strings.HasPrefix(locator, "wss://")
	for k, record := range b.state.Records {
		if webSocket || !strings.HasSuffix(k, "/"+peer) {
			continue
		}
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
		return "", Fail("not_found")
	}
	return locator, nil
}

// Outbound is the subject a call this agent's runtime instance makes to peer carries: the agent
// itself, when granted the call there.
func (b *Mitosis) Outbound(instance, peer, method string) (Subject, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	p := Principal{PeerID: b.PeerID(), Subject: Subject{Kind: "agent"}}
	if !b.allowed(p, "", permission(method), peer) {
		return Subject{}, Fail("unauthorized")
	}
	if _, ok := b.active[instance]; !ok {
		return Subject{}, Fail("unavailable")
	}
	return p.Subject, nil
}
func (b *Mitosis) Authorize(r Request) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowed(r.Principal, r.Call.InstanceID, permission(r.Call.Method), "")
}
