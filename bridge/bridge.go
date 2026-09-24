// Package bridge owns connectivity authority, never agent execution or sessions,
// and defines the non-owning, versioned Orb instance attachments it routes.
package bridge

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

	"github.com/OrdalieTech/orb/bridge/protocol"
)

type Grant struct {
	ID            string    `json:"grant_id,omitempty"`
	Principal     Principal `json:"principal"`
	GroupID       string    `json:"group_id,omitempty"`
	IncludeFuture bool      `json:"include_future"`
	Instances     []string  `json:"instances,omitempty"`
	Permissions   []string  `json:"permissions"`
	Destination   string    `json:"destination,omitempty"`
}
type Instance struct {
	ID         string `json:"instance_id"`
	Alias      string `json:"alias"`
	Group      string `json:"group_id"`
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
}
type state struct {
	Version     int                        `json:"version"`
	Key         []byte                     `json:"key"`
	Personal    string                     `json:"personal_group"`
	Groups      map[string]string          `json:"groups"`
	Instances   map[string]Instance        `json:"instances"`
	Grants      []Grant                    `json:"grants"`
	Blocked     map[string]bool            `json:"blocked"`
	Invitations map[string]invitationState `json:"invitations"`
	Scopes      map[string][]string        `json:"scopes"`
	Records     map[string]Record          `json:"records"`
	Peers       map[string]string          `json:"peers"`
	Transport   json.RawMessage            `json:"transport,omitempty"`
}
type registration struct {
	generation string
	endpoint   Endpoint
}
type Bridge struct {
	mu             sync.Mutex
	store          Store
	state          state
	active         map[string]registration
	channels       map[string]map[*protocol.Conn]bool
	failed, closed bool
	boot           string
	changes        chan struct{}
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
func Open(store Store, create bool) (*Bridge, error) {
	if store == nil {
		return nil, errors.New("bridge requires explicit storage")
	}
	b := &Bridge{changes: make(chan struct{}, 1), store: store, active: map[string]registration{}, channels: map[string]map[*protocol.Conn]bool{}, boot: protocol.NewID()}
	raw, err := store.Load()
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		if !create {
			return nil, errors.New("bridge profile missing; initialize explicitly")
		}
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		group := protocol.NewID()
		b.state = state{Version: 1, Key: key, Personal: group, Groups: map[string]string{group: "personal"}, Instances: map[string]Instance{}, Blocked: map[string]bool{}, Invitations: map[string]invitationState{}, Scopes: map[string][]string{}, Records: map[string]Record{}, Peers: map[string]string{}}
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
		if len(s.Key) != ed25519.PrivateKeySize || s.Groups == nil || s.Instances == nil || s.Blocked == nil || s.Invitations == nil || s.Scopes == nil || s.Records == nil || s.Peers == nil || s.Groups[s.Personal] == "" {
			return nil, errors.New("corrupt bridge profile")
		}
		key := ed25519.NewKeyFromSeed(s.Key[:ed25519.SeedSize])
		if subtle.ConstantTimeCompare(key, s.Key) != 1 {
			return nil, errors.New("corrupt bridge identity")
		}
		for id, r := range s.Instances {
			if id != r.ID || !protocol.ValidID(id) || !validSecret(r.Credential) || s.Groups[r.Group] == "" {
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
func (b *Bridge) PeerID() string {
	return PeerID(ed25519.PrivateKey(b.state.Key).Public().(ed25519.PublicKey))
}
func (b *Bridge) Principal() Principal {
	return Principal{PeerID: b.PeerID(), Subject: Subject{Kind: "controller"}}
}
func (b *Bridge) PersonalGroup() string { return b.state.Personal }
func (b *Bridge) save() error {
	if b.failed || b.closed {
		return Fail("unavailable")
	}
	raw := JSON(b.state)
	if len(raw) > protocol.MaxFrame {
		b.failed = true
		return Fail("resource_exhausted")
	}
	if err := b.store.Save(raw); err != nil {
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
	if g.Principal.Subject.Kind != "controller" && g.Principal.Subject.Kind != "instance" {
		return Fail("unauthorized")
	}
	if g.Principal.Subject.Kind == "instance" && !protocol.ValidID(g.Principal.Subject.InstanceID) {
		return Fail("unauthorized")
	}
	if g.Principal.Subject.Kind == "controller" && g.Principal.Subject.InstanceID != "" {
		return Fail("unauthorized")
	}
	if g.IncludeFuture && g.GroupID == "" {
		return Fail("unauthorized")
	}
	for _, id := range g.Instances {
		if !protocol.ValidID(id) {
			return Fail("not_found")
		}
	}
	for _, p := range g.Permissions {
		switch p {
		case "instance.list", "instance.inspect", "instance.prompt", "instance.steer", "instance.follow_up", "instance.input.reply", "instance.cancel", "instance.session.manage":
		default:
			return Fail("unauthorized")
		}
	}
	if g.Destination != "" {
		if _, err := ParsePeerID(g.Destination); err != nil {
			return err
		}
		if g.Principal.Subject.Kind != "instance" {
			return Fail("unauthorized")
		}
	}
	return nil
}
func (b *Bridge) prepareGrant(g Grant) (Grant, error) {
	g.Instances = slices.Clone(g.Instances)
	g.Permissions = slices.Clone(g.Permissions)
	if err := validateGrant(g); err != nil {
		return g, err
	}
	if g.GroupID != "" {
		if g.GroupID != "*" && b.state.Groups[g.GroupID] == "" {
			return g, Fail("not_found")
		}
		if !g.IncludeFuture {
			g.Instances = nil
			for id, r := range b.state.Instances {
				if g.GroupID == "*" || r.Group == g.GroupID {
					g.Instances = append(g.Instances, id)
				}
			}
			slices.Sort(g.Instances)
			g.GroupID = ""
		}
	}
	return g, nil
}
func (b *Bridge) AddGrant(g Grant) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Grants) >= 1024 {
		return Fail("resource_exhausted")
	}
	g, err := b.prepareGrant(g)
	if err != nil {
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
func (b *Bridge) allowed(p Principal, id, permission, destination string) bool {
	if b.failed || b.closed || b.state.Blocked[p.PeerID] {
		return false
	}
	r, ok := b.state.Instances[id]
	if destination == "" && !ok {
		return false
	}
	for _, g := range b.state.Grants {
		if g.Principal != p || g.Destination != destination || !slices.Contains(g.Permissions, permission) {
			continue
		}
		if slices.Contains(g.Instances, id) || (destination == "" && g.IncludeFuture && (g.GroupID == "*" || g.GroupID == r.Group)) {
			return true
		}
	}
	return false
}
func (b *Bridge) Allowed(p Principal, id, permission string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.allowed(p, id, permission, "")
}
func (b *Bridge) Catalog(p Principal) []Instance {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := []Instance{}
	for id, r := range b.state.Instances {
		if b.allowed(p, id, "instance.list", "") {
			r.Credential = ""
			_, r.Available = b.active[id]
			out = append(out, r)
		}
	}
	slices.SortFunc(out, func(a, c Instance) int { return strings.Compare(a.ID, c.ID) })
	return out
}
func (b *Bridge) Enroll(alias, group string) (Instance, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(alias) > 128 || len(b.state.Instances) >= 4096 {
		return Instance{}, "", Fail("resource_exhausted")
	}
	if b.state.Groups[group] == "" {
		return Instance{}, "", Fail("not_found")
	}
	if alias != "" {
		for _, r := range b.state.Instances {
			if r.Alias == alias {
				return Instance{}, "", Fail("identity_conflict")
			}
		}
	}
	token := secret()
	r := Instance{ID: protocol.NewID(), Alias: alias, Group: group, Credential: hash(token), Generation: "0"}
	b.state.Instances[r.ID] = r
	if err := b.save(); err != nil {
		return Instance{}, "", err
	}
	r.Credential = ""
	return r, token, nil
}
func (b *Bridge) Attach(id, credential, boot string, endpoint Endpoint) (string, error) {
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
	b.active[id] = registration{r.Generation, endpoint}
	return r.Generation, nil
}
func (b *Bridge) Detach(id, generation string) {
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
func (b *Bridge) Available(id string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.active[id]
	return ok && !b.closed && !b.failed
}
func (b *Bridge) Invite(grants []Grant) (Invitation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now().Unix()
	for id, i := range b.state.Invitations {
		if i.Expires < now {
			delete(b.state.Invitations, id)
		}
	}
	if len(b.state.Invitations) >= 128 {
		return Invitation{}, Fail("resource_exhausted")
	}
	// Proposed selectors are committed at approval, so fixed selectors snapshot
	// the owner's approved instance set, never a claimant-controlled list.
	grants = append([]Grant(nil), grants...)
	for i := range grants {
		grants[i].Instances = slices.Clone(grants[i].Instances)
		grants[i].Permissions = slices.Clone(grants[i].Permissions)
		grants[i].Principal = b.Principal()
		if _, err := b.prepareGrant(grants[i]); err != nil {
			return Invitation{}, err
		}
	}
	token := secret()
	inv := Invitation{ID: protocol.NewID(), PeerID: b.PeerID(), Token: token, Expires: now + 600, Grants: grants, Status: "pending"}
	stored := inv
	stored.Token = ""
	b.state.Invitations[inv.ID] = invitationState{stored, hash(token)}
	if err := b.save(); err != nil {
		return Invitation{}, err
	}
	return cloneInvitation(inv), nil
}
func (b *Bridge) Claim(peer, id, token string, locators ...string) (Invitation, error) {
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
func (b *Bridge) PairStatus(peer, id string) (Invitation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	i, ok := b.state.Invitations[id]
	if !ok || i.Claimant != peer || b.state.Blocked[peer] || (i.Status != "approved" && i.Expires < time.Now().Unix()) {
		return Invitation{}, Fail("not_found")
	}
	return cloneInvitation(i.Invitation), nil
}
func (b *Bridge) Approve(id, claimant string) error {
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
	if len(b.state.Grants)+len(i.Grants) > 1024 {
		return Fail("resource_exhausted")
	}
	approved := make([]Grant, 0, len(i.Grants))
	for _, g := range i.Grants {
		g.Principal = Principal{PeerID: claimant, Subject: Subject{Kind: "controller"}}
		g, err := b.prepareGrant(g)
		if err != nil {
			return err
		}
		g.ID = protocol.NewID()
		approved = append(approved, g)
	}
	if i.ClaimantLocator != "" {
		if len(b.state.Peers) >= 128 && b.state.Peers[claimant] == "" {
			return Fail("resource_exhausted")
		}
		b.state.Peers[claimant] = i.ClaimantLocator
	}
	b.state.Grants = append(b.state.Grants, approved...)
	i.Status = "approved"
	b.state.Invitations[id] = i
	return b.save()
}
func (b *Bridge) Block(peer string) error {
	b.mu.Lock()
	b.state.Blocked[peer] = true
	for id, i := range b.state.Invitations {
		if i.Claimant == peer {
			delete(b.state.Invitations, id)
		}
	}
	channels := b.channels[peer]
	delete(b.channels, peer)
	err := b.save()
	b.mu.Unlock()
	for c := range channels {
		_ = c.Close()
	}
	return err
}
func (b *Bridge) Close() error {
	b.mu.Lock()
	b.closed = true
	active := b.active
	b.active = map[string]registration{}
	channels := b.channels
	b.channels = map[string]map[*protocol.Conn]bool{}
	b.mu.Unlock()
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
	case "inspect":
		return "instance.inspect"
	case "session.list", "session.new", "session.switch", "session.fork", "session.model":
		return "instance.session.manage"
	default:
		return "instance." + method
	}
}
func (b *Bridge) Call(ctx context.Context, p Principal, call Call) (json.RawMessage, error) {
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
		i.Grants[n].Instances = slices.Clone(i.Grants[n].Instances)
		i.Grants[n].Permissions = slices.Clone(i.Grants[n].Permissions)
	}
	return i
}

func (b *Bridge) Changes() <-chan struct{} { return b.changes }
