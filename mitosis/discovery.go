package mitosis

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/OrdalieTech/orb/mitosis/protocol"
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
func (b *Mitosis) SetScope(id string, peers []string) error {
	if !protocol.ValidID(id) || len(peers) > maxPeers {
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
func (b *Mitosis) PublishContact(scope, name string, endpoints []Locator, withdraw bool) (Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.publishContact(scope, name, endpoints, withdraw, nil)
}
func (b *Mitosis) publishContact(scope, name string, endpoints []Locator, withdraw bool, extensions map[string]json.RawMessage) (Record, error) {
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
	c := Contact{Schema: "orb.peer-record/1", Scope: scope, Peer: b.PeerID(), Revision: strconv.FormatUint(rev+1, 10), Name: name, Protocols: []string{protocol.Version}, Endpoints: endpoints, Withdrawn: withdraw, Extensions: extensions}
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
func (b *Mitosis) MergeContact(sender string, r Record) (bool, error) {
	c, err := r.Verify()
	if err != nil {
		return false, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	members := b.state.Scopes[c.Scope]
	// A member takes its sync group owner's record from anyone, so it catches up while the owner is away.
	relayed := c.Scope == b.state.Group && len(members) > 0 && c.Peer == members[0]
	if b.state.Blocked[sender] || b.state.Blocked[c.Peer] || !slices.Contains(members, sender) && !relayed {
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
func (b *Mitosis) Contacts(peer, scope string) ([]Record, error) {
	records, err := b.contacts(peer, scope)
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(records))
	for _, k := range slices.Sorted(maps.Keys(records)) {
		out = append(out, records[k])
	}
	return out, nil
}

// contacts is what peer may read of scope, by record peer.
func (b *Mitosis) contacts(peer, scope string) (map[string]Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed || b.failed || b.state.Blocked[peer] || !slices.Contains(b.state.Scopes[scope], peer) {
		return nil, Fail("unauthorized")
	}
	out := map[string]Record{}
	for k, r := range b.state.Records {
		if id, ok := strings.CutPrefix(k, scope+"/"); ok {
			out[id] = r
		}
	}
	return out, nil
}

// revision reads a stored record's revision; records are verified when loaded or stored.
func revision(r Record) uint64 {
	data, _ := base64.RawURLEncoding.DecodeString(r.Payload)
	var c struct {
		Revision string `json:"revision"`
	}
	_ = json.Unmarshal(data, &c)
	n, _ := protocol.Counter(c.Revision)
	return n
}

// Versions is the revision of each record peer may read of scope, by record peer: what this
// Mitosis sends in place of the records to learn what a member holds that it lacks.
func (b *Mitosis) Versions(peer, scope string) (map[string]string, error) {
	records, err := b.contacts(peer, scope)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(records))
	for id, r := range records {
		out[id] = strconv.FormatUint(revision(r), 10)
	}
	return out, nil
}

// Newer splits scope against what peer holds (have, from Versions): the records peer lacks or
// holds older, and the record peers whose records peer holds newer or this Mitosis lacks.
func (b *Mitosis) Newer(peer, scope string, have map[string]string) ([]Record, []string, error) {
	if len(have) > 1024 {
		return nil, nil, Fail("resource_exhausted")
	}
	theirs := make(map[string]uint64, len(have))
	for id, rev := range have {
		n, err := protocol.Counter(rev)
		if err != nil {
			return nil, nil, err
		}
		if _, err = ParsePeerID(id); err != nil {
			return nil, nil, err
		}
		theirs[id] = n
	}
	records, err := b.contacts(peer, scope)
	if err != nil {
		return nil, nil, err
	}
	newer, behind := []Record{}, []string{}
	for _, id := range slices.Sorted(maps.Keys(records)) {
		if n, ok := theirs[id]; !ok || n < revision(records[id]) {
			newer = append(newer, records[id])
		}
	}
	for _, id := range slices.Sorted(maps.Keys(theirs)) {
		if r, ok := records[id]; !ok || revision(r) < theirs[id] {
			behind = append(behind, id)
		}
	}
	return newer, behind, nil
}
