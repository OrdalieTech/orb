package jwks

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func signRS256(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	signing := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestSetRejectsEmptyKidAndThrottlesUnknownKidRefetchEvenOnError(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	fetches := 0
	failNext := false
	set := &Set{
		Floor: 30 * time.Second,
		Now:   func() time.Time { return now },
		Fetch: func(context.Context) (map[string]Key, time.Duration, error) {
			fetches++
			if failNext {
				return nil, 0, errors.New("upstream 503")
			}
			return map[string]Key{"k1": {PublicKey: &priv.PublicKey}}, time.Hour, nil
		},
	}

	// Empty kid is rejected before touching Fetch.
	noKid := signRS256(t, priv, map[string]any{"alg": "RS256"}, map[string]any{"sub": "u1"})
	if _, _, err := set.Verify(context.Background(), noKid); err == nil || fetches != 0 {
		t.Fatalf("empty kid err=%v fetches=%d, want error with 0 fetches", err, fetches)
	}

	valid := signRS256(t, priv, map[string]any{"alg": "RS256", "kid": "k1"}, map[string]any{"sub": "u1"})
	if _, _, err := set.Verify(context.Background(), valid); err != nil || fetches != 1 {
		t.Fatalf("valid token err=%v fetches=%d", err, fetches)
	}

	// After Floor elapses, a transient Fetch error on an unknown kid updates fetched
	// so subsequent unknown-kid requests do not stampede Fetch, and cached k1 keeps working.
	now = now.Add(31 * time.Second)
	failNext = true
	unknown := signRS256(t, priv, map[string]any{"alg": "RS256", "kid": "k2"}, map[string]any{"sub": "u1"})
	if _, _, err := set.Verify(context.Background(), unknown); err == nil || fetches != 2 {
		t.Fatalf("first unknown kid after floor err=%v fetches=%d", err, fetches)
	}
	if _, _, err := set.Verify(context.Background(), unknown); err == nil || fetches != 2 {
		t.Fatalf("second unknown kid within floor err=%v fetches=%d, want throttled at 2", err, fetches)
	}
	if _, _, err := set.Verify(context.Background(), valid); err != nil || fetches != 2 {
		t.Fatalf("cached k1 during outage err=%v fetches=%d", err, fetches)
	}
}

func TestGetSkips32BitOverflowExponent(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	overflowE := new(big.Int).SetInt64(1 << 31).Bytes()
	goodE := big.NewInt(int64(priv.E)).Bytes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]any{
				{"kty": "RSA", "kid": "overflow", "n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(overflowE)},
				{"kty": "RSA", "kid": "good", "n": base64.RawURLEncoding.EncodeToString(priv.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(goodE)},
			},
		})
	}))
	defer server.Close()

	keys, _, err := Get(context.Background(), server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, bad := keys["overflow"]; bad {
		t.Fatal("accepted 1<<31 exponent that overflows 32-bit int")
	}
	if _, ok := keys["good"]; !ok {
		t.Fatal("missing valid RSA key")
	}
}
