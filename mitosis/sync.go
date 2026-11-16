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
	"encoding/json"
	"maps"
	"math/big"
	"slices"

	"github.com/OrdalieTech/orb/mitosis/protocol"
)

// A profile's sync group is a scope whose first member is its owner. Only the owner admits and
// removes devices, and only the owner's vault reaches every member; a member's vault is sealed to
// the owner alone. Each device's vault rides its own signed record in the group scope.
const vaultSchema = "orb.mitosis-vault/1"

type vault struct {
	Schema    string            `json:"schema"`
	Bundle    string            `json:"bundle"`
	Envelopes map[string]string `json:"envelopes"`
}

// bundle is what a group shares, one JSON object per data class, merged by entry name.
type bundle struct {
	Credentials map[string]json.RawMessage `json:"credentials"`
}

// Credentials is the class a sync group shares, as its host keeps it (ai/auth's Store over
// auth.json): what this device may share, and the merge of what the group shared.
type Credentials interface {
	Shareable(ctx context.Context) (map[string]json.RawMessage, error)
	MergeShared(ctx context.Context, incoming []map[string]json.RawMessage, keepOwn bool, removed []string) ([]string, error)
}

func (b *Mitosis) SetCredentials(c Credentials) {
	b.mu.Lock()
	b.credentials = c
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
		b.state.Scopes[b.state.Group] = []string{join, self}
	case remove != "" && len(members) == 0:
		return Fail("not_found")
	case remove != "" && members[0] == remove:
		delete(b.state.Scopes, b.state.Group)
		b.state.Group, b.state.Shared, b.state.Merged = "", nil, nil
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
			if b.state.Peers[add] == "" {
				return Fail("not_found") // a device this one never paired with is never reached
			}
			if len(members) >= 128 {
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
// are where this device is reached; nil keeps the ones it published before. An added device
// whose envelope no longer fits the owner's record is taken out again.
func (b *Mitosis) SyncAdmin(ctx context.Context, params json.RawMessage, endpoints []Locator) (json.RawMessage, error) {
	var p struct {
		Join   string `json:"join,omitempty"`
		Add    string `json:"add,omitempty"`
		Remove string `json:"remove,omitempty"`
	}
	if err := protocol.Decode(params, &p); err != nil {
		return nil, err
	}
	if err := b.setGroup(p.Join, p.Add, p.Remove); err != nil {
		return nil, err
	}
	b.mu.Lock()
	left := b.state.Group == ""
	b.mu.Unlock()
	if left && p.Remove != "" {
		return JSON(struct{}{}), nil
	}
	result, err := b.Sync(ctx, endpoints)
	if p.Add != "" && Code(err) == "resource_exhausted" {
		_ = b.setGroup("", "", p.Add)
	}
	return result, err
}

// Sync runs one round of the group: the owner folds in what members handed it, keeping its own,
// then seals the whole set to every current, unblocked member; a member takes the owner's set
// over its own, drops what the owner stopped sharing, and hands the owner only what its set lacks
// or holds otherwise. It returns the group and the entries the round changed here.
func (b *Mitosis) Sync(ctx context.Context, endpoints []Locator) (json.RawMessage, error) {
	b.mu.Lock()
	group, credentials, self := b.state.Group, b.credentials, b.PeerID()
	members := b.state.Scopes[group]
	if group == "" || len(members) == 0 || credentials == nil {
		b.mu.Unlock()
		return nil, Fail("not_found")
	}
	owner := members[0]
	var incoming []map[string]json.RawMessage
	var ownerSet map[string]json.RawMessage
	var removed []string
	// The owner merges each member's hand-in once: an entry it deleted since comes back only
	// when the member hands it in again.
	merged := map[string]string{}
	if owner == self {
		for _, peer := range members[1:] {
			if in, revision, ok := b.received(group, peer); ok && b.state.Merged[peer] != revision {
				incoming = append(incoming, in.Credentials)
				merged[peer] = revision
			}
		}
	} else if in, _, ok := b.received(group, owner); ok {
		ownerSet = in.Credentials
		if ownerSet == nil {
			ownerSet = map[string]json.RawMessage{}
		}
		incoming = append(incoming, ownerSet)
		for _, name := range b.state.Shared {
			if _, kept := ownerSet[name]; !kept {
				removed = append(removed, name)
			}
		}
	}
	b.mu.Unlock()

	changed, err := credentials.MergeShared(ctx, incoming, owner == self, removed)
	if err != nil {
		return nil, err
	}
	shared, err := credentials.Shareable(ctx)
	if err != nil {
		return nil, err
	}
	for name, entry := range shared {
		if ownerSet != nil && bytes.Equal(ownerSet[name], entry) {
			delete(shared, name)
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Group != group || len(b.state.Scopes[group]) == 0 || b.state.Scopes[group][0] != owner {
		return nil, Fail("not_found")
	}
	recipients := []string{owner}
	if owner == self {
		recipients = slices.Clone(b.state.Scopes[group][1:])
	}
	recipients = slices.DeleteFunc(recipients, func(p string) bool { return b.state.Blocked[p] })
	if len(merged) > 0 {
		if b.state.Merged == nil {
			b.state.Merged = map[string]string{}
		}
		maps.Copy(b.state.Merged, merged)
		if err = b.save(); err != nil {
			return nil, err
		}
	}
	if ownerSet != nil {
		names := slices.Sorted(maps.Keys(ownerSet))
		if !slices.Equal(names, b.state.Shared) {
			b.state.Shared = names
			if err = b.save(); err != nil {
				return nil, err
			}
		}
	}
	r, published := b.state.Records[group+"/"+self]
	if published && endpoints == nil {
		c, _ := r.Verify()
		endpoints = c.Endpoints
	}
	if digest := string(JSON([]any{recipients, shared, endpoints, b.name})); !published || digest != b.synced {
		var ext map[string]json.RawMessage
		if len(recipients) > 0 {
			v, err := seal(recipients, bundle{Credentials: shared})
			if err != nil {
				return nil, err
			}
			ext = map[string]json.RawMessage{"vault": JSON(v)}
		}
		if _, err = b.publishContact(group, b.name, endpoints, false, ext); err != nil {
			return nil, err
		}
		b.synced = digest
	}
	if changed == nil {
		changed = []string{}
	}
	slices.Sort(changed)
	return JSON(map[string]any{"group_id": group, "owner": owner, "members": b.state.Scopes[group], "received": changed}), nil
}

// received opens the vault peer published to the group, if it is sealed to this device. A member
// also takes the owner's recipients as the group, so it gossips with them while the owner is away.
func (b *Mitosis) received(group, peer string) (bundle, string, bool) {
	r, ok := b.state.Records[group+"/"+peer]
	if !ok || b.state.Blocked[peer] {
		return bundle{}, "", false
	}
	c, err := r.Verify()
	var v vault
	if err != nil || c.Withdrawn || json.Unmarshal(c.Extensions["vault"], &v) != nil || v.Schema != vaultSchema {
		return bundle{}, "", false
	}
	in, err := b.open(v)
	if err != nil {
		return bundle{}, "", false
	}
	if members := b.state.Scopes[group]; members[0] == peer {
		next := []string{peer}
		for id := range v.Envelopes {
			next = append(next, id)
		}
		slices.Sort(next[1:])
		b.state.Scopes[group] = next
	}
	return in, c.Revision, true
}

// seal encrypts the bundle once under a fresh key, and that key to each recipient's X25519 key.
func seal(recipients []string, in bundle) (vault, error) {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	sealed, err := aead(key, JSON(in), nil, true)
	if err != nil {
		return vault{}, err
	}
	v := vault{Schema: vaultSchema, Bundle: base64.RawURLEncoding.EncodeToString(sealed), Envelopes: map[string]string{}}
	for _, peer := range recipients {
		to, err := sealingKey(peer)
		if err != nil {
			return vault{}, err
		}
		ephemeral, _ := ecdh.X25519().GenerateKey(rand.Reader)
		shared, err := ephemeral.ECDH(to)
		if err != nil {
			return vault{}, err
		}
		pub := ephemeral.PublicKey().Bytes()
		envelope, err := aead(envelopeKey(shared, pub, to.Bytes()), key, pub, true)
		if err != nil {
			return vault{}, err
		}
		v.Envelopes[peer] = base64.RawURLEncoding.EncodeToString(append(pub, envelope...))
	}
	return v, nil
}

func (b *Mitosis) open(v vault) (bundle, error) {
	envelope, err := base64.RawURLEncoding.DecodeString(v.Envelopes[b.PeerID()])
	if err != nil || len(envelope) < 32 {
		return bundle{}, Fail("unauthorized")
	}
	sum := sha512.Sum512(b.state.Key[:ed25519.SeedSize])
	self, err := ecdh.X25519().NewPrivateKey(sum[:32])
	if err != nil {
		return bundle{}, err
	}
	pub := envelope[:32]
	ephemeral, err := ecdh.X25519().NewPublicKey(pub)
	if err != nil {
		return bundle{}, err
	}
	shared, err := self.ECDH(ephemeral)
	if err != nil {
		return bundle{}, err
	}
	key, err := aead(envelopeKey(shared, pub, self.PublicKey().Bytes()), envelope[32:], pub, false)
	if err != nil {
		return bundle{}, err
	}
	sealed, err := base64.RawURLEncoding.DecodeString(v.Bundle)
	if err != nil {
		return bundle{}, err
	}
	plain, err := aead(key, sealed, nil, false)
	var out bundle
	if err == nil {
		err = json.Unmarshal(plain, &out)
	}
	return out, err
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
