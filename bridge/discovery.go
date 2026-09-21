package bridge

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"

	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
)

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
		return c, connect.Fail("unauthorized")
	}
	canonical, err := protocol.Canonical(data)
	if err != nil || !bytes.Equal(data, canonical) {
		return c, connect.Fail("unauthorized")
	}
	if err = protocol.Decode(data, &c); err != nil {
		return c, err
	}
	if c.Schema != "orb.peer-record/1" || !protocol.ValidID(c.Scope) || len(c.Name) > 256 || len(c.Endpoints) > 8 || !slices.Contains(c.Protocols, protocol.Version) {
		return c, connect.Fail("unsupported_version")
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
		return c, connect.Fail("unauthorized")
	}
	return c, nil
}
func (b *Bridge) SetScope(id string, peers []string) error {
	if !protocol.ValidID(id) || len(peers) > 128 {
		return connect.Fail("resource_exhausted")
	}
	for _, p := range peers {
		if _, err := ParsePeerID(p); err != nil {
			return err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.state.Scopes) >= 128 && b.state.Scopes[id] == nil {
		return connect.Fail("resource_exhausted")
	}
	b.state.Scopes[id] = append([]string{}, peers...)
	return b.save()
}
func (b *Bridge) PublishContact(scope, name string, endpoints []Locator, withdraw bool) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Scopes[scope] == nil {
		return Record{}, connect.Fail("unauthorized")
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
		return Record{}, connect.Fail("resource_exhausted")
	}
	c := Contact{Schema: "orb.peer-record/1", Scope: scope, Peer: b.PeerID(), Revision: strconv.FormatUint(rev+1, 10), Name: name, Protocols: []string{protocol.Version}, Endpoints: endpoints, Withdrawn: withdraw}
	data, err := protocol.Canonical(connect.JSON(c))
	if err != nil || len(data) > protocol.MaxRecord {
		return Record{}, connect.Fail("resource_exhausted")
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
		return false, connect.Fail("unauthorized")
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
				return false, connect.Fail("identity_conflict")
			}
			return false, nil
		}
	} else if len(b.state.Records) >= 1024 {
		return false, connect.Fail("resource_exhausted")
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
		return nil, connect.Fail("unauthorized")
	}
	keys := []string{}
	for k, r := range b.state.Records {
		c, err := r.Verify()
		if err != nil {
			return nil, err
		}
		if c.Scope == scope {
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
