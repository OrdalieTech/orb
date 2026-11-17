package mitosis

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// Peers keeps a Mitosis's channels to its peers over the transports its host
// supplies: it dials them, retries a call once on a fresh channel, pings
// channels so dead ones close, greets known peers at start, exchanges
// contact records with scope members, and serves the owner calls that need
// the network (invite, join, remote, publish).
type Peers struct {
	Mitosis *Mitosis
	// Dial opens a stream to peer at locator.
	Dial func(ctx context.Context, peer, locator string) (net.Conn, error)
	// Locator is where peers reach this Mitosis; WebURL, when set, replaces it
	// in invitations.
	Locator func() (string, error)
	WebURL  string
	// Forget drops what this machine keeps of a peer the owner blocked.
	Forget func(ctx context.Context, peer string) error

	ctx context.Context
	mu  sync.Mutex
	// joining: inviters this Mitosis claimed an invitation from, kept as known peers only once
	// trusted, so one that never approves is not dialled at every start.
	joining map[string]string
	used    sync.Map // peer -> when a call last went to it, so the ring keeps a channel in use
}

// NewPeers serves b's peers until ctx ends.
func NewPeers(ctx context.Context, b *Mitosis) *Peers {
	return &Peers{Mitosis: b, ctx: ctx, joining: map[string]string{}}
}

func (ps *Peers) Peer(ctx context.Context, id, locator string) (*protocol.Conn, error) {
	if c := ps.Mitosis.Connection(id); c != nil {
		return c, nil
	}
	if locator == "" {
		ps.mu.Lock()
		locator = ps.joining[id]
		ps.mu.Unlock()
	}
	if locator == "" {
		var err error
		locator, err = ps.Mitosis.PeerLocator(id)
		if err != nil {
			return nil, err
		}
	}
	timeout, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Dial, unless the peer gets here first: one back from a network cut dials in by itself,
	// often long before a dial toward it would get through.
	dialed := make(chan error, 1)
	go func() { dialed <- ps.dial(timeout, id, locator) }()
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-dialed:
			// The channel dialled may have given way to one the peer dialled at the same time,
			// even before its greeting was over.
			if c := ps.Mitosis.Connection(id); c != nil {
				return c, nil
			}
			return nil, cmp.Or(err, Fail("unavailable"))
		case <-tick.C:
			if c := ps.Mitosis.Connection(id); c != nil {
				return c, nil
			}
		case <-timeout.Done():
			return nil, Fail("unavailable")
		}
	}
}

func (ps *Peers) dial(ctx context.Context, id, locator string) error {
	stream, err := ps.Dial(ctx, id, locator)
	if err != nil {
		return err
	}
	c, _, err := ps.Mitosis.Connect(ctx, stream, id, false)
	if err != nil {
		return err
	}
	go func() {
		opened := time.Now()
		Keepalive(ps.ctx, c, 4*time.Second)
		// A paired peer whose channel from here died after a while (it restarted, say) is dialled
		// again at once, where the next sweep could take 30 s; one that drops the channel at once
		// waits for the sweep, and a channel kept the other way stops it.
		if ps.ctx.Err() == nil && time.Since(opened) > time.Minute && ps.paired(id) && !ps.ringed(id) {
			ctx, cancel := context.WithTimeout(ps.ctx, 20*time.Second)
			_, _ = ps.Peer(ctx, id, "")
			cancel()
		}
	}()
	return nil
}

// ringed reports whether the ring links this device to peer: a member of its sync group reached
// over Tailcat (a Worker, reached over WebSocket, cannot dial, so its peers greet it).
func (ps *Peers) ringed(peer string) bool {
	ps.Mitosis.mu.Lock()
	member := slices.Contains(ps.Mitosis.state.Scopes[ps.Mitosis.state.Group], peer)
	ps.Mitosis.mu.Unlock()
	locator, _ := ps.Mitosis.PeerLocator(peer)
	return member && strings.HasPrefix(locator, "tc")
}

func (ps *Peers) paired(id string) bool {
	raw, err := ps.Mitosis.Admin(ps.ctx, "peers", JSON(struct{}{}))
	var status struct {
		Peers  []string          `json:"peers"`
		States map[string]string `json:"peer_states"`
	}
	return err == nil && json.Unmarshal(raw, &status) == nil && slices.Contains(status.Peers, id) && status.States[id] != "blocked"
}
func (ps *Peers) Remote(ctx context.Context, id, method string, p any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// The owner reaches its own machine with the same calls, served here: one client for every Orb.
	if id == ps.Mitosis.PeerID() {
		params, err := json.Marshal(p)
		if err != nil {
			return nil, err
		}
		return ps.Mitosis.Handle(ctx, id, method, params)
	}
	// A dead connection fails, or is superseded by the channel a restarted peer opens to greet
	// this one; one retry reaches the peer's fresh channel or redials. Only calls safe to repeat
	// are retried: reads, and instances.call, which the peer deduplicates by operation id.
	retries := 1
	if method == "host.launch" || method == "host.terminal.open" || method == "host.terminal.write" {
		retries = 0
	}
	ps.used.Store(id, time.Now())
	for attempt := 0; ; attempt++ {
		c, err := ps.Peer(ctx, id, "")
		if err != nil {
			return nil, err
		}
		result, err := ps.call(ctx, id, c, method, p)
		var rpc *protocol.RPCError
		if err == nil || errors.As(err, &rpc) {
			return result, err
		}
		_ = c.Close()
		if ctx.Err() != nil || attempt == retries {
			return nil, err
		}
	}
}

var errSuperseded = errors.New("superseded by a newer channel")

// call waits for one call on c, giving up early once a newer channel from the same peer arrives.
func (ps *Peers) call(ctx context.Context, id string, c *protocol.Conn, method string, p any) (json.RawMessage, error) {
	done := make(chan error, 1)
	var result json.RawMessage
	go func() { done <- c.Call(ctx, method, p, &result) }()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			return result, err
		case <-tick.C:
			if n := ps.Mitosis.Connection(id); n != nil && n != c {
				return nil, errSuperseded
			}
		}
	}
}

// Keepalive pings a peer connection until the connection or the service ends, and closes it when
// the peer stops answering. A peer that restarted or lost its network leaves a half-open connection
// behind; without this, the next call on it would wait out its timeout before anything redialled.
// A frame heard within the period proves the peer is there (the other end's ping, a reply, an
// event), so the two ends of a quiet channel take turns and a busy one is never pinged.
func Keepalive(ctx context.Context, c *protocol.Conn, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
			return
		case <-t.C:
		}
		if time.Since(c.Heard()) < every {
			continue
		}
		ping, cancel := context.WithTimeout(ctx, every)
		err := c.Call(ping, "mitosis.ping", struct{}{}, nil)
		cancel()
		var rpc *protocol.RPCError
		if err != nil && !errors.As(err, &rpc) && ctx.Err() == nil {
			_ = c.Close()
			return
		}
	}
}

func (ps *Peers) Admin(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	switch method {
	case "block":
		raw, err := ps.Mitosis.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var p struct {
			PeerID string `json:"peer_id"`
		}
		if err = json.Unmarshal(params, &p); err != nil {
			return nil, err
		}
		if ps.Forget == nil {
			return raw, nil
		}
		return raw, ps.Forget(ps.ctx, p.PeerID)
	case "invite":
		locator := ps.WebURL
		var err error
		if locator == "" {
			locator, err = ps.Locator()
			if err != nil {
				return nil, err
			}
		}
		raw, err := ps.Mitosis.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invite Invitation
		_ = json.Unmarshal(raw, &invite)
		invite.Locator = locator
		return JSON(invite), nil
	case "join":
		var inv Invitation
		if err := protocol.Decode(params, &inv); err != nil {
			return nil, err
		}
		c, err := ps.Peer(ctx, inv.PeerID, inv.Locator)
		if err != nil {
			return nil, err
		}
		var result Invitation
		locator := "" // a device no one can dial, a browser tab, claims without one
		if ps.Locator != nil {
			if locator, err = ps.Locator(); err != nil {
				return nil, err
			}
		}
		err = c.Call(ctx, "pair.claim", map[string]string{"invitation_id": inv.ID, "token": inv.Token, "locator": locator}, &result)
		if err != nil {
			return nil, err
		}
		ps.mu.Lock()
		ps.joining[inv.PeerID] = inv.Locator
		ps.mu.Unlock()
		return JSON(result), nil
	case "grant":
		raw, err := ps.Mitosis.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var g Grant
		_ = json.Unmarshal(params, &g)
		ps.mu.Lock()
		locator, joined := ps.joining[g.Principal.PeerID]
		delete(ps.joining, g.Principal.PeerID)
		ps.mu.Unlock()
		if joined {
			return raw, ps.Mitosis.SavePeer(g.Principal.PeerID, locator)
		}
		return raw, nil
	case "remote":
		var p struct {
			PeerID string          `json:"peer_id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		return ps.Remote(ctx, p.PeerID, p.Method, p.Params)
	case "sync":
		var p struct {
			Join string `json:"join"`
		}
		if json.Unmarshal(params, &p) == nil && p.Join != "" {
			_, _ = ps.Peer(ctx, p.Join, "") // a channel to ask it the group
		}
		return ps.Mitosis.SyncAdmin(ctx, params, ps.endpoints())
	case "publish":
		var p struct {
			Scope    string `json:"scope_id"`
			Name     string `json:"display_name"`
			Withdraw bool   `json:"withdrawn"`
		}
		if err := protocol.Decode(params, &p); err != nil {
			return nil, err
		}
		locator, err := ps.Locator()
		if err != nil {
			return nil, err
		}
		r, err := ps.Mitosis.PublishContact(p.Scope, p.Name, []Locator{{Transport: "tailcat/1", Locator: locator}}, p.Withdraw)
		return JSON(r), err
	default:
		return ps.Mitosis.Admin(ctx, method, params)
	}
}

// retireAfter is how long a restarted Mitosis waits before retiring throwaway registrations no Orb
// came back to: attached Orbs retry every two seconds at most.
const retireAfter = 2 * time.Minute

// Greet runs at startup: it clears what the previous run left, then dials every known peer, and
// keeps dialling those without a channel.
// Their connections to that run are half-open; a fresh channel from this side becomes their
// newest, so their next call lands at once instead of waiting out a timeout on the dead one.
func (ps *Peers) Greet() {
	raw, err := ps.Mitosis.Admin(ps.ctx, "status", JSON(struct{}{}))
	if err != nil {
		return
	}
	var status struct {
		Peers     []string            `json:"peers"`
		States    map[string]string   `json:"peer_states"`
		Instances []Instance          `json:"instances"`
		Scopes    map[string][]string `json:"scopes"`
	}
	_ = json.Unmarshal(raw, &status)
	// Orbs a previous run started for peers ended with it (a crash left no one to retire them).
	stale := []string{}
	for _, i := range status.Instances {
		if strings.HasPrefix(i.Alias, "launch-") && !i.Available {
			stale = append(stale, i.ID)
		}
	}
	if len(stale) > 0 {
		_, _ = ps.Mitosis.Admin(ps.ctx, "retire", JSON(map[string][]string{"instance_ids": stale}))
	}
	// Throwaway registrations retire when their Orb exits cleanly; a crashed or killed one never
	// does. Once the Orbs still running have had time to attach again, the rest are gone.
	time.AfterFunc(retireAfter, func() {
		if ps.ctx.Err() == nil {
			_, _ = ps.Mitosis.Admin(ps.ctx, "retire", JSON(struct{}{}))
		}
	})
	// A peer that restarted behind a NAT cannot always dial back (a phone after an app update):
	// every known peer without a channel is dialled again until one opens, but the ring, not a
	// greeting, links the members of this device's sync group.
	for {
		for _, peer := range status.Peers {
			if status.States[peer] == "blocked" || peer == ps.Mitosis.PeerID() || ps.Mitosis.Connection(peer) != nil || ps.ringed(peer) {
				continue
			}
			go func() {
				ctx, cancel := context.WithTimeout(ps.ctx, 20*time.Second)
				defer cancel()
				_, _ = ps.Peer(ctx, peer, "")
			}()
		}
		select {
		case <-ps.ctx.Done():
			return
		case <-time.After(30 * time.Second):
		}
		raw, err := ps.Mitosis.Admin(ps.ctx, "status", JSON(struct{}{}))
		if err != nil {
			return
		}
		_ = json.Unmarshal(raw, &status)
	}
}

// Reconcile exchanges scope records with members on every change and every 15 s. A link whose
// records and channel are as they were after its last exchange waits five minutes, so an idle
// scope costs no traffic, and one that failed waits a minute, so an offline member is not dialled
// (a whole Tailcat client started and stopped) every sweep.
func (ps *Peers) Reconcile() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	type mark struct {
		versions string
		c        *protocol.Conn
		until    time.Time
	}
	var mu sync.Mutex
	last := map[string]mark{}
	for {
		select {
		case <-ps.ctx.Done():
			return
		case <-ticker.C:
		case <-ps.Mitosis.Changes():
		}
		// A device in a sync group runs a round each time, so the group converges with no command.
		_, _ = ps.Mitosis.Sync(ps.ctx, ps.endpoints())
		raw, err := ps.Mitosis.Admin(ps.ctx, "status", JSON(struct{}{}))
		if err != nil {
			continue
		}
		var status struct {
			Scopes map[string][]string `json:"scopes"`
			Peers  []string            `json:"peers"`
		}
		_ = json.Unmarshal(raw, &status)
		var wg sync.WaitGroup
		slots := make(chan struct{}, 8)
		for scope, members := range status.Scopes {
			for _, peer := range ps.links(members) {
				k := scope + "/" + peer
				versions, err := ps.Mitosis.Versions(peer, scope)
				if err != nil {
					continue
				}
				mu.Lock()
				m, ok := last[k]
				mu.Unlock()
				if ok && m.versions == string(JSON(versions)) && m.c == ps.Mitosis.Connection(peer) && time.Now().Before(m.until) {
					continue
				}
				wg.Add(1)
				slots <- struct{}{}
				go func() {
					defer func() { <-slots; wg.Done() }()
					ctx, cancel := context.WithTimeout(ps.ctx, 10*time.Second)
					defer cancel()
					c, err := ps.Peer(ctx, peer, "")
					if err == nil {
						err = ps.exchange(ctx, c, scope, peer, versions)
					}
					m := mark{string(JSON(versions)), ps.Mitosis.Connection(peer), time.Now().Add(time.Minute)}
					if after, e := ps.Mitosis.Versions(peer, scope); err == nil && e == nil {
						m = mark{string(JSON(after)), c, time.Now().Add(5 * time.Minute)}
					}
					mu.Lock()
					last[k] = m
					mu.Unlock()
				}()
			}
		}
		wg.Wait()
		// A channel this device dialled to a member it no longer links to (the ring moved as the
		// scope grew, a hand-in arrived) is hung up, unless a peer outside the ring is paired or
		// a call went to it within a minute.
		dials := map[string]bool{}
		for _, members := range status.Scopes {
			for _, p := range ps.dials(members) {
				dials[p] = true
			}
		}
		for _, members := range status.Scopes {
			for _, p := range members {
				used, _ := ps.used.Load(p)
				if !dials[p] && (ps.ringed(p) || !slices.Contains(status.Peers, p)) && (used == nil || time.Since(used.(time.Time)) > time.Minute) {
					ps.Mitosis.Hangup(p)
				}
			}
		}
	}
}

// dials is who this device dials to reconcile a scope: the members a power of two ahead of it on
// the ring of members by PeerID, and a sync group's owners only while this member has something
// to hand them (an owner fetches a hand-in from its publisher, and no one dials a browser tab). A
// change then crosses n members in about log2(n) hops with about log2(n) dialled channels a
// device, owners included, where dialling every member would take n-1.
func (ps *Peers) dials(members []string) []string {
	self := ps.Mitosis.PeerID()
	ring := slices.Compact(slices.Sorted(slices.Values(members)))
	i, found := slices.BinarySearch(ring, self)
	if !found {
		ring = slices.Insert(ring, i, self)
	}
	out := []string{}
	add := func(p string) {
		if p != self && !slices.Contains(out, p) {
			out = append(out, p)
		}
	}
	ps.Mitosis.mu.Lock()
	if ps.Mitosis.handing && slices.Equal(members, ps.Mitosis.state.Scopes[ps.Mitosis.state.Group]) {
		for _, o := range ps.Mitosis.state.Owners {
			add(o)
		}
	}
	ps.Mitosis.mu.Unlock()
	for d := 1; d < len(ring); d *= 2 {
		add(ring[(i+d)%len(ring)])
	}
	return out
}

// links is who this device reconciles a scope with: those it dials, and every member it already
// has a channel to.
func (ps *Peers) links(members []string) []string {
	out := ps.dials(members)
	for _, p := range members {
		if p != ps.Mitosis.PeerID() && !slices.Contains(out, p) && ps.Mitosis.Connection(p) != nil {
			out = append(out, p)
		}
	}
	return out
}

// exchange sends peer the revisions this device holds of scope (have), merges the records peer
// holds newer, and publishes the ones peer holds older or lacks.
func (ps *Peers) exchange(ctx context.Context, c *protocol.Conn, scope, peer string, have map[string]string) error {
	var behind []string
	cursor := ""
	for pages := 0; pages < 16; pages++ {
		var page struct {
			Items  []Record `json:"items"`
			Cursor string   `json:"cursor"`
			Behind []string `json:"behind"`
		}
		if err := c.Call(ctx, "peers.list", map[string]any{"scope_id": scope, "cursor": cursor, "have": have}, &page); err != nil {
			return err
		}
		for _, r := range page.Items {
			_, _ = ps.Mitosis.MergeContact(peer, r)
		}
		behind = page.Behind
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	if len(behind) == 0 {
		return nil
	}
	held, err := ps.Mitosis.contacts(peer, scope)
	if err != nil {
		return err
	}
	records := []Record{}
	for _, id := range behind {
		if r, ok := held[id]; ok {
			records = append(records, r)
		}
	}
	for len(records) > 0 {
		n := min(len(records), 16)
		if err = c.Call(ctx, "peers.publish", map[string]any{"records": records[:n]}, nil); err != nil {
			return err
		}
		records = records[n:]
	}
	return nil
}

// endpoints is where this device is reached, once its transport is up.
func (ps *Peers) endpoints() []Locator {
	if ps.Locator == nil {
		return nil
	}
	if locator, err := ps.Locator(); err == nil {
		return []Locator{{Transport: "tailcat/1", Locator: locator}}
	}
	return nil
}

// FullGrant is what an invitation offers: control of every conversation, current and
// future, and of the machine itself (host.launch: its threads, starting Orb, updating it).
func FullGrant(peer string) Grant {
	g := ConversationGrant(peer)
	g.Permissions = append(g.Permissions, "host.launch")
	return g
}

const invitationPrefix = "orb-mitosis:v1:"

// InvitationCode is what a joining device copies or scans: only what it uses, so grants stay
// with the inviter and the QR code stays small.
func InvitationCode(inv Invitation) string {
	inv = Invitation{ID: inv.ID, PeerID: inv.PeerID, Token: inv.Token, Locator: inv.Locator, Expires: inv.Expires}
	return invitationPrefix + base64.RawURLEncoding.EncodeToString(JSON(inv))
}

// ParseInvitation reads an invitation from its code, its JSON, or a message that holds the code.
func ParseInvitation(text string) (Invitation, error) {
	var inv Invitation
	text = strings.TrimSpace(text)
	if len(text) > protocol.MaxFrame {
		return inv, fmt.Errorf("invitation is too large")
	}
	raw := []byte(text)
	if _, code, ok := strings.Cut(text, invitationPrefix); ok {
		var err error
		if raw, err = base64.RawURLEncoding.Strict().DecodeString(strings.Fields(code + " ")[0]); err != nil {
			return inv, fmt.Errorf("invalid invitation; copy it again from Create an invitation")
		}
	}
	if err := protocol.Decode(raw, &inv); err != nil {
		return inv, fmt.Errorf("invalid invitation; paste the complete invitation")
	}
	if _, err := ParsePeerID(inv.PeerID); err != nil || !protocol.ValidID(inv.ID) || inv.Token == "" || inv.Locator == "" {
		return inv, fmt.Errorf("incomplete invitation; create a new one on the other device")
	}
	if inv.Expires <= time.Now().Unix() {
		return inv, fmt.Errorf("invitation expired; create a new one on the other device")
	}
	return inv, nil
}

// ConversationGrant is control of every conversation without the machine: what a joining
// Orb gives back to the Orb it joined, which never asked for more.
func ConversationGrant(peer string) Grant {
	return Grant{Principal: Principal{PeerID: peer, Subject: Subject{Kind: "controller"}}, GroupID: "*", IncludeFuture: true, Permissions: []string{"instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.input.reply", "instance.cancel", "instance.session.manage"}}
}
