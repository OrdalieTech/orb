package bridge

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/bridge/vault"
)

func TestScopeMembersAdvertiseSealingKeys(t *testing.T) {
	a, b, c := newBridge(t), newBridge(t), newBridge(t)
	all := []*Bridge{a, b, c}
	scope := protocol.NewID()
	for _, x := range all {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID(), c.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	// Each peer publishes its contact; a collects them all.
	for _, x := range all {
		rec, err := x.PublishContact(scope, "n", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.MergeContact(x.PeerID(), rec); err != nil {
			t.Fatal(err)
		}
	}

	members, err := a.ScopeMembers(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 3 {
		t.Fatalf("members = %d, want 3", len(members))
	}
	// The advertised key must match each peer's own X25519Public.
	for _, m := range members {
		var want []byte
		for _, x := range all {
			if x.PeerID() == m.PeerID {
				want, _ = x.X25519Public()
			}
		}
		if want == nil || !bytes.Equal(m.Public, want) {
			t.Fatalf("advertised key for %s does not match its identity", m.PeerID)
		}
	}

	// Owner seals a vault to the enumerated members; every peer opens it with the
	// X25519 key derived from its own identity seed.
	doc, err := vault.Build(members, 1, 1, vault.Bundle{Credentials: json.RawMessage(`{"anthropic":"sk-ant-z"}`)})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range all {
		priv, err := vault.X25519FromSeed(x.state.Key[:ed25519.SeedSize])
		if err != nil {
			t.Fatal(err)
		}
		got, err := doc.OpenBundle(x.PeerID(), priv)
		if err != nil {
			t.Fatalf("%s could not open the vault: %v", x.PeerID(), err)
		}
		if string(got.Credentials) != `{"anthropic":"sk-ant-z"}` {
			t.Fatalf("%s got wrong bundle: %s", x.PeerID(), got.Credentials)
		}
	}
}

func TestScopeMembersExcludesWithdrawn(t *testing.T) {
	a, b := newBridge(t), newBridge(t)
	scope := protocol.NewID()
	for _, x := range []*Bridge{a, b} {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := b.PublishContact(scope, "B", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.MergeContact(b.PeerID(), rec); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.ScopeMembers(scope); len(got) != 1 {
		t.Fatalf("before withdrawal: %d members, want 1", len(got))
	}
	withdrawn, err := b.PublishContact(scope, "B", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.MergeContact(b.PeerID(), withdrawn); err != nil {
		t.Fatal(err)
	}
	if got, _ := a.ScopeMembers(scope); len(got) != 0 {
		t.Fatalf("after withdrawal: %d members, want 0", len(got))
	}
}
