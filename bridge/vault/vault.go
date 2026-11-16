// Package vault seals the secrets a Mitosis profile shares between its peers, so
// a credential reaches a new peer encrypted to that peer's key and never as a raw
// value on the wire or on anyone else's disk. It is stdlib-only and self-contained:
// identity keys, a symmetric group key, and a sealed box bind a payload to one
// recipient's X25519 key.
//
// Two layers compose:
//
//   - A profile has a 256-bit group key (NewGroupKey). SealGroup/OpenGroup encrypt
//     the shared bundle with it once for every peer (AES-256-GCM).
//   - The group key travels sealed to each admitted peer: Seal encrypts it to that
//     peer's X25519 public key, and only that peer's private key opens it (Open).
//
// Every Orb's X25519 key is derived from its Ed25519 identity seed (X25519FromSeed),
// so it is available wherever the identity is, with nothing new to persist.
package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"io"
)

// GroupKeySize is the length of a profile group key, a 256-bit AES key.
const GroupKeySize = 32

const (
	x25519KeySize = 32
	nonceSize     = 12 // AES-GCM standard nonce
)

// X25519FromSeed derives a peer's X25519 key from its Ed25519 identity seed, so the
// two live and die together and nothing new has to be stored. The derivation is
// domain-separated, so the X25519 scalar is independent of the Ed25519 key despite
// sharing the seed.
func X25519FromSeed(seed []byte) (*ecdh.PrivateKey, error) {
	if len(seed) < 32 {
		return nil, errors.New("vault: seed too short")
	}
	sum := sha512.Sum512(append([]byte("orb-mitosis-x25519/1\n"), seed...))
	return ecdh.X25519().NewPrivateKey(sum[:x25519KeySize])
}

// NewGroupKey returns a fresh random profile group key.
func NewGroupKey() ([]byte, error) {
	key := make([]byte, GroupKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	return key, nil
}

// Seal encrypts plaintext (a group key, typically) to one recipient's X25519 public
// key. The output is ephemeralPublic(32) || nonce(12) || ciphertext; only the holder
// of the matching private key can Open it. A fresh ephemeral key per call means two
// seals of the same plaintext differ.
func Seal(recipient *ecdh.PublicKey, plaintext []byte) ([]byte, error) {
	ephemeral, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return sealWith(ephemeral, recipient, randomNonce, plaintext)
}

// Open reverses Seal with the recipient's private key.
func Open(recipient *ecdh.PrivateKey, sealed []byte) ([]byte, error) {
	if len(sealed) < x25519KeySize+nonceSize {
		return nil, errors.New("vault: sealed value too short")
	}
	ephemeralPub, err := ecdh.X25519().NewPublicKey(sealed[:x25519KeySize])
	if err != nil {
		return nil, err
	}
	shared, err := recipient.ECDH(ephemeralPub)
	if err != nil {
		return nil, err
	}
	gcm, err := newGCM(deriveKey(shared, ephemeralPub.Bytes(), recipient.PublicKey().Bytes()))
	if err != nil {
		return nil, err
	}
	nonce := sealed[x25519KeySize : x25519KeySize+nonceSize]
	return gcm.Open(nil, nonce, sealed[x25519KeySize+nonceSize:], ephemeralPub.Bytes())
}

// SealGroup encrypts a bundle with a profile group key: nonce(12) || ciphertext.
func SealGroup(groupKey, plaintext []byte) ([]byte, error) {
	return sealGroupWith(groupKey, randomNonce, plaintext)
}

// OpenGroup reverses SealGroup.
func OpenGroup(groupKey, sealed []byte) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, errors.New("vault: group key must be 32 bytes")
	}
	if len(sealed) < nonceSize {
		return nil, errors.New("vault: sealed bundle too short")
	}
	gcm, err := newGCM(groupKey)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, sealed[:nonceSize], sealed[nonceSize:], nil)
}

// sealWith is Seal with an explicit ephemeral key and nonce source, for deterministic
// fixtures and for the randomized public entry point alike.
func sealWith(ephemeral *ecdh.PrivateKey, recipient *ecdh.PublicKey, nonceFn func() ([]byte, error), plaintext []byte) ([]byte, error) {
	shared, err := ephemeral.ECDH(recipient)
	if err != nil {
		return nil, err
	}
	ephemeralPub := ephemeral.PublicKey().Bytes()
	gcm, err := newGCM(deriveKey(shared, ephemeralPub, recipient.Bytes()))
	if err != nil {
		return nil, err
	}
	nonce, err := nonceFn()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, x25519KeySize+nonceSize+len(plaintext)+gcm.Overhead())
	out = append(out, ephemeralPub...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, ephemeralPub), nil
}

func sealGroupWith(groupKey []byte, nonceFn func() ([]byte, error), plaintext []byte) ([]byte, error) {
	if len(groupKey) != GroupKeySize {
		return nil, errors.New("vault: group key must be 32 bytes")
	}
	gcm, err := newGCM(groupKey)
	if err != nil {
		return nil, err
	}
	nonce, err := nonceFn()
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, nonceSize+len(plaintext)+gcm.Overhead())
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

// deriveKey turns an X25519 shared secret into an AES-256 key, binding both public
// keys so a sealed value cannot be replayed under a different pairing.
func deriveKey(shared, ephemeralPub, recipientPub []byte) []byte {
	h := sha256.New()
	h.Write([]byte("orb-mitosis-seal/1\n"))
	h.Write(shared)
	h.Write(ephemeralPub)
	h.Write(recipientPub)
	return h.Sum(nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func randomNonce() ([]byte, error) {
	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return nonce, nil
}
