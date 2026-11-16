package vault

import (
	"bytes"
	"crypto/ecdh"
	"encoding/json"
	"slices"
	"testing"
)

func member(t *testing.T, id string, seed []byte) (Member, *ecdh.PrivateKey) {
	t.Helper()
	k, err := X25519FromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return Member{PeerID: id, Public: k.PublicKey().Bytes()}, k
}

func testBundle() Bundle {
	return Bundle{
		Credentials: json.RawMessage(`{"anthropic":{"type":"api","key":"sk-ant-x"}}`),
		Config:      json.RawMessage(`{"defaultModel":"claude-sonnet-5"}`),
	}
}

func TestVaultBuildAndOpen(t *testing.T) {
	mA, keyA := member(t, "peer-A", seedA)
	mB, keyB := member(t, "peer-B", seedB)
	want := testBundle()

	doc, err := Build([]Member{mA, mB}, 1, 1, want)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != Schema || doc.Epoch != 1 || doc.Revision != 1 {
		t.Fatalf("bad header: %+v", doc)
	}
	if !slices.Equal(doc.Members(), []string{"peer-A", "peer-B"}) {
		t.Fatalf("members = %v", doc.Members())
	}
	for id, key := range map[string]*ecdh.PrivateKey{"peer-A": keyA, "peer-B": keyB} {
		got, err := doc.OpenBundle(id, key)
		if err != nil {
			t.Fatalf("%s open: %v", id, err)
		}
		if !bytes.Equal(got.Credentials, want.Credentials) || !bytes.Equal(got.Config, want.Config) {
			t.Fatalf("%s bundle mismatch: %+v", id, got)
		}
	}
}

func TestVaultNonMemberCannotOpen(t *testing.T) {
	mA, _ := member(t, "peer-A", seedA)
	doc, err := Build([]Member{mA}, 1, 1, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	// A stranger key, and a peer id with no envelope.
	stranger, _ := X25519FromSeed(seedB)
	if _, err := doc.OpenBundle("peer-A", stranger); err == nil {
		t.Fatal("stranger key opened peer-A's envelope")
	}
	if _, err := doc.OpenBundle("peer-ghost", stranger); err == nil {
		t.Fatal("absent peer opened a bundle")
	}
}

func TestVaultRevocationReseal(t *testing.T) {
	mA, keyA := member(t, "peer-A", seedA)
	mB, keyB := member(t, "peer-B", seedB)

	v1, err := Build([]Member{mA, mB}, 1, 1, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	// Both can open epoch 1.
	if _, err := v1.OpenBundle("peer-B", keyB); err != nil {
		t.Fatalf("B should open epoch 1: %v", err)
	}

	// Revoke B: rebuild for A only at a new epoch.
	v2, err := Build([]Member{mA}, 2, 2, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v2.OpenBundle("peer-A", keyA); err != nil {
		t.Fatalf("A should open epoch 2: %v", err)
	}
	if _, ok := v2.Envelopes["peer-B"]; ok {
		t.Fatal("revoked peer still has an envelope")
	}
	if _, err := v2.OpenBundle("peer-B", keyB); err == nil {
		t.Fatal("revoked peer opened the new epoch")
	}
}

func TestVaultBuildValidation(t *testing.T) {
	mA, _ := member(t, "peer-A", seedA)
	if _, err := Build(nil, 1, 1, testBundle()); err == nil {
		t.Fatal("empty membership accepted")
	}
	// Duplicate member.
	if _, err := Build([]Member{mA, mA}, 1, 1, testBundle()); err == nil {
		t.Fatal("duplicate member accepted")
	}
	// Bad X25519 key length.
	if _, err := Build([]Member{{PeerID: "x", Public: []byte{1, 2, 3}}}, 1, 1, testBundle()); err == nil {
		t.Fatal("invalid X25519 key accepted")
	}
	// Too many members.
	many := make([]Member, MaxMembers+1)
	for i := range many {
		m, _ := member(t, string(rune('a'+i%26))+string(rune('0'+i/26)), append([]byte{byte(i)}, bytes.Repeat([]byte{0x09}, 31)...))
		many[i] = m
	}
	if _, err := Build(many, 1, 1, testBundle()); err == nil {
		t.Fatal("oversized membership accepted")
	}
}

func TestVaultTamperedEnvelopeOrBundle(t *testing.T) {
	mA, keyA := member(t, "peer-A", seedA)
	doc, err := Build([]Member{mA}, 1, 1, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt the sealed bundle.
	bad := doc
	bad.Bundle = doc.Bundle[:len(doc.Bundle)-2] + "AA"
	if _, err := bad.OpenBundle("peer-A", keyA); err == nil {
		t.Fatal("tampered bundle accepted")
	}
	// Wrong schema.
	bad = doc
	bad.Schema = "orb.peer-record/1"
	if _, err := bad.OpenBundle("peer-A", keyA); err == nil {
		t.Fatal("wrong schema accepted")
	}
}

// The document survives a JSON round-trip (it is gossiped as JSON inside a record).
func TestVaultJSONRoundTrip(t *testing.T) {
	mA, keyA := member(t, "peer-A", seedA)
	doc, err := Build([]Member{mA}, 3, 7, testBundle())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var back Document
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	got, err := back.OpenBundle("peer-A", keyA)
	if err != nil {
		t.Fatalf("open after round-trip: %v", err)
	}
	if !bytes.Equal(got.Credentials, testBundle().Credentials) {
		t.Fatal("bundle changed across JSON round-trip")
	}
}
