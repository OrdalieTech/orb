package vault

import "crypto/ecdh"

// Store is the slice of a Bridge the sync orchestration needs. *bridge.Bridge
// satisfies it structurally, so this package stays free of an import cycle.
type Store interface {
	ScopeMembers(scope string) ([]Member, error)
	SetVault(scope string, doc *Document) error
	ScopeVault(scope string) (*Document, string, error)
}

// Publish seals bundle to every current member of scope and records it for
// gossip. The caller supplies monotonic epoch/revision counters (bump both after
// a membership change so revocation takes a fresh key; bump revision when only the
// bundle changed). The caller republishes its contact afterwards to carry it.
func Publish(s Store, scope string, bundle Bundle, epoch, revision uint64) error {
	members, err := s.ScopeMembers(scope)
	if err != nil {
		return err
	}
	doc, err := Build(members, epoch, revision, bundle)
	if err != nil {
		return err
	}
	return s.SetVault(scope, &doc)
}

// Apply reads the scope's current vault and opens it with this peer's key. ok is
// false, with no error, when there is no vault or none sealed to this peer (never
// admitted, or revoked) — the caller then leaves local credentials untouched.
func Apply(s Store, scope, selfPeerID string, selfPriv *ecdh.PrivateKey) (bundle Bundle, ok bool, err error) {
	doc, _, err := s.ScopeVault(scope)
	if err != nil {
		return Bundle{}, false, err
	}
	if doc == nil {
		return Bundle{}, false, nil
	}
	bundle, err = doc.OpenBundle(selfPeerID, selfPriv)
	if err != nil {
		return Bundle{}, false, nil
	}
	return bundle, true, nil
}
