package mitosis

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"maps"
	"math/big"
	"slices"
	"time"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// A profile's sync group is a scope whose first member is its owner. Only the owner admits and
// removes devices, and only the owner's vaults reach every member; a member's are sealed to the
// owner alone. A device's signed record in the group scope carries only its vaults' digests and
// sizes, one vault per data class; a vault is fetched by digest (vault.get) from its publisher or
// any member holding it.
const (
	vaultSchema = "orb.mitosis-vault/2"
	// maxVault bounds a device's vaults together, which every member holds whole in memory, a
	// browser tab included, and the owner sends to each: megabytes of memory fit, files do not.
	maxVault = 64 << 20
	// vaultChunk is one vault.get answer, well under the 1 MiB frame once base64-encoded.
	vaultChunk = 512 << 10
	// envelope: the recipient's Ed25519 key, an ephemeral X25519 key, and the sealed bundle key.
	envelopeSize = 32 + 32 + 12 + 32 + 16
	// MaxPeers bounds the devices one Mitosis pairs with, holds channels to, or counts in a scope:
	// their locators and records stay well within the 1 MiB profile state.
	MaxPeers = 256
)

// pointer is what a record says of one of its publisher's vaults.
type pointer struct {
	Schema string `json:"schema"`
	Digest string `json:"digest"` // base64url SHA-256 of the vault
	Size   int    `json:"size"`
}

// bundle is what a group shares: per data class, entries by name; each class is its own vault.
type bundle map[string]map[string]json.RawMessage

// A Class is one kind of profile data a sync group shares, as its host keeps it, by entry name:
// Shareable is what this device may share; MergeShared folds in what the group shared and returns
// the entries it changed. keepOwn (the owner) takes only entries it lacks, or a fresher form of
// one by the class's own rule; a member takes the owner's entries over its own and drops removed,
// those the owner stopped sharing.
type Class interface {
	Shareable(ctx context.Context) (map[string]json.RawMessage, error)
	MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error)
}

// SetClass shares a data class with the sync group under name; nil stops sharing it.
func (b *Mitosis) SetClass(name string, c Class) {
	b.mu.Lock()
	if c == nil {
		delete(b.classes, name)
	} else {
		b.classes[name] = c
	}
	b.mu.Unlock()
}

func groupID(owner string) string {
	sum := sha256.Sum256([]byte("orb-mitosis-group/" + owner))
	return base64.RawURLEncoding.EncodeToString(sum[:16])
}

// setGroup applies one membership change: join makes owner's group this profile's own, add and
// remove are the owner's, and the first add creates the group. A member leaves by removing the
// owner, so it can follow or start another group when the owner is gone for good.
func (b *Mitosis) setGroup(join, add, remove string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	self := b.PeerID()
	for _, p := range []string{join, add, remove} {
		if p == self {
			return Fail("not_found")
		}
		if _, err := ParsePeerID(p); p != "" && err != nil {
			return err
		}
	}
	members := b.state.Scopes[b.state.Group]
	switch {
	case join != "":
		if len(b.state.Scopes) >= 128 && len(members) == 0 {
			return Fail("resource_exhausted")
		}
		delete(b.state.Scopes, b.state.Group)
		b.state.Group, b.state.Shared, b.state.Merged = groupID(join), nil, nil
		clear(b.vaults)
		clear(b.sealed)
		b.state.Scopes[b.state.Group] = []string{join, self}
	case remove != "" && len(members) == 0:
		return Fail("not_found")
	case remove != "" && members[0] == remove:
		delete(b.state.Scopes, b.state.Group)
		b.state.Group, b.state.Shared, b.state.Merged = "", nil, nil
		clear(b.vaults)
		clear(b.sealed)
	case add != "" || remove != "":
		if len(members) == 0 {
			if len(b.state.Scopes) >= 128 {
				return Fail("resource_exhausted")
			}
			b.state.Group = groupID(self)
			members = []string{self}
		}
		if members[0] != self {
			return Fail("unauthorized")
		}
		if add != "" && !slices.Contains(members, add) {
			if b.state.Peers[add] == "" && !slices.ContainsFunc(b.state.Grants, func(g Grant) bool { return g.Principal.PeerID == add }) {
				return Fail("not_found") // a device this one never paired with is never reached
			}
			if len(members) >= MaxPeers {
				return Fail("resource_exhausted")
			}
			members = append(members, add)
		}
		b.state.Scopes[b.state.Group] = slices.DeleteFunc(members, func(p string) bool { return p == remove })
	default:
		return nil
	}
	return b.save()
}

// SyncAdmin is the sync admin method: one membership change, then a round. endpoints, when set,
// are where this device is reached; nil keeps the ones it published before.
func (b *Mitosis) SyncAdmin(ctx context.Context, params json.RawMessage, endpoints []Locator) (json.RawMessage, error) {
	var p struct {
		Join   string `json:"join,omitempty"`
		Add    string `json:"add,omitempty"`
		Remove string `json:"remove,omitempty"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	b.round.Lock()
	defer b.round.Unlock()
	if err := b.setGroup(p.Join, p.Add, p.Remove); err != nil {
		return nil, err
	}
	b.mu.Lock()
	group := b.state.Group
	b.mu.Unlock()
	if group == "" && p.Remove != "" {
		return JSON(struct{}{}), nil
	}
	out, err := b.sync(ctx, endpoints)
	if p.Remove != "" {
		b.tellRemoved(b.Connection(p.Remove), group)
	}
	return out, err
}

// Sync runs one round of the group: the owner folds in what members handed it, keeping its own,
// then seals the whole set to every current, unblocked member; a member takes the owner's set
// over its own, drops what the owner stopped sharing, and hands the owner only what its set lacks
// or holds otherwise. It returns the group and, per class, the entries the round changed here.
func (b *Mitosis) Sync(ctx context.Context, endpoints []Locator) (json.RawMessage, error) {
	b.round.Lock()
	defer b.round.Unlock()
	return b.sync(ctx, endpoints)
}

func (b *Mitosis) sync(ctx context.Context, endpoints []Locator) (json.RawMessage, error) {
	b.mu.Lock()
	group, classes, self := b.state.Group, maps.Clone(b.classes), b.PeerID()
	members := slices.Clone(b.state.Scopes[group])
	if group == "" || len(members) == 0 || len(classes) == 0 {
		b.mu.Unlock()
		return nil, Fail("not_found")
	}
	owner := members[0]
	// The owner opens each member's hand-in of a class once: an entry it deleted since comes back
	// only when the member hands it in again. A member opens the owner's vault of every class.
	senders := []string{owner}
	if owner == self {
		senders = members[1:]
	}
	type sent struct {
		peer, class string
		vault       pointer
	}
	var pending []sent
	ownerVaults := map[string]pointer{}
	for _, peer := range senders {
		if b.state.Blocked[peer] {
			continue
		}
		for class, v := range b.pointers(group, peer) {
			if classes[class] != nil && (owner != self || b.state.Merged[peer+" "+class] != v.Digest) {
				pending = append(pending, sent{peer, class, v})
			}
			if peer == owner {
				ownerVaults[class] = v
			}
		}
	}
	shared := maps.Clone(b.state.Shared)
	b.mu.Unlock()

	var handins []bundle
	var ownerSet bundle
	var excluded []string // the owner's recipients, when its vaults seal to others only
	var ownerRecipients []string
	opened := map[string][]byte{}
	merged := map[string]string{}
	stale := map[string]bool{}
	for _, s := range pending {
		blob, err := b.fetch(ctx, s.vault, s.peer, s.peer == owner)
		var in map[string]json.RawMessage
		var recipients []string
		if err == nil {
			in, recipients, err = b.open(blob)
		}
		switch {
		case s.peer != owner && err == nil:
			handins = append(handins, bundle{s.class: in})
			merged[s.peer+" "+s.class] = s.vault.Digest
		case s.peer == owner && err != nil:
			// Until it opens the owner's vault of a class, a member leaves that class as it was: it
			// cannot tell what the owner removed from what it lacks. A large memory vault still on
			// its way holds back no credential.
			stale[s.class] = true
			if Code(err) == "unauthorized" && recipients != nil {
				excluded = recipients
			}
		case s.peer == owner:
			if ownerSet == nil {
				ownerSet = bundle{}
			}
			ownerSet[s.class], ownerRecipients = in, recipients
			opened[s.vault.Digest] = blob
		}
	}
	if ownerSet == nil && len(stale) > 0 && excluded == nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !slices.Contains(b.state.Scopes[group], self) { // refused by the group (Refused)
			return b.groupResult(group, owner, false, nil), nil
		}
		return nil, Fail("unavailable")
	}
	if ownerSet == nil && excluded != nil {
		// The owner removed this device, or has not added it yet: its group is who the owner seals to.
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.state.Group == group {
			slices.Sort(excluded)
			b.state.Scopes[group] = append([]string{owner}, slices.DeleteFunc(excluded, func(p string) bool { return p == owner })...)
			if err := b.save(); err != nil {
				return nil, err
			}
		}
		return b.groupResult(group, owner, false, nil), nil
	}
	if owner != self && len(ownerVaults) > 0 && ownerSet == nil {
		ownerSet = bundle{} // the owner shares none of this device's classes
	}

	out := bundle{}
	received := map[string][]string{}
	for name, class := range classes {
		if stale[name] {
			continue
		}
		var incoming []map[string]json.RawMessage
		var removed []string
		for _, in := range handins {
			incoming = append(incoming, in[name])
		}
		if ownerSet != nil {
			set := ownerSet[name]
			incoming = append(incoming, set)
			for _, entry := range shared[name] {
				if _, kept := set[entry]; !kept {
					removed = append(removed, entry)
				}
			}
		}
		changed, err := class.MergeShared(ctx, incoming, owner == self, removed)
		if err != nil {
			return nil, err
		}
		own, err := class.Shareable(ctx)
		if err != nil {
			return nil, err
		}
		for entry, value := range own {
			if ownerSet != nil && bytes.Equal(ownerSet[name][entry], value) {
				delete(own, entry)
			}
		}
		out[name] = own
		if len(changed) > 0 {
			received[name] = slices.Sorted(slices.Values(changed))
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Group != group || len(b.state.Scopes[group]) == 0 || b.state.Scopes[group][0] != owner {
		return nil, Fail("not_found")
	}
	save := len(merged) > 0
	if b.state.Merged == nil {
		b.state.Merged = map[string]string{}
	}
	maps.Copy(b.state.Merged, merged)
	if ownerSet != nil {
		if ownerRecipients != nil {
			// A member gossips with the owner's recipients, so it catches up while the owner is away.
			slices.Sort(ownerRecipients)
			b.state.Scopes[group] = append([]string{owner}, slices.DeleteFunc(ownerRecipients, func(p string) bool { return p == owner })...)
		}
		if b.state.Shared == nil {
			b.state.Shared = map[string][]string{}
		}
		for name := range classes {
			if names := slices.Sorted(maps.Keys(ownerSet[name])); !stale[name] && !slices.Equal(names, b.state.Shared[name]) {
				b.state.Shared[name], save = names, true
			}
		}
	}
	if save {
		if err := b.save(); err != nil {
			return nil, err
		}
	}
	recipients := []string{owner}
	if owner == self {
		recipients = slices.Clone(b.state.Scopes[group][1:])
	}
	recipients = slices.DeleteFunc(recipients, func(p string) bool { return b.state.Blocked[p] })
	r, published := b.state.Records[group+"/"+self]
	if published && endpoints == nil {
		c, _ := r.Verify()
		endpoints = c.Endpoints
	}
	// Each class is sealed apart, and again only when what it seals changes: a refreshed token
	// is not a reason to send every member the whole memory.
	vaults := map[string]pointer{}
	total := 0
	b.handing = false
	for name, entries := range out {
		b.handing = b.handing || owner != self && len(entries) > 0
		if len(recipients) == 0 {
			break
		}
		sum := sha256.Sum256(JSON([]any{recipients, entries}))
		last, ok := b.sealed[name]
		if !ok || last.sum != sum || b.vaults[last.vault.Digest] == nil {
			blob, err := seal(recipients, entries)
			if err != nil {
				return nil, err
			}
			last = sealed{sum, pointer{vaultSchema, vaultDigest(blob), len(blob)}}
			b.sealed[name], b.vaults[last.vault.Digest] = last, blob
		}
		vaults[name] = last.vault
		if total += last.vault.Size; total > maxVault {
			return nil, Fail("resource_exhausted")
		}
	}
	for name := range stale {
		if last, ok := b.sealed[name]; ok {
			vaults[name] = last.vault // the hand-in stands until the owner's vault opens
		}
	}
	sum := sha256.Sum256(JSON([]any{vaults, endpoints, b.name}))
	if digest := string(sum[:]); !published || digest != b.synced {
		var ext map[string]json.RawMessage
		if len(vaults) > 0 {
			ext = map[string]json.RawMessage{"vaults": JSON(vaults)}
		}
		if _, err := b.publishContact(group, b.name, endpoints, false, ext); err != nil {
			return nil, err
		}
		b.synced = digest
	}
	// This device serves its own vaults and, as a member, the owner's it opened; nothing else.
	keep := map[string]bool{}
	for _, peer := range []string{self, owner} {
		for _, v := range b.pointers(group, peer) {
			keep[v.Digest] = true
		}
	}
	for digest, blob := range opened {
		if keep[digest] {
			b.vaults[digest] = blob
		}
	}
	maps.DeleteFunc(b.vaults, func(digest string, _ []byte) bool { return !keep[digest] })
	maps.DeleteFunc(b.sealed, func(_ string, s sealed) bool { return !keep[s.vault.Digest] })
	return b.groupResult(group, owner, owner == self || ownerSet != nil, received), nil
}

// groupResult is what a round reports: the group, its members and their names, whether this
// device holds the owner's set (in_sync), and per class the entries the round changed here.
func (b *Mitosis) groupResult(group, owner string, inSync bool, received map[string][]string) json.RawMessage {
	if received == nil {
		received = map[string][]string{}
	}
	names := map[string]string{}
	for _, p := range b.state.Scopes[group] {
		if r, ok := b.state.Records[group+"/"+p]; ok {
			if c, err := r.Verify(); err == nil {
				names[p] = c.Name // a browser tab has none: present still means it published here
			}
		}
	}
	return JSON(map[string]any{"group_id": group, "self": b.PeerID(), "owner": owner, "members": b.state.Scopes[group], "names": names, "in_sync": inSync, "received": received})
}

// Refused is a member of this device's sync group refusing it the group's records: that member
// does not count it, as the owner does not once it removed or blocked it (or not yet, which the
// owner's next vault sealed to it corrects). The group then reads without this device, and the
// owner's vaults it holds are dropped, so an old one no longer says otherwise.
func (b *Mitosis) Refused(scope string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	members, self := b.state.Scopes[scope], b.PeerID()
	if scope != b.state.Group || len(members) == 0 || members[0] == self || !slices.Contains(members, self) {
		return
	}
	for _, v := range b.pointers(scope, members[0]) {
		delete(b.vaults, v.Digest)
	}
	b.state.Scopes[scope] = slices.DeleteFunc(slices.Clone(members), func(p string) bool { return p == self })
	_ = b.save()
}

// tellRemoved hands a device the owner just left out its new record, whose vaults no longer seal
// to it, over a channel already open, so it stops showing itself a member and no older vault a
// lagging member still serves brings it back; one offline learns it when the group refuses it.
func (b *Mitosis) tellRemoved(c *protocol.Conn, group string) {
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b.mu.Lock()
	r, ok := b.state.Records[group+"/"+b.PeerID()]
	b.mu.Unlock()
	if ok {
		_ = c.Call(ctx, "peers.publish", map[string]any{"records": []Record{r}}, nil)
	}
	_ = c.Call(ctx, "sync.removed", map[string]string{"group_id": group}, nil)
}

// sealed is a class this device last sealed: a digest of what it sealed, and its vault.
type sealed struct {
	sum   [32]byte
	vault pointer
}

// pointers reads the vaults, by class, that peer's record in the group points at: none unless
// every one is well formed and together they stay within maxVault.
func (b *Mitosis) pointers(group, peer string) map[string]pointer {
	r, ok := b.state.Records[group+"/"+peer]
	if !ok {
		return nil
	}
	var vaults map[string]pointer
	c, err := r.Verify()
	if err != nil || c.Withdrawn || json.Unmarshal(c.Extensions["vaults"], &vaults) != nil || len(vaults) > 16 {
		return nil
	}
	total := 0
	for _, v := range vaults {
		if total += v.Size; v.Schema != vaultSchema || len(v.Digest) != 43 || v.Size < 2 || total > maxVault {
			return nil
		}
	}
	return vaults
}

func vaultDigest(blob []byte) string {
	sum := sha256.Sum256(blob)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// fetch returns the vault v points at, from this device or over a channel already open, in either
// direction, to its publisher first, then (for the owner's, relayed) to any other peer: a Worker object cannot dial, so it
// fetches over the streams its peers opened, and a device just added knows no member but the
// owner yet. Holders serve only the vault's recipients, and it is checked against the digest its
// publisher signed before anything opens it.
func (b *Mitosis) fetch(ctx context.Context, v pointer, publisher string, relayed bool) ([]byte, error) {
	b.mu.Lock()
	blob := b.vaults[v.Digest]
	holders := slices.Collect(maps.Keys(b.channels)) // map order spreads a new device's fetches
	b.mu.Unlock()
	if blob != nil {
		return blob, nil
	}
	if i := slices.Index(holders, publisher); i > 0 {
		holders[0], holders[i] = holders[i], holders[0]
	}
	if !relayed { // a member's hand-in lives on its publisher alone: no channel, no round spent asking
		holders = slices.DeleteFunc(holders, func(h string) bool { return h != publisher })
	}
	for _, holder := range holders {
		c := b.Connection(holder)
		if c == nil {
			continue
		}
		blob = nil
		for len(blob) < v.Size {
			var chunk struct {
				Size int    `json:"size"`
				Data []byte `json:"data"`
			}
			call, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := c.Call(call, "vault.get", map[string]any{"digest": v.Digest, "offset": len(blob)}, &chunk)
			cancel()
			if err != nil || chunk.Size != v.Size || len(chunk.Data) == 0 || len(blob)+len(chunk.Data) > v.Size {
				break
			}
			blob = append(blob, chunk.Data...)
		}
		if len(blob) == v.Size && vaultDigest(blob) == v.Digest {
			return blob, nil
		}
	}
	return nil, Fail("unavailable")
}

// serveVault answers vault.get: a vault this device holds, only to a current, unblocked member of
// its group that the vault is sealed to.
func (b *Mitosis) serveVault(peer string, params json.RawMessage) (json.RawMessage, error) {
	var p struct {
		Digest string `json:"digest"`
		Offset int    `json:"offset"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	b.mu.Lock()
	blob := b.vaults[p.Digest]
	member := b.state.Group != "" && !b.state.Blocked[peer] && slices.Contains(b.state.Scopes[b.state.Group], peer)
	b.mu.Unlock()
	envelopes, _, err := split(blob)
	if !member || err != nil || envelopes[peer] == nil || p.Offset < 0 || p.Offset >= len(blob) {
		return nil, Fail("not_found")
	}
	return JSON(map[string]any{"size": len(blob), "data": blob[p.Offset:min(len(blob), p.Offset+vaultChunk)]}), nil
}

// seal encrypts a class's entries once under a fresh key, and that key to each recipient's X25519
// key. A vault is the recipient count (two bytes, big-endian), one envelope per recipient, then
// the sealed entries.
func seal(recipients []string, in map[string]json.RawMessage) ([]byte, error) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	out := binary.BigEndian.AppendUint16(nil, uint16(len(recipients)))
	for _, peer := range recipients {
		id, err := ParsePeerID(peer)
		if err != nil {
			return nil, err
		}
		to, err := sealingKey(peer)
		if err != nil {
			return nil, err
		}
		ephemeral, _ := ecdh.X25519().GenerateKey(rand.Reader)
		shared, err := ephemeral.ECDH(to)
		if err != nil {
			return nil, err
		}
		pub := ephemeral.PublicKey().Bytes()
		envelope, err := aead(envelopeKey(shared, pub, to.Bytes()), key, pub, true)
		if err != nil {
			return nil, err
		}
		out = slices.Concat(out, id, pub, envelope)
	}
	sealed, err := aead(key, JSON(in), nil, true)
	if err != nil {
		return nil, err
	}
	if out = append(out, sealed...); len(out) > maxVault {
		return nil, Fail("resource_exhausted")
	}
	return out, nil
}

// split reads a vault's envelopes, by recipient, and its sealed bundle.
func split(blob []byte) (map[string][]byte, []byte, error) {
	if len(blob) < 2 {
		return nil, nil, Fail("unauthorized")
	}
	n := int(binary.BigEndian.Uint16(blob))
	if len(blob) < 2+n*envelopeSize {
		return nil, nil, Fail("unauthorized")
	}
	envelopes := make(map[string][]byte, n)
	for i := range n {
		e := blob[2+i*envelopeSize:][:envelopeSize]
		envelopes[PeerID(ed25519.PublicKey(e[:32]))] = e[32:]
	}
	return envelopes, blob[2+n*envelopeSize:], nil
}

// open decrypts a vault sealed to this device and returns its entries and recipients.
func (b *Mitosis) open(blob []byte) (map[string]json.RawMessage, []string, error) {
	envelopes, sealed, err := split(blob)
	if err != nil {
		return nil, nil, err
	}
	envelope := envelopes[b.PeerID()]
	if envelope == nil {
		return nil, slices.Collect(maps.Keys(envelopes)), Fail("unauthorized")
	}
	sum := sha512.Sum512(b.state.Key[:ed25519.SeedSize])
	self, err := ecdh.X25519().NewPrivateKey(sum[:32])
	if err != nil {
		return nil, nil, err
	}
	pub := envelope[:32]
	ephemeral, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	shared, err := self.ECDH(ephemeral)
	if err != nil {
		return nil, nil, err
	}
	key, err := aead(envelopeKey(shared, pub, self.PublicKey().Bytes()), envelope[32:], pub, false)
	if err != nil {
		return nil, nil, err
	}
	plain, err := aead(key, sealed, nil, false)
	var out map[string]json.RawMessage
	if err == nil {
		err = json.Unmarshal(plain, &out)
	}
	return out, slices.Collect(maps.Keys(envelopes)), err
}

// sealingKey is the Montgomery form of a peer's Ed25519 identity (RFC 7748 §4.1), the X25519 key
// whose private half is the clamped SHA-512 of its seed, so sealing to a peer needs only its PeerID.
func sealingKey(peer string) (*ecdh.PublicKey, error) {
	key, err := ParsePeerID(peer)
	if err != nil {
		return nil, err
	}
	le := slices.Clone(key)
	le[31] &= 0x7f
	slices.Reverse(le)
	p := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 255), big.NewInt(19))
	y := new(big.Int).SetBytes(le)
	inverse := new(big.Int).ModInverse(new(big.Int).Mod(new(big.Int).Sub(big.NewInt(1), y), p), p)
	if inverse == nil {
		return nil, Fail("unauthorized")
	}
	u := new(big.Int).Mul(new(big.Int).Add(big.NewInt(1), y), inverse)
	out := u.Mod(u, p).FillBytes(make([]byte, 32))
	slices.Reverse(out)
	return ecdh.X25519().NewPublicKey(out)
}

func envelopeKey(shared, ephemeral, recipient []byte) []byte {
	sum := sha256.Sum256(slices.Concat([]byte("orb-mitosis-seal/1\n"), shared, ephemeral, recipient))
	return sum[:]
}

// aead is AES-256-GCM with a random nonce prefixed to the ciphertext.
func aead(key, data, ad []byte, encrypt bool) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, err
	}
	if encrypt {
		return gcm.Seal(nil, nil, data, ad), nil
	}
	return gcm.Open(nil, nil, data, ad)
}
