// Package jwks verifies RS256 JWTs against a cached JSON Web Key Set, the
// trust boundary of the platforms that sign their webhook calls. Callers
// check the claims; errors never carry token material.
package jwks

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/chat/internal/httpjson"
)

// Key is a signing key and the channels it is endorsed for, when listed.
type Key struct {
	*rsa.PublicKey
	Endorsements []string
}

// Set caches the keys Fetch returns for the lifetime it returns. An unknown
// kid refetches at most once per Floor, so garbage kids cannot stampede the
// endpoint while a key rotation is still picked up.
type Set struct {
	Fetch func(context.Context) (map[string]Key, time.Duration, error)
	Floor time.Duration
	Now   func() time.Time

	mu      sync.Mutex
	keys    map[string]Key
	fetched time.Time
	expires time.Time
}

// Verify checks token's RS256 signature and returns the key that signed it
// and the token's claims JSON.
func (s *Set) Verify(ctx context.Context, token string) (Key, []byte, error) {
	parts := strings.SplitN(token, ".", 4)
	if len(parts) != 3 {
		return Key{}, nil, errors.New("malformed token")
	}
	var header struct{ Alg, Kid string }
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(headerJSON, &header) != nil {
		return Key{}, nil, errors.New("malformed token header")
	}
	if header.Alg != "RS256" {
		return Key{}, nil, fmt.Errorf("unsupported token alg %q", header.Alg)
	}
	if header.Kid == "" {
		return Key{}, nil, errors.New("missing token kid")
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Key{}, nil, errors.New("malformed token claims")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Key{}, nil, errors.New("malformed token signature")
	}
	key, err := s.key(ctx, header.Kid)
	if err != nil {
		return Key{}, nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key.PublicKey, crypto.SHA256, digest[:], signature) != nil {
		return Key{}, nil, errors.New("token signature verification failed")
	}
	return key, claims, nil
}

func (s *Set) key(ctx context.Context, kid string) (Key, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	at := now()
	if at.Before(s.expires) {
		if key, ok := s.keys[kid]; ok {
			return key, nil
		}
		if at.Sub(s.fetched) < s.Floor {
			return Key{}, fmt.Errorf("no JWKS key for kid %q", kid)
		}
	}
	keys, ttl, err := s.Fetch(ctx)
	if err != nil {
		if s.keys != nil && at.Before(s.expires) {
			s.fetched = at
		}
		return Key{}, err
	}
	s.keys, s.fetched, s.expires = keys, at, at.Add(ttl)
	key, ok := keys[kid]
	if !ok {
		return Key{}, fmt.Errorf("no JWKS key for kid %q", kid)
	}
	return key, nil
}

// Get fetches the JWKS document at url and returns its usable RSA keys by kid,
// with the response header for its cache policy.
func Get(ctx context.Context, client *http.Client, url string) (map[string]Key, http.Header, error) {
	var document struct {
		Keys []struct {
			Kty, Kid, N, E string
			Endorsements   []string
		}
	}
	response, err := httpjson.Do(ctx, client, http.MethodGet, url, nil, &document)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch JWKS: %w", err)
	}
	if response.Status != http.StatusOK {
		return nil, nil, fmt.Errorf("JWKS endpoint returned HTTP %d", response.Status)
	}
	keys := make(map[string]Key, len(document.Keys))
	for _, jwk := range document.Keys {
		modulus, errN := base64.RawURLEncoding.DecodeString(jwk.N)
		exponent, errE := base64.RawURLEncoding.DecodeString(jwk.E)
		e := new(big.Int).SetBytes(exponent)
		if jwk.Kty != "RSA" || jwk.Kid == "" || errN != nil || errE != nil || len(modulus) == 0 || !e.IsInt64() || e.Int64() <= 1 || e.Int64() > 1<<31-1 {
			continue
		}
		keys[jwk.Kid] = Key{&rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(e.Int64())}, jwk.Endorsements}
	}
	if len(keys) == 0 {
		return nil, nil, errors.New("JWKS contains no usable RSA keys")
	}
	return keys, response.Header, nil
}
