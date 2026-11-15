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
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/chat/internal/httpjson"
	"github.com/OrdalieTech/orb/chat/internal/jwks"
)

const clockSkew = 5 * time.Minute

type jwtValidator struct {
	issuer   string
	audience string
	now      func() time.Time
	keys     *jwks.Set
}

// newValidator caches the JWKS the OpenID metadata names for 24h (the docs
// mandate refreshing at least daily).
func newValidator(metadataURL, issuer, audience string, client *http.Client) *jwtValidator {
	return &jwtValidator{issuer: issuer, audience: audience, now: time.Now, keys: &jwks.Set{Floor: 30 * time.Second, Now: time.Now,
		Fetch: func(ctx context.Context) (map[string]jwks.Key, time.Duration, error) {
			var metadata struct {
				JWKSURI string `json:"jwks_uri"`
			}
			response, err := httpjson.Do(ctx, client, http.MethodGet, metadataURL, nil, &metadata)
			if err == nil && response.Status != http.StatusOK {
				err = fmt.Errorf("http %d", response.Status)
			}
			if err != nil {
				return nil, 0, fmt.Errorf("openid metadata fetch failed: %w", err)
			}
			if metadata.JWKSURI == "" {
				return nil, 0, errors.New("openid metadata has no jwks_uri")
			}
			keys, _, err := jwks.Get(ctx, client, metadata.JWKSURI)
			return keys, 24 * time.Hour, err
		}}}
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
	token, ok := strings.CutPrefix(authorization, "Bearer ")
	if !ok {
		return errors.New("missing bearer token")
	}
	key, claimsJSON, err := v.keys.Verify(ctx, token)
	if err != nil {
		return err
	}
	if len(key.Endorsements) > 0 && !slices.Contains(key.Endorsements, channelID) {
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
