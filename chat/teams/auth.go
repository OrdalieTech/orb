package teams

// auth.go validates inbound Bot Framework JWTs — the adapter's trust
// boundary. Every check is mandatory and none is configurable away: RS256
// signature against the cached JWKS (picked by kid, endorsements filtered
// by channel), issuer, audience (the bot app id), validity window with
// 5-minute clock skew, and the serviceUrl claim matching the activity's
// serviceUrl byte for byte (this blocks redirecting replies — and the
// outbound token — to an attacker host). Validation errors carry only the
// mismatch class, never token material.

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
	"slices"
	"strings"
	"sync"
	"time"
)

const clockSkew = 5 * time.Minute

type signingKey struct {
	key          *rsa.PublicKey
	endorsements []string
}

type jwtValidator struct {
	metadataURL string
	issuer      string
	audience    string
	http        *http.Client
	now         func() time.Time
	// ttl is the JWKS cache lifetime (docs mandate refreshing at least
	// every 24h); refetchFloor throttles the forced unknown-kid refetch so
	// garbage kids cannot stampede the JWKS endpoint.
	ttl          time.Duration
	refetchFloor time.Duration

	mu      sync.Mutex
	keys    map[string]signingKey
	fetched time.Time
}

type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type audienceClaim []string

// UnmarshalJSON implements json.Unmarshaler.
func (a *audienceClaim) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*a = audienceClaim{single}
		return nil
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	*a = audienceClaim(list)
	return nil
}

type jwtClaims struct {
	Issuer   string        `json:"iss"`
	Audience audienceClaim `json:"aud"`
	Exp      float64       `json:"exp"`
	Nbf      float64       `json:"nbf"`
	// The connector emits the claim in lower case; both spellings are
	// accepted, the value check is byte-exact either way.
	ServiceURLLower string `json:"serviceurl"`
	ServiceURLCamel string `json:"serviceUrl"`
}

func (v *jwtValidator) validate(ctx context.Context, authorization, serviceURL, channelID string) error {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return errors.New("missing bearer token")
	}
	parts := strings.Split(authorization[len(prefix):], ".")
	if len(parts) != 3 {
		return errors.New("malformed token")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return errors.New("malformed token header")
	}
	var header jwtHeader
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return errors.New("malformed token header")
	}
	if header.Alg != "RS256" {
		return errors.New("token algorithm is not RS256")
	}
	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return errors.New("malformed token claims")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return errors.New("malformed token signature")
	}

	key, err := v.signingKey(ctx, header.Kid)
	if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key.key, crypto.SHA256, digest[:], signature) != nil {
		return errors.New("signature verification failed")
	}
	if len(key.endorsements) > 0 && !slices.Contains(key.endorsements, channelID) {
		return errors.New("signing key not endorsed for channel")
	}

	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return errors.New("malformed token claims")
	}
	if claims.Issuer != v.issuer {
		return errors.New("issuer mismatch")
	}
	if !slices.Contains(claims.Audience, v.audience) {
		return errors.New("audience mismatch")
	}
	now := v.now()
	if claims.Exp == 0 || now.After(time.Unix(int64(claims.Exp), 0).Add(clockSkew)) {
		return errors.New("token expired")
	}
	if claims.Nbf != 0 && now.Before(time.Unix(int64(claims.Nbf), 0).Add(-clockSkew)) {
		return errors.New("token not yet valid")
	}
	claimed := claims.ServiceURLLower
	if claimed == "" {
		claimed = claims.ServiceURLCamel
	}
	if claimed == "" || claimed != serviceURL {
		return errors.New("serviceUrl claim mismatch")
	}
	return nil
}

func (v *jwtValidator) signingKey(ctx context.Context, kid string) (signingKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys == nil || v.now().Sub(v.fetched) > v.ttl {
		if err := v.refreshLocked(ctx); err != nil {
			return signingKey{}, err
		}
	}
	if key, ok := v.keys[kid]; ok {
		return key, nil
	}
	if v.now().Sub(v.fetched) > v.refetchFloor {
		if err := v.refreshLocked(ctx); err != nil {
			return signingKey{}, err
		}
		if key, ok := v.keys[kid]; ok {
			return key, nil
		}
	}
	return signingKey{}, errors.New("unknown signing key")
}

type openIDMetadata struct {
	JWKSURI string `json:"jwks_uri"`
}

type jwksDocument struct {
	Keys []struct {
		Kty          string   `json:"kty"`
		Kid          string   `json:"kid"`
		N            string   `json:"n"`
		E            string   `json:"e"`
		Endorsements []string `json:"endorsements"`
	} `json:"keys"`
}

func (v *jwtValidator) refreshLocked(ctx context.Context) error {
	var metadata openIDMetadata
	if err := v.getJSON(ctx, v.metadataURL, &metadata); err != nil {
		return fmt.Errorf("openid metadata fetch failed: %w", err)
	}
	if metadata.JWKSURI == "" {
		return errors.New("openid metadata has no jwks_uri")
	}
	var document jwksDocument
	if err := v.getJSON(ctx, metadata.JWKSURI, &document); err != nil {
		return fmt.Errorf("jwks fetch failed: %w", err)
	}
	keys := make(map[string]signingKey, len(document.Keys))
	for _, entry := range document.Keys {
		if entry.Kty != "RSA" || entry.Kid == "" {
			continue
		}
		modulus, err := base64.RawURLEncoding.DecodeString(entry.N)
		if err != nil {
			continue
		}
		exponent, err := base64.RawURLEncoding.DecodeString(entry.E)
		if err != nil {
			continue
		}
		e := new(big.Int).SetBytes(exponent)
		if !e.IsInt64() || e.Int64() <= 1 || e.Int64() > 1<<31 {
			continue
		}
		keys[entry.Kid] = signingKey{
			key:          &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(e.Int64())},
			endorsements: entry.Endorsements,
		}
	}
	if len(keys) == 0 {
		return errors.New("jwks contains no usable RSA keys")
	}
	v.keys = keys
	v.fetched = v.now()
	return nil
}

func (v *jwtValidator) getJSON(ctx context.Context, url string, out any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := v.http.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}
