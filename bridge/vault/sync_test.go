package vault

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"testing"
)

type fakeStore struct {
	members  []Member
	vault    *Document
	setErr   error
	memErr   error
	setCalls int
}

func (f *fakeStore) ScopeMembers(string) ([]Member, error) {
	return f.members, f.memErr
}

func (f *fakeStore) SetVault(_ string, d *Document) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.vault = d
	f.setCalls++
	return nil
}

func (f *fakeStore) ScopeVault(string) (*Document, string, error) {
	return f.vault, "owner", nil
}

func newMember(t *testing.T, id string, seed []byte) (Member, *ecdh.PrivateKey) {
	t.Helper()
	k, err := X25519FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return Member{PeerID: id, Public: k.PublicKey().Bytes()}, k
}

func TestPublishThenApply(t *testing.T) {
	mA, keyA := newMember(t, "A", seedA)
	mB, keyB := newMember(t, "B", seedB)
	store := &fakeStore{members: []Member{mA, mB}}

	want := Bundle{Credentials: json.RawMessage(`{"anthropic":"sk"}`)}
	if err := Publish(store, "scope", want, 1, 1); err != nil {
		t.Fatal(err)
	}
	if store.vault == nil || store.setCalls != 1 {
		t.Fatal("publish did not record a vault")
	}

	for id, key := range map[string]*ecdh.PrivateKey{"A": keyA, "B": keyB} {
		got, ok, err := Apply(store, "scope", id, key)
		if err != nil || !ok {
			t.Fatalf("%s apply ok=%v err=%v", id, ok, err)
		}
		if string(got.Credentials) != `{"anthropic":"sk"}` {
			t.Fatalf("%s got %s", id, got.Credentials)
		}
	}
}

func TestApplyNoVaultIsNoop(t *testing.T) {
	_, keyA := newMember(t, "A", seedA)
	store := &fakeStore{} // no vault set
	if _, ok, err := Apply(store, "scope", "A", keyA); ok || err != nil {
		t.Fatalf("expected no-op, got ok=%v err=%v", ok, err)
	}
}

func TestApplyNotAMemberIsNoop(t *testing.T) {
	mA, _ := newMember(t, "A", seedA)
	store := &fakeStore{members: []Member{mA}}
	if err := Publish(store, "scope", Bundle{Config: json.RawMessage(`{}`)}, 1, 1); err != nil {
		t.Fatal(err)
	}
	// A different peer with no envelope gets a clean no-op, not an error.
	_, strangerKey := newMember(t, "Z", seedB)
	if _, ok, err := Apply(store, "scope", "Z", strangerKey); ok || err != nil {
		t.Fatalf("stranger should be a no-op, got ok=%v err=%v", ok, err)
	}
}

func TestPublishPropagatesErrors(t *testing.T) {
	mA, _ := newMember(t, "A", seedA)
	if err := Publish(&fakeStore{memErr: errors.New("boom")}, "s", Bundle{}, 1, 1); err == nil {
		t.Fatal("ScopeMembers error not propagated")
	}
	if err := Publish(&fakeStore{members: []Member{mA}, setErr: errors.New("x")}, "s", Bundle{}, 1, 1); err == nil {
		t.Fatal("SetVault error not propagated")
	}
	// No members → Build fails.
	if err := Publish(&fakeStore{members: nil}, "s", Bundle{}, 1, 1); err == nil {
		t.Fatal("empty membership should fail the build")
	}
}
