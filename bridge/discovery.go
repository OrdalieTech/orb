package bridge

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/bridge/vault"
)

// A Bridge is the Store the vault sync orchestration drives.
var _ vault.Store = (*Bridge)(nil)

// OpenVault reads the scope's current vault and decrypts it with this peer's
// identity-derived key. ok is false (no error) when nothing is sealed to this
// peer — never admitted, or revoked — so callers leave local secrets untouched.
// Decryption uses the private key, which never leaves this process.
func (b *Bridge) OpenVault(scope string) (vault.Bundle, bool, error) {
	priv, err := vault.X25519FromSeed(b.state.Key[:ed25519.SeedSize])
	if err != nil {
		return vault.Bundle{}, false, err
	}
	return vault.Apply(b, scope, b.PeerID(), priv)
}

// X25519Public is this peer's Mitosis sealing key, derived from its Ed25519
// identity (vault.X25519FromSeed), so profile secrets can be sealed to it.
func (b *Bridge) X25519Public() ([]byte, error) {
	k, err := vault.X25519FromSeed(b.state.Key[:ed25519.SeedSize])
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// SetVault records the profile vault this peer publishes to a scope; the next
// PublishContact carries it. Passing a nil document clears it.
func (b *Bridge) SetVault(scope string, doc *vault.Document) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Scopes[scope] == nil {
		return Fail("unauthorized")
	}
	if doc == nil {
		delete(b.state.Vaults, scope)
		return b.save()
	}
	raw := JSON(doc)
	if len(raw) > protocol.MaxRecord {
		return Fail("resource_exhausted")
	}
	b.state.Vaults[scope] = raw
	return b.save()
}

// ScopeVault returns the newest vault published to a scope by any member, with the
// PeerID that published it, or a nil document when none has been.
func (b *Bridge) ScopeVault(scope string) (*vault.Document, string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed {
		return nil, "", Fail("unavailable")
	}
	prefix := scope + "/"
	var best *vault.Document
	var from string
	for k, r := range b.state.Records {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		c, err := r.Verify()
		if err != nil || c.Withdrawn {
			continue
		}
		raw, ok := c.Extensions["vault"]
		if !ok {
			continue
		}
		var doc vault.Document
		if json.Unmarshal(raw, &doc) != nil || doc.Schema != vault.Schema {
			continue
		}
		if best == nil || doc.Revision > best.Revision {
			d := doc
			best, from = &d, c.Peer
		}
	}
	return best, from, nil
}

// ScopeMembers lists the peers in a scope that advertise a sealing key, newest
// records only, sorted by PeerID. It is what the owner seals a vault to.
func (b *Bridge) ScopeMembers(scope string) ([]vault.Member, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed {
		return nil, Fail("unavailable")
	}
	prefix := scope + "/"
	var out []vault.Member
	for k, r := range b.state.Records {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		c, err := r.Verify()
		if err != nil || c.Withdrawn {
			continue
		}
		raw, ok := c.Extensions["x25519"]
		if !ok {
			continue
		}
		var enc string
		if json.Unmarshal(raw, &enc) != nil {
			continue
		}
		pub, err := base64.RawURLEncoding.Strict().DecodeString(enc)
		if err != nil || len(pub) != 32 {
			continue
		}
		out = append(out, vault.Member{PeerID: c.Peer, Public: pub})
	}
	slices.SortFunc(out, func(a, z vault.Member) int { return strings.Compare(a.PeerID, z.PeerID) })
	return out, nil
}

type Locator struct {
	Transport string `json:"transport"`
	Locator   string `json:"locator"`
}
type Contact struct {
	Schema     string                     `json:"schema"`
	Scope      string                     `json:"scope_id"`
	Peer       string                     `json:"peer_id"`
	Revision   string                     `json:"revision"`
	Name       string                     `json:"display_name"`
	Protocols  []string                   `json:"protocols"`
	Endpoints  []Locator                  `json:"endpoints"`
	Withdrawn  bool                       `json:"withdrawn"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}
type Record struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func (r Record) Verify() (Contact, error) {
	var c Contact
	data, err := base64.RawURLEncoding.Strict().DecodeString(r.Payload)
	if err != nil || len(data) > protocol.MaxRecord {
		return c, Fail("unauthorized")
	}
	canonical, err := protocol.Canonical(data)
	if err != nil || !bytes.Equal(data, canonical) {
		return c, Fail("unauthorized")
	}
	if err = protocol.Decode(data, &c); err != nil {
		return c, err
	}
	if c.Schema != "orb.peer-record/1" || !protocol.ValidID(c.Scope) || len(c.Name) > 256 || len(c.Endpoints) > 8 || !slices.Contains(c.Protocols, protocol.Version) {
		return c, Fail("unsupported_version")
	}
	if _, err = protocol.Counter(c.Revision); err != nil {
		return c, err
	}
	key, err := ParsePeerID(c.Peer)
	if err != nil {
		return c, err
	}
	sig, err := base64.RawURLEncoding.Strict().DecodeString(r.Signature)
	if err != nil || !ed25519.Verify(key, append([]byte("orb.peer-record/1\n"), data...), sig) {
		return c, Fail("unauthorized")
	}
	return c, nil
}
func (b *Bridge) SetScope(id string, peers []string) error {
	if !protocol.ValidID(id) || len(peers) > 128 {
		return Fail("resource_exhausted")
	}
	for _, p := range peers {
		if _, err := ParsePeerID(p); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Scopes) >= 128 && b.state.Scopes[id] == nil {
		return Fail("resource_exhausted")
	}
	b.state.Scopes[id] = append([]string{}, peers...)
	return b.save()
}
func (b *Bridge) PublishContact(scope, name string, endpoints []Locator, withdraw bool) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Scopes[scope] == nil {
		return Record{}, Fail("unauthorized")
	}
	k := scope + "/" + b.PeerID()
	rev := uint64(0)
	if old, ok := b.state.Records[k]; ok {
		c, err := old.Verify()
		if err != nil {
			return Record{}, err
		}
		rev, _ = protocol.Counter(c.Revision)
	}
	if rev == ^uint64(0) {
		return Record{}, Fail("resource_exhausted")
	}
	var ext map[string]json.RawMessage
	if xpub, err := b.X25519Public(); err == nil {
		if enc, err := json.Marshal(base64.RawURLEncoding.EncodeToString(xpub)); err == nil {
			ext = map[string]json.RawMessage{"x25519": enc}
		}
	}
	// A peer that has set a profile vault carries it in its own record, so it
	// gossips to the scope on the existing reconcile loop with no extra plumbing.
	if doc := b.state.Vaults[scope]; len(doc) > 0 && !withdraw {
		if ext == nil {
			ext = map[string]json.RawMessage{}
		}
		ext["vault"] = doc
	}
	c := Contact{Schema: "orb.peer-record/1", Scope: scope, Peer: b.PeerID(), Revision: strconv.FormatUint(rev+1, 10), Name: name, Protocols: []string{protocol.Version}, Endpoints: endpoints, Withdrawn: withdraw, Extensions: ext}
	data, err := protocol.Canonical(JSON(c))
	if err != nil || len(data) > protocol.MaxRecord {
		return Record{}, Fail("resource_exhausted")
	}
	r := Record{Payload: base64.RawURLEncoding.EncodeToString(data), Signature: base64.RawURLEncoding.EncodeToString(ed25519.Sign(b.state.Key, append([]byte("orb.peer-record/1\n"), data...)))}
	if _, err = r.Verify(); err != nil {
		return Record{}, err
	}
	b.state.Records[k] = r
	if err = b.save(); err != nil {
		return Record{}, err
	}
	return r, nil
}
func (b *Bridge) MergeContact(sender string, r Record) (bool, error) {
	c, err := r.Verify()
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Blocked[sender] || !slices.Contains(b.state.Scopes[c.Scope], sender) {
		return false, Fail("unauthorized")
	}
	k := c.Scope + "/" + c.Peer
	if old, ok := b.state.Records[k]; ok {
		before, err := old.Verify()
		if err != nil {
			return false, err
		}
		a, _ := protocol.Counter(before.Revision)
		z, _ := protocol.Counter(c.Revision)
		if z < a {
			return false, nil
		}
		if z == a {
			if old.Payload != r.Payload {
				return false, Fail("identity_conflict")
			}
			return false, nil
		}
	} else if len(b.state.Records) >= 1024 {
		return false, Fail("resource_exhausted")
	}
	b.state.Records[k] = r
	if err = b.save(); err != nil {
		return false, err
	}
	return true, nil
}
func (b *Bridge) Contacts(peer, scope string) ([]Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed || b.state.Blocked[peer] || !slices.Contains(b.state.Scopes[scope], peer) {
		return nil, Fail("unauthorized")
	}
	// Records are verified when loaded or stored, under their scope/peer key.
	keys := []string{}
	for k := range b.state.Records {
		if strings.HasPrefix(k, scope+"/") {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	out := make([]Record, 0, len(keys))
	for _, k := range keys {
		out = append(out, b.state.Records[k])
	}
	return out, nil
}
