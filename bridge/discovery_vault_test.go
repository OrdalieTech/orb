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

func TestScopeVaultGossipsAndOpens(t *testing.T) {
	a, b, c := newBridge(t), newBridge(t), newBridge(t)
	all := []*Bridge{a, b, c}
	scope := protocol.NewID()
	for _, x := range all {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID(), c.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
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
	doc, err := vault.Build(members, 1, 1, vault.Bundle{Credentials: json.RawMessage(`{"openai":"sk-o"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVault(scope, &doc); err != nil {
		t.Fatal(err)
	}
	// a republishes its contact, now carrying the vault, and b receives it.
	rec, err := a.PublishContact(scope, "A", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.MergeContact(a.PeerID(), rec); err != nil {
		t.Fatal(err)
	}
	got, from, err := b.ScopeVault(scope)
	if err != nil || got == nil {
		t.Fatalf("b found no vault: %v", err)
	}
	if from != a.PeerID() {
		t.Fatalf("vault publisher = %s, want %s", from, a.PeerID())
	}
	priv, err := vault.X25519FromSeed(b.state.Key[:ed25519.SeedSize])
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := got.OpenBundle(b.PeerID(), priv)
	if err != nil {
		t.Fatalf("b could not open the gossiped vault: %v", err)
	}
	if string(bundle.Credentials) != `{"openai":"sk-o"}` {
		t.Fatalf("wrong bundle: %s", bundle.Credentials)
	}
	// Clearing removes it from future publications.
	if err := a.SetVault(scope, nil); err != nil {
		t.Fatal(err)
	}
}

func TestOpenVaultDecryptsWithIdentityKey(t *testing.T) {
	a, b := newBridge(t), newBridge(t)
	scope := protocol.NewID()
	for _, x := range []*Bridge{a, b} {
		if err := x.SetScope(scope, []string{a.PeerID(), b.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	for _, x := range []*Bridge{a, b} {
		rec, err := x.PublishContact(scope, "n", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.MergeContact(x.PeerID(), rec); err != nil {
			t.Fatal(err)
		}
	}
	members, _ := a.ScopeMembers(scope)
	doc, err := vault.Build(members, 1, 1, vault.Bundle{Credentials: json.RawMessage(`{"p":"k"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVault(scope, &doc); err != nil {
		t.Fatal(err)
	}
	rec, err := a.PublishContact(scope, "A", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.MergeContact(a.PeerID(), rec); err != nil {
		t.Fatal(err)
	}
	// b opens with its own identity, no key material passed in.
	bundle, ok, err := b.OpenVault(scope)
	if err != nil || !ok {
		t.Fatalf("b OpenVault ok=%v err=%v", ok, err)
	}
	if string(bundle.Credentials) != `{"p":"k"}` {
		t.Fatalf("wrong bundle: %s", bundle.Credentials)
	}
	// A bridge with no vault in scope is a clean no-op.
	c := newBridge(t)
	if err := c.SetScope(scope, []string{c.PeerID()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.OpenVault(scope); ok || err != nil {
		t.Fatalf("empty scope should be a no-op, got ok=%v err=%v", ok, err)
	}
}

func TestVaultPersistsAcrossRestart(t *testing.T) {
	a := newBridge(t)
	scope := protocol.NewID()
	if err := a.SetScope(scope, []string{a.PeerID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PublishContact(scope, "A", nil, false); err != nil {
		t.Fatal(err)
	}
	members, err := a.ScopeMembers(scope)
	if err != nil || len(members) != 1 {
		t.Fatalf("members=%v err=%v", members, err)
	}
	doc, err := vault.Build(members, 1, 1, vault.Bundle{Config: json.RawMessage(`{"defaultModel":"x"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SetVault(scope, &doc); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PublishContact(scope, "A", nil, false); err != nil {
		t.Fatal(err)
	}
	// Reopen from the same store: the vault (state) and the record both survive.
	restarted, err := Open(a.store, false)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := restarted.ScopeVault(scope)
	if err != nil || got == nil {
		t.Fatalf("vault lost across restart: %v", err)
	}
	priv, _ := vault.X25519FromSeed(restarted.state.Key[:ed25519.SeedSize])
	bundle, err := got.OpenBundle(restarted.PeerID(), priv)
	if err != nil || string(bundle.Config) != `{"defaultModel":"x"}` {
		t.Fatalf("restarted peer could not open its own vault: %v", err)
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
