package googlechat

// jwt.go verifies the bearer JWT Google attaches to every inbound
// interaction event: RS256 via the stdlib against the published JWKS for
// chat@system.gserviceaccount.com, with issuer, audience (numeric project
// number), and expiry checks. This is the ingress trust boundary — the
// webhook rejects every request that fails it, and [New] refuses to
// construct without the audience.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/chat/internal/jwks"
)

const chatIssuer = "chat@system.gserviceaccount.com"

const clockSkew = 60 * time.Second

const defaultCertTTL = 5 * time.Minute

// Unknown kids cannot stampede JWKS while real key rotation still refetches.
const defaultRefetchFloor = 30 * time.Second

type verifier struct {
	audience string
	now      func() time.Time
	keys     *jwks.Set
}

// newVerifier caches the JWKS at certURL as its Cache-Control allows.
func newVerifier(certURL, audience string, client *http.Client) *verifier {
	return &verifier{audience: audience, now: time.Now, keys: &jwks.Set{Floor: defaultRefetchFloor, Now: time.Now,
		Fetch: func(ctx context.Context) (map[string]jwks.Key, time.Duration, error) {
			keys, header, err := jwks.Get(ctx, client, certURL)
			if err != nil {
				return nil, 0, fmt.Errorf("googlechat: %w", err)
			}
			return keys, certTTL(header.Get("Cache-Control")), nil
		}}}
}

func (v *verifier) verify(ctx context.Context, token string) error {
	_, claimsJSON, err := v.keys.Verify(ctx, token)
	if err != nil {
		return fmt.Errorf("googlechat: %w", err)
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
