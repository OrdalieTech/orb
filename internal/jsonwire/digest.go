package jsonwire

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
)

// Digest returns the base64url SHA-256 of compact JSON raw, matching Mitosis sync entry digests.
func Digest(raw []byte) string {
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		raw = compact.Bytes()
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
