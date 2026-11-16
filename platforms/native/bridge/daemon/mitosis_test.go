package daemon

import (
	"encoding/json"
	"testing"

	"github.com/OrdalieTech/orb/bridge"
	"github.com/OrdalieTech/orb/bridge/protocol"
	"github.com/OrdalieTech/orb/internal/document"
)

func TestMitosisScopeIsDeterministicAndValid(t *testing.T) {
	a := MitosisScope("personal")
	b := MitosisScope("personal")
	if a != b {
		t.Fatal("scope id is not deterministic for a profile")
	}
	if !protocol.ValidID(a) {
		t.Fatalf("derived scope id %q is not a valid id", a)
	}
	if MitosisScope("work") == a {
		t.Fatal("different profiles share a scope id")
	}
}

// Exercises the real daemon method: status -> SetScope -> PublishContact ->
// vault.Publish -> OpenVault, for the single-device self round-trip (what a live
// `orb mitosis sync` runs before any peer has joined).
func TestServiceMitosisSyncSelfRoundTrip(t *testing.T) {
	ctx := t.Context()
	b, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Peers: bridge.NewPeers(ctx, b), ctx: ctx}
	scope := MitosisScope("personal")

	creds := []byte(`{"anthropic":{"key":"fake-ant"},"openai":{"key":"fake-openai"}}`)
	bundle, found, err := s.mitosisSync(ctx, scope, creds)
	if err != nil {
		t.Fatalf("mitosisSync: %v", err)
	}
	if !found {
		t.Fatal("the device should receive the vault it just sealed to itself")
	}
	if string(bundle.Credentials) != string(creds) {
		t.Fatalf("self round-trip mismatch:\n got %s\nwant %s", bundle.Credentials, creds)
	}

	// The device is now a member of the deterministic scope.
	raw, err := b.Admin(ctx, "scopes", bridge.JSON(struct{}{}))
	if err != nil {
		t.Fatal(err)
	}
	if want := b.PeerID(); !containsPeer(t, raw, scope, want) {
		t.Fatalf("scope %s does not contain this device", scope)
	}
}

func TestServiceMitosisSyncEmptyCredentialsNoVault(t *testing.T) {
	ctx := t.Context()
	b, err := bridge.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{Peers: bridge.NewPeers(ctx, b), ctx: ctx}
	// Empty credentials: the device joins the scope but seals nothing, so there is
	// no vault to open yet.
	_, found, err := s.mitosisSync(ctx, MitosisScope("personal"), []byte("{}"))
	if err != nil {
		t.Fatalf("mitosisSync: %v", err)
	}
	if found {
		t.Fatal("no vault should exist when nothing was shared")
	}
}

func containsPeer(t *testing.T, scopesJSON []byte, scope, peer string) bool {
	t.Helper()
	var status struct {
		Scopes map[string][]string `json:"scopes"`
	}
	if err := json.Unmarshal(scopesJSON, &status); err != nil {
		t.Fatal(err)
	}
	for _, p := range status.Scopes[scope] {
		if p == peer {
			return true
		}
	}
	return false
}
