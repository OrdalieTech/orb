package vault

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
)

// Schema is the vault document's schema tag.
const Schema = "orb.mitosis-vault/1"

// MaxMembers bounds how many peers one vault document seals to, keeping it inside
// the peer-record frame it rides in.
const MaxMembers = 64

// Bundle is what a Mitosis profile shares between its peers. Both fields are opaque
// to this package: the assembly fills Credentials from the auth store and Config
// from settings/models overlays, and reads them back the same way.
type Bundle struct {
	Credentials json.RawMessage `json:"credentials,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
}

// Document is the owner-published vault: the bundle sealed once under a per-epoch
// group key, and that group key sealed to each member peer's X25519 key. It carries
// no cleartext secret. Revision is monotonic so peers merge the newest; Epoch names
// the group-key generation and bumps on revocation.
type Document struct {
	Schema    string            `json:"schema"`
	Epoch     uint64            `json:"epoch"`
	Revision  uint64            `json:"revision"`
	Bundle    string            `json:"bundle"`
	Envelopes map[string]string `json:"envelopes"`
}

// Member is one peer a vault seals to: its PeerID and its X25519 public key bytes.
type Member struct {
	PeerID string
	Public []byte
}

// Build seals bundle under a fresh group key and seals that key to every member.
// epoch and revision are the caller's monotonic counters (bump both when rebuilding
// after a membership change; bump revision alone when only the bundle changed, which
// still rotates the key here — simplicity over key reuse).
func Build(members []Member, epoch, revision uint64, bundle Bundle) (Document, error) {
	if len(members) == 0 {
		return Document{}, errors.New("vault: a document needs at least one member")
	}
	if len(members) > MaxMembers {
		return Document{}, errors.New("vault: too many members for one document")
	}
	plain, err := json.Marshal(bundle)
	if err != nil {
		return Document{}, err
	}
	groupKey, err := NewGroupKey()
	if err != nil {
		return Document{}, err
	}
	sealedBundle, err := SealGroup(groupKey, plain)
	if err != nil {
		return Document{}, err
	}
	doc := Document{
		Schema:    Schema,
		Epoch:     epoch,
		Revision:  revision,
		Bundle:    base64.RawURLEncoding.EncodeToString(sealedBundle),
		Envelopes: make(map[string]string, len(members)),
	}
	for _, m := range members {
		pub, err := ecdh.X25519().NewPublicKey(m.Public)
		if err != nil {
			return Document{}, errors.New("vault: member " + m.PeerID + " has an invalid X25519 key")
		}
		if m.PeerID == "" {
			return Document{}, errors.New("vault: member with empty PeerID")
		}
		if _, dup := doc.Envelopes[m.PeerID]; dup {
			return Document{}, errors.New("vault: duplicate member " + m.PeerID)
		}
		sealed, err := Seal(pub, groupKey)
		if err != nil {
			return Document{}, err
		}
		doc.Envelopes[m.PeerID] = base64.RawURLEncoding.EncodeToString(sealed)
	}
	return doc, nil
}

// OpenBundle recovers the bundle for peerID using its X25519 private key. It fails
// cleanly when the peer has no envelope (never admitted, or revoked).
func (d Document) OpenBundle(peerID string, priv *ecdh.PrivateKey) (Bundle, error) {
	if d.Schema != Schema {
		return Bundle{}, errors.New("vault: unknown document schema")
	}
	envB64, ok := d.Envelopes[peerID]
	if !ok {
		return Bundle{}, errors.New("vault: no envelope for this peer")
	}
	envelope, err := base64.RawURLEncoding.DecodeString(envB64)
	if err != nil {
		return Bundle{}, errors.New("vault: malformed envelope")
	}
	groupKey, err := Open(priv, envelope)
	if err != nil {
		return Bundle{}, err
	}
	sealedBundle, err := base64.RawURLEncoding.DecodeString(d.Bundle)
	if err != nil {
		return Bundle{}, errors.New("vault: malformed bundle")
	}
	plain, err := OpenGroup(groupKey, sealedBundle)
	if err != nil {
		return Bundle{}, err
	}
	var bundle Bundle
	if err := json.Unmarshal(plain, &bundle); err != nil {
		return Bundle{}, errors.New("vault: malformed bundle contents")
	}
	return bundle, nil
}

// Members returns the PeerIDs the document seals to, sorted, for diagnostics.
func (d Document) Members() []string {
	ids := make([]string, 0, len(d.Envelopes))
	for id := range d.Envelopes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
