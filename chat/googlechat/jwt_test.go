package googlechat

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"strings"
	"testing"
	"time"
)

// newTestVerifier wires a verifier against the fake cert server.
func newTestVerifier(t *testing.T, fetches *int32) *verifier {
	t.Helper()
	cert := newCertServer(t, fetches)
	return &verifier{
		certURL:  cert.URL,
		audience: testProjectNumber,
		client:   http.DefaultClient,
		now:      time.Now,
	}
}

func TestVerifyMatrix(t *testing.T) {
	key := testRSAKey(t)
	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	header := func(overrides map[string]any) map[string]any {
		h := map[string]any{"alg": "RS256", "typ": "JWT", "kid": testKID}
		for k, v := range overrides {
			h[k] = v
		}
		return h
	}
	claims := func(overrides map[string]any) map[string]any {
		c := map[string]any{
			"iss": chatIssuer,
			"aud": testProjectNumber,
			"exp": time.Now().Add(time.Hour).Unix(),
		}
		for k, v := range overrides {
			c[k] = v
		}
		return c
	}

	cases := []struct {
		name    string
		token   func(t *testing.T) string
		wantErr string // "" means the token must verify
	}{
		{"valid", func(t *testing.T) string {
			return signTestJWT(t, key, header(nil), claims(nil))
		}, ""},
		{"expired within skew", func(t *testing.T) string {
			return signTestJWT(t, key, header(nil), claims(map[string]any{"exp": time.Now().Add(-30 * time.Second).Unix()}))
		}, ""},
		{"expired beyond skew", func(t *testing.T) string {
			return signTestJWT(t, key, header(nil), claims(map[string]any{"exp": time.Now().Add(-2 * time.Minute).Unix()}))
		}, "expired"},
		{"wrong issuer", func(t *testing.T) string {
			return signTestJWT(t, key, header(nil), claims(map[string]any{"iss": "attacker@example.com"}))
		}, "issuer"},
		{"wrong audience (project id instead of number)", func(t *testing.T) string {
			return signTestJWT(t, key, header(nil), claims(map[string]any{"aud": "my-project-id"}))
		}, "audience"},
		{"signed by the wrong key", func(t *testing.T) string {
			return signTestJWT(t, otherKey, header(nil), claims(nil))
		}, "signature"},
		{"unknown kid", func(t *testing.T) string {
			return signTestJWT(t, key, header(map[string]any{"kid": "rotated-away"}), claims(nil))
		}, "no JWKS key"},
		{"alg none", func(t *testing.T) string {
			return signTestJWT(t, key, header(map[string]any{"alg": "none"}), claims(nil))
		}, "alg"},
		{"alg HS256", func(t *testing.T) string {
			return signTestJWT(t, key, header(map[string]any{"alg": "HS256"}), claims(nil))
		}, "alg"},
		{"malformed", func(t *testing.T) string { return "not.a-jwt" }, "malformed"},
		{"tampered payload", func(t *testing.T) string {
			token := signTestJWT(t, key, header(nil), claims(nil))
			parts := strings.Split(token, ".")
			parts[1] = parts[1][:len(parts[1])-2] + "xx"
			return strings.Join(parts, ".")
		}, ""}, // any error is acceptable; checked below
	}
	verifier := newTestVerifier(t, nil)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := verifier.verify(context.Background(), tc.token(t))
			if tc.name == "tampered payload" {
				if err == nil {
					t.Fatal("tampered token verified")
				}
				return
			}
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("verify: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("token verified, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestVerifierCachesJWKS(t *testing.T) {
	var fetches int32
	verifier := newTestVerifier(t, &fetches)
	token := inboundJWT(t, nil)
	for range 3 {
		if err := verifier.verify(context.Background(), token); err != nil {
			t.Fatalf("verify: %v", err)
		}
	}
	if fetches != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 (Cache-Control max-age honored)", fetches)
	}
}

func TestVerifierThrottlesUnknownKidRefetch(t *testing.T) {
	var fetches int32
	verifier := newTestVerifier(t, &fetches)
	current := time.Now()
	verifier.now = func() time.Time { return current }
	if err := verifier.verify(context.Background(), inboundJWT(t, nil)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	rotated := signTestJWT(t, testRSAKey(t),
		map[string]any{"alg": "RS256", "kid": "rotated-away"},
		map[string]any{"iss": chatIssuer, "aud": testProjectNumber, "exp": time.Now().Add(time.Hour).Unix()})
	for range 3 {
		if err := verifier.verify(context.Background(), rotated); err == nil {
			t.Fatal("unknown kid verified")
		}
	}
	if fetches != 1 {
		t.Fatalf("JWKS fetched %d times, want 1 while the unknown-kid refetch floor is active", fetches)
	}
	current = current.Add(defaultRefetchFloor + time.Second)
	if err := verifier.verify(context.Background(), rotated); err == nil {
		t.Fatal("unknown kid verified after the refetch floor")
	}
	if fetches != 2 {
		t.Fatalf("JWKS fetched %d times, want one key-rotation retry after the floor", fetches)
	}
}
