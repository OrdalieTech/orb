// Package mitosis owns connectivity authority, never agent execution or sessions,
// and defines the non-owning, versioned Orb instance attachments it routes.
package mitosis

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// Grant is what a principal may do with this agent: its conversations, every one of them, as the
// agent is the unit of access. Destination, on an agent principal, is the peer this agent may call.
type Grant struct {
	ID          string    `json:"grant_id,omitempty"`
	Principal   Principal `json:"principal"`
	Permissions []string  `json:"permissions"`
	Destination string    `json:"destination,omitempty"`
}
type Instance struct {
	ID         string `json:"instance_id"`
	Alias      string `json:"alias"`
	Generation string `json:"registration_generation"`
	Credential string `json:"credential_hash,omitempty"`
	BootID     string `json:"boot_id,omitempty"`
	Available  bool   `json:"available"`
}
type Invitation struct {
	ClaimantLocator string  `json:"claimant_locator,omitempty"`
	ID              string  `json:"invitation_id"`
	PeerID          string  `json:"peer_id"`
	Token           string  `json:"token,omitempty"`
	Locator         string  `json:"locator,omitempty"`
	Expires         int64   `json:"expires"`
	Grants          []Grant `json:"grants"`
	Claimant        string  `json:"claimant,omitempty"`
	Status          string  `json:"status"`
}
type invitationState struct {
	Invitation
	Hash string `json:"token_hash"`
	// Contested: a second device presented the code. Someone else saw it, so it is never approved.
	Contested bool `json:"contested,omitempty"`
}
type state struct {
	Version      int                           `json:"version"`
	Key          []byte                        `json:"key"`
	Instances    map[string]Instance           `json:"instances"`
	Grants       []Grant                       `json:"grants"`
	Blocked      map[string]bool               `json:"blocked"`
	Invitations  map[string]invitationState    `json:"invitations"`
	Scopes       map[string][]string           `json:"scopes"`
	Records      map[string]Record             `json:"records"`
	Peers        map[string]string             `json:"peers"`
	Group        string                        `json:"sync_group,omitempty"`
	Owners       []string                      `json:"sync_owners,omitempty"`
	Prior        []string                      `json:"sync_prior,omitempty"` // the owners of the membership version this one replaced
	GroupVersion uint64                        `json:"sync_version,omitempty"`
	Versions     map[string]map[string]version `json:"sync_versions,omitempty"` // class -> entry -> where it stands
	Merged       map[string]string             `json:"sync_merged,omitempty"`   // "<member> <class>" -> digest of the hand-in an owner folded in
	Transport    json.RawMessage               `json:"transport,omitempty"`
}
type registration struct {
	generation string
	endpoint   Endpoint
	catalog    string // the digest of models and commands its last describe sent
}
type Mitosis struct {
	mu             sync.Mutex
	round          sync.Mutex // held through a sync round and each membership change, so no round outlives its group
	store          document.Document
	state          state
	active         map[string]registration
	channels       map[string]map[*protocol.Conn]uint64 // value: arrival order, newest highest
	arrivals       uint64
	dialled        map[*protocol.Conn]bool // channels this Mitosis opened, against those it accepted
	failed, closed bool
	boot           string
	changes        chan struct{}
	host           Host
	name           string
	classes        map[string]Class
	synced         string                // digest of what Sync last published, so an unchanged round publishes nothing
	sealed         map[string]sealed     // class -> what Sync last sealed, so an unchanged class is not sealed again
	vaults         map[string][]byte     // digest -> sealed vault this device serves (vault.get)
	handing        bool                  // a member's last round handed the owners something the group's set lacks
	op             [2]string             // this owner's last membership change (change)
	resolved       map[string]resolution // agent.call operations by ledger key, as first resolved
	watches        map[string]map[uint64]context.CancelFunc
	watchSeq       uint64
	notifyTimer    *time.Timer // coalesces local synced-class changes before waking Reconcile
}

// Host serves a peer's machine-level calls — the threads stored on this machine, and starting
// Orb in a folder — for principals whose grant includes host.launch.
type Host func(ctx context.Context, p Principal, method string, params json.RawMessage) (json.RawMessage, error)

func (b *Mitosis) SetHost(h Host) {
	b.mu.Lock()
	b.host = h
	b.mu.Unlock()
}

// SetName is what this agent is called on its peers' screens ("Baudouin's MacBook Pro").
func (b *Mitosis) SetName(name string) {
	b.mu.Lock()
	b.name = name
	b.mu.Unlock()
}

// owner: a call as this Mitosis's own peer, which only its owner can make, in process (a peer
// proves its identity with its key, so none arrives as this one). The owner reaches everything
// here as a full controller would, the way the owner API already administers it.
func (b *Mitosis) owner(p Principal) bool {
	return p.Subject.Kind == "controller" && p.PeerID == b.PeerID()
}

// groupPermissions is the authority a current, unblocked sync-group member holds on every
// member of the group: identical to FullGrant (agent.* and host.launch), nothing more.
var groupPermissions = FullGrant("").Permissions

// groupMember reports whether p is a controller of a current, unblocked member of this Mitosis's
// sync group. Plain discovery scopes grant no execution authority, and an agent subject still
// requires its own explicit grant.
func (b *Mitosis) groupMember(p Principal) bool {
	return p.Subject.Kind == "controller" &&
		!b.failed && !b.closed && !b.state.Blocked[p.PeerID] &&
		b.state.Group != "" &&
		slices.Contains(b.state.Scopes[b.state.Group], b.PeerID()) &&
		slices.Contains(b.state.Scopes[b.state.Group], p.PeerID)
}

// groupPermits reports whether p holds permission through current sync-group membership
// (equivalent to FullGrant). An empty permission asks whether p holds any group access.
func (b *Mitosis) groupPermits(p Principal, permission string) bool {
	return b.groupMember(p) && (permission == "" || slices.Contains(groupPermissions, permission))
}

func (b *Mitosis) hasExplicitGrantLocked(peer string) bool {
	return slices.ContainsFunc(b.state.Grants, func(g Grant) bool { return g.Principal.PeerID == peer })
}

// watch registers an active events.subscribe wait for peer so losing membership or being blocked
// cancels it immediately.
func (b *Mitosis) watch(ctx context.Context, peer string) (context.Context, func()) {
	wctx, cancel := context.WithCancel(ctx)
	b.mu.Lock()
	b.watchSeq++
	id := b.watchSeq
	if b.watches[peer] == nil {
		b.watches[peer] = map[uint64]context.CancelFunc{}
	}
	b.watches[peer][id] = cancel
	b.mu.Unlock()
	return wctx, func() {
		cancel()
		b.mu.Lock()
		delete(b.watches[peer], id)
		if len(b.watches[peer]) == 0 {
			delete(b.watches, peer)
		}
		b.mu.Unlock()
	}
}

func (b *Mitosis) cancelWatchesLocked(peer string) []context.CancelFunc {
	ws := b.watches[peer]
	delete(b.watches, peer)
	cancels := make([]context.CancelFunc, 0, len(ws))
	for _, cancel := range ws {
		cancels = append(cancels, cancel)
	}
	return cancels
}

func (b *Mitosis) takeChannelsLocked(peer string) []*protocol.Conn {
	cs := b.channels[peer]
	delete(b.channels, peer)
	out := make([]*protocol.Conn, 0, len(cs))
	for c := range cs {
		out = append(out, c)
	}
	return out
}

func closeAccess(conns []*protocol.Conn, cancels []context.CancelFunc) {
	for _, cancel := range cancels {
		cancel()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// hostAllowed: only a controller granted host.launch (explicitly or via the sync group) reaches the machine.
func (b *Mitosis) hostAllowed(p Principal) bool {
	if b.failed || b.closed || b.state.Blocked[p.PeerID] || p.Subject.Kind != "controller" {
		return false
	}
	if b.owner(p) || b.groupPermits(p, "host.launch") {
		return true
	}
	for _, g := range b.state.Grants {
		if g.Principal == p && g.Destination == "" && slices.Contains(g.Permissions, "host.launch") {
			return true
		}
	}
	return false
}

func PeerID(key ed25519.PublicKey) string {
	return "orb:ed25519:" + base64.RawURLEncoding.EncodeToString(key)
}
func ParsePeerID(id string) (ed25519.PublicKey, error) {
	s, ok := strings.CutPrefix(id, "orb:ed25519:")
	if !ok {
		return nil, Fail("unauthorized")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize || len(s) != 43 {
		return nil, Fail("unauthorized")
	}
	return ed25519.PublicKey(b), nil
}
func Open(store document.Document, create bool) (*Mitosis, error) {
	if store == nil {
		return nil, errors.New("mitosis requires explicit storage")
	}
	b := &Mitosis{changes: make(chan struct{}, 1), classes: map[string]Class{}, sealed: map[string]sealed{}, resolved: map[string]resolution{}, watches: map[string]map[uint64]context.CancelFunc{}, vaults: map[string][]byte{}, store: store, active: map[string]registration{}, channels: map[string]map[*protocol.Conn]uint64{}, dialled: map[*protocol.Conn]bool{}, boot: protocol.NewID()}
	raw, err := store.Read(context.Background())
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		if !create {
			return nil, errors.New("mitosis profile missing; initialize explicitly")
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		b.state = state{Version: 1, Key: key, Instances: map[string]Instance{}, Blocked: map[string]bool{}, Invitations: map[string]invitationState{}, Scopes: map[string][]string{}, Records: map[string]Record{}, Peers: map[string]string{}}
		if err = b.save(); err != nil {
			return nil, err
		}
	} else {
		if err = protocol.Decode(raw, &b.state); err != nil {
			return nil, err
		}
		s := &b.state
		if s.Version != 1 {
			return nil, Fail("unsupported_version")
		}
		if len(s.Key) != ed25519.PrivateKeySize || s.Instances == nil || s.Blocked == nil || s.Invitations == nil || s.Scopes == nil || s.Records == nil || s.Peers == nil {
			return nil, errors.New("corrupt mitosis profile")
		}
		key := ed25519.NewKeyFromSeed(s.Key[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(key, s.Key) != 1 {
			return nil, errors.New("corrupt mitosis identity")
		}
		for id, r := range s.Instances {
			if id != r.ID || !protocol.ValidID(id) || !validSecret(r.Credential) {
				return nil, errors.New("corrupt registration")
			}
			if _, err = protocol.Counter(r.Generation); err != nil {
				return nil, err
			}
		}
		for k, r := range s.Records {
			c, e := r.Verify()
			if e != nil || k != c.Scope+"/"+c.Peer {
				return nil, errors.New("corrupt contact record")
			}
		}
		for _, g := range s.Grants {
			if err = validateGrant(g); err != nil {
				return nil, err
			}
		}
	}
	return b, nil
}
func (b *Mitosis) PeerID() string {
	return PeerID(ed25519.PrivateKey(b.state.Key).Public().(ed25519.PublicKey))
}
func (b *Mitosis) Principal() Principal {
	return Principal{PeerID: b.PeerID(), Subject: Subject{Kind: "controller"}}
}
func (b *Mitosis) save() error {
	if b.failed || b.closed {
		return Fail("unavailable")
	}
	raw := JSON(b.state)
	if len(raw) > protocol.MaxFrame {
		b.failed = true
		return Fail("resource_exhausted")
	}
	if err := document.Replace(context.Background(), b.store, raw); err != nil {
		b.failed = true
		return err
	}
	select {
	case b.changes <- struct{}{}:
	default:
	}
	return nil
}
func secret() string {
	var v [32]byte
	_, _ = rand.Read(v[:])
	return base64.RawURLEncoding.EncodeToString(v[:])
}
func hash(s string) string {
	h := sha256.Sum256([]byte(s))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func validSecret(s string) bool {
	v, e := base64.RawURLEncoding.Strict().DecodeString(s)
	return e == nil && len(v) == 32 && len(s) == 43
}
func matches(s, digest string) bool {
	return validSecret(s) && subtle.ConstantTimeCompare([]byte(hash(s)), []byte(digest)) == 1
}

func validateGrant(g Grant) error {
	if g.ID != "" && !protocol.ValidID(g.ID) {
		return Fail("invalid_params")
	}
	if _, err := ParsePeerID(g.Principal.PeerID); err != nil {
		return err
	}
	if g.Principal.Subject.Kind != "controller" && g.Principal.Subject.Kind != "agent" {
		return Fail("unauthorized")
	}
	for _, p := range g.Permissions {
		switch p {
		case "agent.list", "agent.inspect", "agent.prompt", "agent.steer", "agent.follow_up", "agent.input.reply", "agent.cancel", "agent.session.manage":
		case "host.launch":
			if g.Principal.Subject.Kind != "controller" {
				return Fail("unauthorized")
			}
		default:
			return Fail("unauthorized")
		}
	}
	if g.Destination != "" {
		if _, err := ParsePeerID(g.Destination); err != nil {
			return err
		}
		if g.Principal.Subject.Kind != "agent" {
			return Fail("unauthorized")
		}
	}
	return nil
}
func (b *Mitosis) AddGrant(g Grant) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Grants) >= 1024 {
		return Fail("resource_exhausted")
	}
	g.Permissions = slices.Clone(g.Permissions)
	if err := validateGrant(g); err != nil {
		return err
	}
	if g.ID == "" {
		g.ID = protocol.NewID()
	}
	for _, old := range b.state.Grants {
		if old.ID == g.ID {
			return Fail("identity_conflict")
		}
	}
	b.state.Grants = append(b.state.Grants, g)
	return b.save()
}
func (b *Mitosis) allowed(p Principal, id, permission, destination string) bool {
	if b.failed || b.closed || b.state.Blocked[p.PeerID] {
		return false
	}
	_, ok := b.state.Instances[id]
	if destination == "" && (!ok || b.owner(p) || b.groupPermits(p, permission)) {
		return ok
	}
	return slices.ContainsFunc(b.state.Grants, func(g Grant) bool {
		return g.Principal == p && g.Destination == destination && slices.Contains(g.Permissions, permission)
	})
}
func (b *Mitosis) Allowed(p Principal, id, permission string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowed(p, id, permission, "")
}
func (b *Mitosis) Catalog(p Principal) []Instance {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Instance{}
	for id, r := range b.state.Instances {
		if b.allowed(p, id, "agent.list", "") {
			r.Credential = ""
			_, r.Available = b.active[id]
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, c Instance) int { return strings.Compare(a.ID, c.ID) })
	return out
}

// autoAlias is the name of the throwaway instance an Orb enrolls when started
// without an alias of its own.
var autoAlias = lazyregexp.New(`^instance-[a-z0-9_-]{8}$`)

// Retire removes the named instances or, with none named, every throwaway
// instance whose Orb has gone: one that attached once and is not attached now. It returns how
// many it removed.
func (b *Mitosis) Retire(ids []string) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.retireLocked(ids)
}

func (b *Mitosis) retireLocked(ids []string) (int, error) {
	removed := 0
	for id, r := range b.state.Instances {
		_, active := b.active[id]
		if slices.Contains(ids, id) || len(ids) == 0 && !active && r.BootID != "" && autoAlias().MatchString(r.Alias) {
			delete(b.state.Instances, id)
			removed++
		}
	}
	if removed == 0 {
		return 0, nil
	}
	return removed, b.save()
}

func (b *Mitosis) Enroll(alias string) (Instance, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Instances) >= 4096 {
		if _, err := b.retireLocked(nil); err != nil {
			return Instance{}, "", err
		}
	}
	if len(alias) > 128 || len(b.state.Instances) >= 4096 {
		return Instance{}, "", Fail("resource_exhausted")
	}
	if alias != "" {
		for _, r := range b.state.Instances {
			if r.Alias == alias {
				return Instance{}, "", Fail("identity_conflict")
			}
		}
	}
	token := secret()
	r := Instance{ID: protocol.NewID(), Alias: alias, Credential: hash(token), Generation: "0"}
	b.state.Instances[r.ID] = r
	if err := b.save(); err != nil {
		return Instance{}, "", err
	}
	r.Credential = ""
	return r, token, nil
}
func (b *Mitosis) Attach(id, credential, boot string, endpoint Endpoint) (string, error) {
	if endpoint == nil || !protocol.ValidID(boot) {
		return "", Fail("unauthorized")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.state.Instances[id]
	if !ok || !matches(credential, r.Credential) {
		return "", Fail("unauthorized")
	}
	if _, ok = b.active[id]; ok {
		return "", Fail("identity_conflict")
	}
	n, err := protocol.Counter(r.Generation)
	if err != nil || n == ^uint64(0) {
		return "", Fail("resource_exhausted")
	}
	r.Generation = strconv.FormatUint(n+1, 10)
	r.BootID = boot
	b.state.Instances[id] = r
	if err = b.save(); err != nil {
		return "", err
	}
	b.active[id] = registration{generation: r.Generation, endpoint: endpoint}
	return r.Generation, nil
}
func (b *Mitosis) Detach(id, generation string) {
	b.mu.Lock()
	r, ok := b.active[id]
	if ok && r.generation == generation {
		delete(b.active, id)
	}
	b.mu.Unlock()
	if ok && r.generation == generation {
		_ = r.endpoint.Close()
	}
}
func (b *Mitosis) Available(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.active[id]
	return ok && !b.closed && !b.failed
}
func (b *Mitosis) Invite(grants []Grant) (Invitation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().Unix()
	for id, i := range b.state.Invitations {
		if i.Expires < now {
			delete(b.state.Invitations, id)
		}
	}
	// An approved invitation stays until it expires, so its claimant can read the approval; only
	// those still open count against the cap.
	open := 0
	for _, i := range b.state.Invitations {
		if i.Status != "approved" {
			open++
		}
	}
	if open >= 128 {
		return Invitation{}, Fail("resource_exhausted")
	}
	grants = append([]Grant(nil), grants...)
	for i := range grants {
		grants[i].Permissions = slices.Clone(grants[i].Permissions)
		grants[i].Principal = b.Principal()
		if err := validateGrant(grants[i]); err != nil {
			return Invitation{}, err
		}
	}
	token := secret()
	inv := Invitation{ID: protocol.NewID(), PeerID: b.PeerID(), Token: token, Expires: now + 600, Grants: grants, Status: "pending"}
	stored := inv
	stored.Token = ""
	b.state.Invitations[inv.ID] = invitationState{Invitation: stored, Hash: hash(token)}
	if err := b.save(); err != nil {
		return Invitation{}, err
	}
	return cloneInvitation(inv), nil
}
func (b *Mitosis) Claim(peer, id, token string, locators ...string) (Invitation, error) {
	if _, err := ParsePeerID(peer); err != nil {
		return Invitation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.state.Invitations[id]
	if !ok || b.state.Blocked[peer] || i.Expires < time.Now().Unix() || !matches(token, i.Hash) {
		return Invitation{}, Fail("unauthorized")
	}
	if i.Claimant != "" && i.Claimant != peer {
		i.Contested = true
		b.state.Invitations[id] = i
		_ = b.save()
		return Invitation{}, Fail("identity_conflict")
	}
	if len(locators) > 0 {
		if len(locators[0]) > 8192 {
			return Invitation{}, Fail("resource_exhausted")
		}
		if i.ClaimantLocator != "" && i.ClaimantLocator != locators[0] {
			return Invitation{}, Fail("identity_conflict")
		}
		i.ClaimantLocator = locators[0]
	}
	i.Claimant = peer
	b.state.Invitations[id] = i
	if err := b.save(); err != nil {
		return Invitation{}, err
	}
	return cloneInvitation(i.Invitation), nil
}
func (b *Mitosis) PairStatus(peer, id string) (Invitation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.state.Invitations[id]
	if !ok || i.Claimant != peer || b.state.Blocked[peer] || (i.Status != "approved" && i.Expires < time.Now().Unix()) {
		return Invitation{}, Fail("not_found")
	}
	return cloneInvitation(i.Invitation), nil
}
func (b *Mitosis) Approve(id, claimant string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.state.Invitations[id]
	if !ok || i.Claimant == "" || i.Claimant != claimant || b.state.Blocked[claimant] {
		return Fail("unauthorized")
	}
	if i.Status == "approved" {
		return nil
	}
	if i.Expires < time.Now().Unix() {
		return Fail("unauthorized")
	}
	if i.Contested {
		return Fail("identity_conflict")
	}
	if len(b.state.Grants)+len(i.Grants) > 1024 {
		return Fail("resource_exhausted")
	}
	approved := make([]Grant, 0, len(i.Grants))
	for _, g := range i.Grants {
		g.Principal = Principal{PeerID: claimant, Subject: Subject{Kind: "controller"}}
		g.Permissions = slices.Clone(g.Permissions)
		if err := validateGrant(g); err != nil {
			return err
		}
		g.ID = protocol.NewID()
		approved = append(approved, g)
	}
	if i.ClaimantLocator != "" {
		if len(b.state.Peers) >= MaxPeers && b.state.Peers[claimant] == "" {
			return Fail("resource_exhausted")
		}
		b.state.Peers[claimant] = i.ClaimantLocator
	}
	b.state.Grants = append(b.state.Grants, approved...)
	i.Status = "approved"
	b.state.Invitations[id] = i
	return b.save()
}
func (b *Mitosis) Block(peer string) error {
	// A block waits for the round in flight, so a blocked owner's set changes nothing after it.
	b.round.Lock()
	defer b.round.Unlock()
	b.mu.Lock()
	group, owner := b.state.Group, slices.Contains(b.state.Owners, b.PeerID()) && slices.Contains(b.state.Scopes[b.state.Group], peer)
	if owner && (b.change("remove", peer) != nil || b.save() != nil) {
		owner = false
	}
	b.mu.Unlock()
	if owner {
		// Out of the group first, while its channel still carries the news.
		_, _ = b.sync(context.Background(), nil)
		b.tellRemoved(peer, group)
	}
	b.mu.Lock()
	b.state.Blocked[peer] = true
	b.state.Grants = slices.DeleteFunc(b.state.Grants, func(g Grant) bool { return g.Principal.PeerID == peer })
	for id, i := range b.state.Invitations {
		if i.Claimant == peer {
			delete(b.state.Invitations, id)
		}
	}
	conns := b.takeChannelsLocked(peer)
	cancels := b.cancelWatchesLocked(peer)
	err := b.save()
	b.mu.Unlock()
	closeAccess(conns, cancels)
	return err
}
func (b *Mitosis) Close() error {
	b.mu.Lock()
	b.closed = true
	if b.notifyTimer != nil {
		b.notifyTimer.Stop()
		b.notifyTimer = nil
	}
	active := b.active
	b.active = map[string]registration{}
	channels := b.channels
	b.channels = map[string]map[*protocol.Conn]uint64{}
	watches := b.watches
	b.watches = map[string]map[uint64]context.CancelFunc{}
	b.mu.Unlock()
	for _, ws := range watches {
		for _, cancel := range ws {
			cancel()
		}
	}
	for _, r := range active {
		_ = r.endpoint.Close()
	}
	for _, cs := range channels {
		for c := range cs {
			_ = c.Close()
		}
	}
	return nil
}

func permission(method string) string {
	switch method {
	case "inspect", "image": // an image the conversation carried is part of what inspection reads
		return "agent.inspect"
	case "session.list", "session.new", "session.switch", "session.fork", "session.model", "session.name", "session.compact":
		return "agent.session.manage"
	case "shell", "complete": // a command in its shell, or the files `@` lists, is what a prompt can already reach
		return "agent.prompt"
	default:
		return "agent." + method
	}
}
func (b *Mitosis) Call(ctx context.Context, p Principal, call Call) (json.RawMessage, error) {
	if err := ValidateCall(call); err != nil {
		return nil, err
	}
	b.mu.Lock()
	if !b.allowed(p, call.InstanceID, permission(call.Method), "") {
		b.mu.Unlock()
		return nil, Fail("not_found")
	}
	r, ok := b.active[call.InstanceID]
	b.mu.Unlock()
	if !ok {
		return nil, Fail("unavailable")
	}
	return r.endpoint.Invoke(ctx, "call", JSON(Request{PageLimit: protocol.PageLimit(ctx), Principal: p, Generation: r.generation, Call: call}))
}

func cloneInvitation(i Invitation) Invitation {
	i.Grants = slices.Clone(i.Grants)
	for n := range i.Grants {
		i.Grants[n].Permissions = slices.Clone(i.Grants[n].Permissions)
	}
	return i
}

func (b *Mitosis) Changes() <-chan struct{} { return b.changes }

// Notify schedules a debounced sync round (250 ms) so a local change to a synced class
// (credentials, accounts, memory, mcp) leaves promptly instead of waiting for the 15 s sweep
// while coalescing bursts into one round. No-op when not in a sync group.
func (b *Mitosis) Notify() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed || b.state.Group == "" {
		return
	}
	if b.notifyTimer != nil {
		b.notifyTimer.Reset(250 * time.Millisecond)
		return
	}
	b.notifyTimer = time.AfterFunc(250*time.Millisecond, func() {
		b.mu.Lock()
		closed := b.closed || b.failed
		b.mu.Unlock()
		if !closed {
			select {
			case b.changes <- struct{}{}:
			default:
			}
		}
	})
}
