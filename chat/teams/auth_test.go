package teams

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pi-go/chat"
)

// TestWebhookJWTValidationMatrix drives the full inbound trust boundary
// through the webhook: every failed check answers 403 and nothing is
// published.
func TestWebhookJWTValidationMatrix(t *testing.T) {
	env := newTestEnv(t)
	published := 0
	handler := env.adapter.Webhook(func(m chat.Message) error { published++; return nil })
	serviceURL := env.connector.server.URL
	keyA, keyB := testKeys(t)

	mint := func(mutate func(map[string]any)) string {
		return env.bearer(t, serviceURL, mutate)
	}
	// tampered re-encodes the payload segment (claims changed after
	// signing) without re-signing.
	tampered := func() string {
		token := strings.TrimPrefix(mint(nil), "Bearer ")
		parts := strings.Split(token, ".")
		payload, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		forged := strings.Replace(string(payload), testAppID, "forged-app-id", 1)
		parts[1] = base64.RawURLEncoding.EncodeToString([]byte(forged))
		return "Bearer " + strings.Join(parts, ".")
	}

	cases := []struct {
		name string
		auth string
		want int
	}{
		{"valid", mint(nil), http.StatusOK},
		{"missing header", "", http.StatusForbidden},
		{"not a bearer", "Basic abc", http.StatusForbidden},
		{"malformed token", "Bearer not.a", http.StatusForbidden},
		{"wrong issuer", mint(func(c map[string]any) { c["iss"] = "https://evil.example" }), http.StatusForbidden},
		{"wrong audience", mint(func(c map[string]any) { c["aud"] = "22222222-0000-0000-0000-000000000000" }), http.StatusForbidden},
		{"wrong serviceUrl claim", mint(func(c map[string]any) { c["serviceurl"] = "https://attacker.example" }), http.StatusForbidden},
		{"serviceUrl differs by one byte", mint(func(c map[string]any) { c["serviceurl"] = serviceURL + "/" }), http.StatusForbidden},
		{"missing serviceUrl claim", mint(func(c map[string]any) { delete(c, "serviceurl") }), http.StatusForbidden},
		{"camelCase serviceUrl claim accepted", mint(func(c map[string]any) {
			delete(c, "serviceurl")
			c["serviceUrl"] = serviceURL
		}), http.StatusOK},
		{"audience array accepted", mint(func(c map[string]any) { c["aud"] = []string{testAppID} }), http.StatusOK},
		{"expired beyond skew", mint(func(c map[string]any) { c["exp"] = time.Now().Add(-10 * time.Minute).Unix() }), http.StatusForbidden},
		{"expired within skew", mint(func(c map[string]any) { c["exp"] = time.Now().Add(-2 * time.Minute).Unix() }), http.StatusOK},
		{"missing exp", mint(func(c map[string]any) { delete(c, "exp") }), http.StatusForbidden},
		{"not yet valid beyond skew", mint(func(c map[string]any) { c["nbf"] = time.Now().Add(10 * time.Minute).Unix() }), http.StatusForbidden},
		{"nbf within skew", mint(func(c map[string]any) { c["nbf"] = time.Now().Add(2 * time.Minute).Unix() }), http.StatusOK},
		{"alg none", "Bearer " + mintToken(t, keyA, testKid, "none", defaultClaims(serviceURL)), http.StatusForbidden},
		{"alg HS256", "Bearer " + mintToken(t, keyA, testKid, "HS256", defaultClaims(serviceURL)), http.StatusForbidden},
		{"unknown kid", "Bearer " + mintToken(t, keyA, "ghost-kid", "RS256", defaultClaims(serviceURL)), http.StatusForbidden},
		{"wrong signing key", "Bearer " + mintToken(t, keyB, testKid, "RS256", defaultClaims(serviceURL)), http.StatusForbidden},
		{"tampered payload", tampered(), http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := published
			recorder := postActivity(t, handler, personalActivity(serviceURL), tc.auth)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d", recorder.Code, tc.want)
			}
			gotPublish := published - before
			wantPublish := 0
			if tc.want == http.StatusOK {
				wantPublish = 1
			}
			if gotPublish != wantPublish {
				t.Fatalf("published %d messages, want %d", gotPublish, wantPublish)
			}
		})
	}
}

// TestWebhookServiceURLBinding proves the serviceUrl claim is compared
// against the request body: a token minted for one connector cannot
// authenticate an activity pointing replies elsewhere.
func TestWebhookServiceURLBinding(t *testing.T) {
	env := newTestEnv(t)
	handler := env.adapter.Webhook(func(chat.Message) error { return nil })
	auth := env.bearer(t, env.connector.server.URL, nil)
	activity := personalActivity("https://attacker.example/redirected")
	if recorder := postActivity(t, handler, activity, auth); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// TestJWKSRotationRefetch proves the unknown-kid retry-once path: a token
// signed by a freshly rotated key is accepted after a forced JWKS refetch.
func TestJWKSRotationRefetch(t *testing.T) {
	env := newTestEnv(t)
	env.adapter.validator.refetchFloor = 0
	handler := env.adapter.Webhook(func(chat.Message) error { return nil })
	serviceURL := env.connector.server.URL

	if recorder := postActivity(t, handler, personalActivity(serviceURL), env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusOK {
		t.Fatalf("priming request status = %d", recorder.Code)
	}
	fetchesBefore := env.idp.keyFetches()

	_, keyB := testKeys(t)
	env.idp.setKey("test-2", keyB, []string{"msteams"})
	rotated := "Bearer " + mintToken(t, keyB, "test-2", "RS256", defaultClaims(serviceURL))
	if recorder := postActivity(t, handler, personalActivity(serviceURL), rotated); recorder.Code != http.StatusOK {
		t.Fatalf("rotated-key request status = %d, want 200", recorder.Code)
	}
	if fetches := env.idp.keyFetches(); fetches <= fetchesBefore {
		t.Fatalf("JWKS fetches = %d, want > %d (forced refetch on unknown kid)", fetches, fetchesBefore)
	}
}

// TestEndorsementsFilter proves a signing key not endorsed for msteams is
// rejected even when the signature and claims are valid.
func TestEndorsementsFilter(t *testing.T) {
	env := newTestEnv(t)
	env.idp.setEndorsements(testKid, []string{"directline"})
	handler := env.adapter.Webhook(func(chat.Message) error {
		t.Error("unexpected publish")
		return nil
	})
	serviceURL := env.connector.server.URL
	if recorder := postActivity(t, handler, personalActivity(serviceURL), env.bearer(t, serviceURL, nil)); recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
}

// TestNewRefusesValidationlessConfig: without the app id there is no
// audience to validate against, so construction fails.
func TestNewRefusesValidationlessConfig(t *testing.T) {
	if _, err := New(Options{AppPassword: "x"}); err == nil {
		t.Fatal("New without AppID should fail")
	}
	if _, err := New(Options{AppID: testAppID}); err == nil {
		t.Fatal("New without AppPassword should fail")
	}
}
