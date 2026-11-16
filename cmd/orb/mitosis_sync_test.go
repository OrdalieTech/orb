package main

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/bridge/vault"
	"github.com/OrdalieTech/orb/internal/document"
)

// TestMitosisSyncEndToEnd drives the whole pipeline the daemon and CLI perform, in
// one process: two peers in a shared scope, the owner sealing its credentials to the
// scope, and the peer opening them and merging them into its own store without losing
// its local login. This is the credential-safety contract, end to end.
func TestMitosisSyncEndToEnd(t *testing.T) {
	ctx := context.Background()
	owner, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	peer, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	scope := protocol.NewID()
	for _, b := range []*bridge.Bridge{owner, peer} {
		if err := b.SetScope(scope, []string{owner.PeerID(), peer.PeerID()}); err != nil {
			t.Fatal(err)
		}
	}
	// Both advertise their sealing keys; the owner learns the peer's.
	for _, b := range []*bridge.Bridge{owner, peer} {
		rec, err := b.PublishContact(scope, "n", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := owner.MergeContact(b.PeerID(), rec); err != nil {
			t.Fatal(err)
		}
	}

	// Owner side: read its credentials and seal them to the scope.
	ownerCreds := auth.NewMemoryStore(map[string]*auth.Credential{
		"anthropic": auth.APIKeyCredential("owner-ant"),
		"openai":    auth.APIKeyCredential("owner-openai"),
	})
	shared, err := ownerCreds.Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.Publish(owner, scope, vault.Bundle{Credentials: shared}, 1, 1); err != nil {
		t.Fatal(err)
	}
	ownerRec, err := owner.PublishContact(scope, "owner", nil, false) // now carries the vault
	if err != nil {
		t.Fatal(err)
	}
	if _, err := peer.MergeContact(owner.PeerID(), ownerRec); err != nil {
		t.Fatal(err)
	}

	// Peer side: open the vault with its own identity and merge into its store,
	// which already holds a local-only login.
	peerCreds := auth.NewMemoryStore(map[string]*auth.Credential{
		"gemini": auth.APIKeyCredential("peer-local-gemini"),
	})
	bundle, ok, err := peer.OpenVault(scope)
	if err != nil || !ok {
		t.Fatalf("peer could not open the vault: ok=%v err=%v", ok, err)
	}
	if err := peerCreds.MergeIn(ctx, bundle.Credentials); err != nil {
		t.Fatal(err)
	}

	// The peer now has the owner's credentials AND kept its own.
	for provider, want := range map[string]string{
		"anthropic": "owner-ant",
		"openai":    "owner-openai",
		"gemini":    "peer-local-gemini",
	} {
		got, err := peerCreds.Read(ctx, provider)
		if err != nil || got == nil || got.Key == nil || *got.Key != want {
			t.Fatalf("peer %s = %+v, want %q", provider, got, want)
		}
	}
}

// A peer that is not in the scope (never admitted) gets nothing and keeps its store.
func TestMitosisSyncOutsiderUnaffected(t *testing.T) {
	ctx := context.Background()
	owner, _ := bridge.Open(&document.Memory{}, true)
	outsider, _ := bridge.Open(&document.Memory{}, true)
	scope := protocol.NewID()
	// Only the owner is in the scope.
	if err := owner.SetScope(scope, []string{owner.PeerID()}); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.PublishContact(scope, "owner", nil, false); err != nil {
		t.Fatal(err)
	}
	members, _ := owner.ScopeMembers(scope)
	doc, err := vault.Build(members, 1, 1, vault.Bundle{Credentials: []byte(`{"anthropic":{"key":"secret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.SetVault(scope, &doc); err != nil {
		t.Fatal(err)
	}
	// The outsider has the same scope id but no envelope and no shared record.
	if err := outsider.SetScope(scope, []string{outsider.PeerID()}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := outsider.OpenVault(scope); ok || err != nil {
		t.Fatalf("outsider should get nothing: ok=%v err=%v", ok, err)
	}
	_ = ctx
}
