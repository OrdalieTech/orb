package vault

import (
	"bytes"
	"crypto/ecdh"
	"encoding/hex"
	"testing"
)

// fixed test material. seedA/seedB stand in for two peers' Ed25519 identity seeds.
var (
	seedA     = bytes.Repeat([]byte{0x01}, 32)
	seedB     = bytes.Repeat([]byte{0x02}, 32)
	ephScalar = bytes.Repeat([]byte{0x07}, 32)
	fixNonce  = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	groupKey  = bytes.Repeat([]byte{0xAA}, GroupKeySize)
	bundle    = []byte(`{"anthropic":"sk-ant-secret","openai":"sk-openai-secret"}`)
)

func fixedNonceFn(n []byte) func() ([]byte, error) {
	return func() ([]byte, error) { return n, nil }
}

func keyA(t *testing.T) *ecdh.PrivateKey {
	t.Helper()
	k, err := X25519FromSeed(seedA)
	if err != nil {
		t.Fatalf("X25519FromSeed(A): %v", err)
	}
	return k
}

func TestX25519FromSeedDeterministicAndSeparated(t *testing.T) {
	a1, err := X25519FromSeed(seedA)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := X25519FromSeed(seedA)
	if !bytes.Equal(a1.Bytes(), a2.Bytes()) {
		t.Fatal("same seed produced different X25519 keys")
	}
	b, _ := X25519FromSeed(seedB)
	if bytes.Equal(a1.Bytes(), b.Bytes()) {
		t.Fatal("different seeds produced the same X25519 key")
	}
	// Domain separation: the X25519 scalar must not be the raw seed.
	if bytes.Equal(a1.Bytes(), seedA) {
		t.Fatal("X25519 private scalar equals the raw Ed25519 seed")
	}
	if _, err := X25519FromSeed(seedA[:31]); err == nil {
		t.Fatal("short seed accepted")
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	recipient := keyA(t)
	sealed, err := Seal(recipient.PublicKey(), groupKey)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != x25519KeySize+nonceSize+len(groupKey)+16 {
		t.Fatalf("unexpected sealed length %d", len(sealed))
	}
	got, err := Open(recipient, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, groupKey) {
		t.Fatal("round-trip mismatch")
	}
}

func TestSealIsRandomizedPerCall(t *testing.T) {
	recipient := keyA(t)
	s1, _ := Seal(recipient.PublicKey(), groupKey)
	s2, _ := Seal(recipient.PublicKey(), groupKey)
	if bytes.Equal(s1, s2) {
		t.Fatal("two seals of the same plaintext were identical (ephemeral/nonce reused)")
	}
	// Both still open to the same plaintext.
	for _, s := range [][]byte{s1, s2} {
		got, err := Open(recipient, s)
		if err != nil || !bytes.Equal(got, groupKey) {
			t.Fatalf("open failed: %v", err)
		}
	}
}

func TestOpenWrongRecipientFails(t *testing.T) {
	a := keyA(t)
	b, _ := X25519FromSeed(seedB)
	sealed, _ := Seal(a.PublicKey(), groupKey)
	if _, err := Open(b, sealed); err == nil {
		t.Fatal("a value sealed to A was opened by B")
	}
}

func TestOpenTamperedFails(t *testing.T) {
	recipient := keyA(t)
	sealed, _ := Seal(recipient.PublicKey(), groupKey)
	// Flip one byte in each region: ephemeral pub, nonce, ciphertext/tag.
	for _, pos := range []int{0, x25519KeySize, len(sealed) - 1} {
		tampered := bytes.Clone(sealed)
		tampered[pos] ^= 0xFF
		if _, err := Open(recipient, tampered); err == nil {
			t.Fatalf("tampering at byte %d was not detected", pos)
		}
	}
	if _, err := Open(recipient, sealed[:x25519KeySize+nonceSize-1]); err == nil {
		t.Fatal("truncated sealed value accepted")
	}
}

func TestGroupSealRoundTripAndRejection(t *testing.T) {
	sealed, err := SealGroup(groupKey, bundle)
	if err != nil {
		t.Fatal(err)
	}
	got, err := OpenGroup(groupKey, sealed)
	if err != nil || !bytes.Equal(got, bundle) {
		t.Fatalf("group round-trip failed: %v", err)
	}
	// Wrong key.
	other := bytes.Repeat([]byte{0xBB}, GroupKeySize)
	if _, err := OpenGroup(other, sealed); err == nil {
		t.Fatal("bundle opened under the wrong group key")
	}
	// Tamper.
	tampered := bytes.Clone(sealed)
	tampered[len(tampered)-1] ^= 0xFF
	if _, err := OpenGroup(groupKey, tampered); err == nil {
		t.Fatal("tampered bundle accepted")
	}
	// Bad key length.
	if _, err := SealGroup(groupKey[:16], bundle); err == nil {
		t.Fatal("short group key accepted by SealGroup")
	}
	if _, err := OpenGroup(groupKey[:16], sealed); err == nil {
		t.Fatal("short group key accepted by OpenGroup")
	}
}

// End-to-end: the way a profile actually shares a bundle. Owner seals the bundle
// under a group key, seals the group key to each peer, and only that peer recovers
// both.
func TestEndToEndEnvelopeToBundle(t *testing.T) {
	peer := keyA(t)
	stranger, _ := X25519FromSeed(seedB)

	gk, err := NewGroupKey()
	if err != nil {
		t.Fatal(err)
	}
	sealedBundle, _ := SealGroup(gk, bundle)
	envelope, _ := Seal(peer.PublicKey(), gk)

	// The peer opens its envelope, then the bundle.
	recoveredKey, err := Open(peer, envelope)
	if err != nil {
		t.Fatal(err)
	}
	recoveredBundle, err := OpenGroup(recoveredKey, sealedBundle)
	if err != nil || !bytes.Equal(recoveredBundle, bundle) {
		t.Fatalf("peer could not recover bundle: %v", err)
	}
	// A stranger with the sealed bundle but no envelope learns nothing.
	if _, err := Open(stranger, envelope); err == nil {
		t.Fatal("stranger opened the peer's envelope")
	}
}

// Known-answer tests pin the wire format: fixed ephemeral key and nonce must always
// produce these exact bytes, so an accidental format change is caught.
func TestSealKnownAnswer(t *testing.T) {
	recipient := keyA(t)
	ephemeral, err := ecdh.X25519().NewPrivateKey(ephScalar)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := sealWith(ephemeral, recipient.PublicKey(), fixedNonceFn(fixNonce), groupKey)
	if err != nil {
		t.Fatal(err)
	}
	const want = "13be4feaeaf204c7fd3358fc9c00721881d174278128227ec674f37f7fe97b6d000102030405060708090a0bcf0d835dd9162df5c3fb546751fe95d22350821efc45e498a96e4a5efa0930865f51d9adbac080b8e8aff499f465b19b"
	if got := hex.EncodeToString(sealed); got != want {
		t.Fatalf("seal golden drift:\n got=%s", got)
	}
	// And it must still open.
	out, err := Open(recipient, sealed)
	if err != nil || !bytes.Equal(out, groupKey) {
		t.Fatalf("golden seal did not open: %v", err)
	}
}

func TestGroupKnownAnswer(t *testing.T) {
	sealed, err := sealGroupWith(groupKey, fixedNonceFn(fixNonce), bundle)
	if err != nil {
		t.Fatal(err)
	}
	const want = "000102030405060708090a0be1fab19a80e48389c72f6544c41e89b2a07eb5219134b115536fb3878e5f0e268b79302f1e79e2116ea5b949989de8924ee0c5804806815a9dc2853f9643029054648b714fc4fa98b5"
	if got := hex.EncodeToString(sealed); got != want {
		t.Fatalf("group golden drift:\n got=%s", got)
	}
}
