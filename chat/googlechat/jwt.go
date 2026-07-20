package googlechat

// jwt.go verifies the bearer JWT Google attaches to every inbound
// interaction event: RS256 via the stdlib against the published JWKS for
// chat@system.gserviceaccount.com, with issuer, audience (numeric project
// number), and expiry checks. This is the ingress trust boundary — the
// webhook rejects every request that fails it, and [New] refuses to
// construct without the audience.

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const chatIssuer = "chat@system.gserviceaccount.com"

const clockSkew = 60 * time.Second

const defaultCertTTL = 5 * time.Minute

// Unknown kids cannot stampede JWKS while real key rotation still refetches.
const defaultRefetchFloor = 30 * time.Second

type verifier struct {
	certURL  string
	audience string
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	expires time.Time
	fetched time.Time
	// refetchFloor is configurable only as a deterministic test seam.
	refetchFloor time.Duration
}

func (v *verifier) verify(ctx context.Context, token string) error {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("googlechat: malformed bearer token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("googlechat: malformed token header")
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return errors.New("googlechat: malformed token header")
	}
	if header.Alg != "RS256" {
		return fmt.Errorf("googlechat: unsupported token alg %q", header.Alg)
	}
	key, err := v.key(ctx, header.Kid)
	if err != nil {
		return err
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("googlechat: malformed token signature")
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], signature); err != nil {
		return errors.New("googlechat: token signature verification failed")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("googlechat: malformed token claims")
	}
	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return errors.New("googlechat: malformed token claims")
	}
	if claims.Iss != chatIssuer {
		return fmt.Errorf("googlechat: unexpected token issuer %q", claims.Iss)
	}
	if claims.Aud != v.audience {
		return errors.New("googlechat: token audience does not match the project number")
	}
	if v.now().After(time.Unix(claims.Exp, 0).Add(clockSkew)) {
		return errors.New("googlechat: token expired")
	}
	return nil
}

func (v *verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	if now.Before(v.expires) {
		if key, ok := v.keys[kid]; ok {
			return key, nil
		}
		floor := v.refetchFloor
		if floor <= 0 {
			floor = defaultRefetchFloor
		}
		if now.Sub(v.fetched) < floor {
			return nil, fmt.Errorf("googlechat: no JWKS key for kid %q", kid)
		}
	}
	if err := v.fetchLocked(ctx); err != nil {
		return nil, err
	}
	key, ok := v.keys[kid]
	if !ok {
		return nil, fmt.Errorf("googlechat: no JWKS key for kid %q", kid)
	}
	return key, nil
}

func (v *verifier) fetchLocked(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certURL, nil)
	if err != nil {
		return fmt.Errorf("googlechat: build JWKS request: %w", err)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return fmt.Errorf("googlechat: fetch JWKS: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("googlechat: read JWKS: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("googlechat: JWKS endpoint returned HTTP %d", resp.StatusCode)
	}
	var payload struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return fmt.Errorf("googlechat: decode JWKS: %w", err)
	}
	keys := make(map[string]*rsa.PublicKey, len(payload.Keys))
	for _, jwk := range payload.Keys {
		if jwk.Kty != "RSA" || jwk.Kid == "" {
			continue
		}
		key, err := rsaKeyFromJWK(jwk.N, jwk.E)
		if err != nil {
			continue
		}
		keys[jwk.Kid] = key
	}
	if len(keys) == 0 {
		return errors.New("googlechat: JWKS contains no usable RSA keys")
	}
	v.keys = keys
	v.fetched = v.now()
	v.expires = v.fetched.Add(certTTL(resp.Header.Get("Cache-Control")))
	return nil
}

func rsaKeyFromJWK(n, e string) (*rsa.PublicKey, error) {
	nBytes, err := base64.RawURLEncoding.DecodeString(n)
	if err != nil {
		return nil, err
	}
	eBytes, err := base64.RawURLEncoding.DecodeString(e)
	if err != nil {
		return nil, err
	}
	if len(nBytes) == 0 || len(eBytes) == 0 || len(eBytes) > 8 {
		return nil, errors.New("bad JWK component length")
	}
	exponent := new(big.Int).SetBytes(eBytes)
	if !exponent.IsInt64() || exponent.Int64() <= 1 {
		return nil, errors.New("bad JWK exponent")
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: int(exponent.Int64())}, nil
}

func certTTL(cacheControl string) time.Duration {
	for _, directive := range strings.Split(cacheControl, ",") {
		directive = strings.TrimSpace(directive)
		if value, ok := strings.CutPrefix(directive, "max-age="); ok {
			if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
				return time.Duration(seconds) * time.Second
			}
		}
	}
	return defaultCertTTL
}
