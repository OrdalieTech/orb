package googlechat

// fake_api_test.go is the shared test infrastructure: a generated RSA key
// with matching credentials JSON and JWKS, a fake OAuth token endpoint, a
// fake Chat API recording every call, and inbound-JWT builders. Everything
// runs against httptest servers — no live network.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// testProjectNumber is the numeric project number used across the tests.
const testProjectNumber = "123456789012"

// testKID is the JWKS key id the fake cert endpoint serves.
const testKID = "test-kid"

var (
	rsaKeyOnce sync.Once
	rsaKey     *rsa.PrivateKey
)

// testRSAKey returns the process-wide 2048-bit test key.
func testRSAKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	rsaKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		rsaKey = key
	})
	return rsaKey
}

// testCredentialsJSON builds a service-account key file around the test key.
func testCredentialsJSON(t *testing.T) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(testRSAKey(t))
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	pemText := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	creds, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"client_email":   "app@test-project.iam.gserviceaccount.com",
		"private_key":    string(pemText),
		"private_key_id": "sa-key-id",
	})
	if err != nil {
		t.Fatalf("marshal creds: %v", err)
	}
	return creds
}

// newCertServer serves a JWKS for the test key under testKID.
func newCertServer(t *testing.T, fetches *int32) *httptest.Server {
	t.Helper()
	pub := &testRSAKey(t).PublicKey
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fetches != nil {
			mu.Lock()
			*fetches++
			mu.Unlock()
		}
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"keys": []map[string]string{{
				"kty": "RSA",
				"alg": "RS256",
				"use": "sig",
				"kid": testKID,
				"n":   base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
			}},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// signTestJWT signs headerJSON.claimsJSON with key.
func signTestJWT(t *testing.T, key *rsa.PrivateKey, header, claims map[string]any) string {
	t.Helper()
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	input := base64.RawURLEncoding.EncodeToString(headerJSON) + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(input))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// inboundJWT builds a valid inbound event token, with overrides applied on
// top of the default claims.
func inboundJWT(t *testing.T, overrides map[string]any) string {
	t.Helper()
	claims := map[string]any{
		"iss": chatIssuer,
		"aud": testProjectNumber,
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	for k, v := range overrides {
		claims[k] = v
	}
	return signTestJWT(t, testRSAKey(t), map[string]any{"alg": "RS256", "typ": "JWT", "kid": testKID}, claims)
}

// apiCall is one recorded Chat API request.
type apiCall struct {
	Method string
	Path   string
	Query  url.Values
	Body   apiMessage
	Bearer string
}

// scripted is one canned response served before the fake's default
// behavior resumes. The zero value ("pass") lets that call fall through to
// the default behavior, so failures can target a later call.
type scripted struct {
	status     int
	grpcStatus string
	retryAfter string
}

// fakeChat is an httptest-backed Chat API: message store, call recorder,
// and a scriptable failure queue consumed by message-write calls.
type fakeChat struct {
	t *testing.T

	mu       sync.Mutex
	calls    []apiCall
	script   []scripted
	messages map[string]string // resource name -> text
	media    map[string][]byte // resource name -> content

	server *httptest.Server
}

func newFakeChat(t *testing.T) *fakeChat {
	f := &fakeChat{t: t, messages: map[string]string{}, media: map[string][]byte{}}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

// pushScript queues canned responses for the next message-write calls.
func (f *fakeChat) pushScript(responses ...scripted) {
	f.mu.Lock()
	f.script = append(f.script, responses...)
	f.mu.Unlock()
}

// callLog returns a copy of the recorded calls.
func (f *fakeChat) callLog() []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]apiCall(nil), f.calls...)
}

// text returns the stored text of a message.
func (f *fakeChat) text(name string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.messages[name]
}

// setMessage pre-stores a message.
func (f *fakeChat) setMessage(name, text string) {
	f.mu.Lock()
	f.messages[name] = text
	f.mu.Unlock()
}

// setMedia pre-stores downloadable media content.
func (f *fakeChat) setMedia(resource string, content []byte) {
	f.mu.Lock()
	f.media[resource] = content
	f.mu.Unlock()
}

func writeAPIError(w http.ResponseWriter, status int, grpcStatus, retryAfter string) {
	if retryAfter != "" {
		w.Header().Set("Retry-After", retryAfter)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `{"error":{"code":%d,"message":"scripted","status":%q}}`, status, grpcStatus)
}

func (f *fakeChat) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	call := apiCall{
		Method: r.Method,
		Path:   r.URL.Path,
		Query:  r.URL.Query(),
		Bearer: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "),
	}
	body, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(body, &call.Body)
	f.calls = append(f.calls, call)

	// Media download.
	if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/media/") {
		resource := strings.TrimPrefix(r.URL.Path, "/v1/media/")
		content, ok := f.media[resource]
		if !ok || r.URL.Query().Get("alt") != "media" {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "")
			return
		}
		_, _ = w.Write(content)
		return
	}

	// Scripted failures apply to message writes; a zero entry passes the
	// call through to the default behavior.
	if len(f.script) > 0 {
		next := f.script[0]
		f.script = f.script[1:]
		if next.status != 0 {
			writeAPIError(w, next.status, next.grpcStatus, next.retryAfter)
			return
		}
	}

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/messages"):
		space := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/messages")
		name := space + "/messages/" + fmt.Sprintf("srv-%d", len(f.calls))
		if id := r.URL.Query().Get("messageId"); id != "" {
			name = space + "/messages/" + id
			if _, exists := f.messages[name]; exists {
				writeAPIError(w, http.StatusConflict, "ALREADY_EXISTS", "")
				return
			}
		}
		f.messages[name] = call.Body.Text
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiMessage{Name: name, Text: call.Body.Text})
	case r.Method == http.MethodPatch:
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		_, exists := f.messages[name]
		if !exists && r.URL.Query().Get("allowMissing") != "true" {
			writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "")
			return
		}
		f.messages[name] = call.Body.Text
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(apiMessage{Name: name, Text: call.Body.Text})
	default:
		writeAPIError(w, http.StatusNotFound, "NOT_FOUND", "")
	}
}

// fakeToken is the OAuth token endpoint: it validates the grant shape and
// serves sequenced tokens so refreshes are observable.
type fakeToken struct {
	t      *testing.T
	mu     sync.Mutex
	mints  int
	server *httptest.Server
}

func newFakeToken(t *testing.T) *fakeToken {
	f := &fakeToken{t: t}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.PostForm.Get("grant_type") != jwtBearerGrant {
			http.Error(w, "bad grant_type", http.StatusBadRequest)
			return
		}
		assertion := r.PostForm.Get("assertion")
		if len(strings.Split(assertion, ".")) != 3 {
			http.Error(w, "bad assertion", http.StatusBadRequest)
			return
		}
		claims := decodeJWTClaims(t, assertion)
		if claims["scope"] != chatBotScope {
			http.Error(w, "bad scope", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.mints++
		token := fmt.Sprintf("tok-%d", f.mints)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
	}))
	t.Cleanup(f.server.Close)
	return f
}

// mintCount returns how many tokens have been issued.
func (f *fakeToken) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.mints
}

// decodeJWTClaims decodes the payload segment of a JWT without verifying.
func decodeJWTClaims(t *testing.T, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %d parts", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	return claims
}

// testEnv bundles the fakes and the adapter wired against them.
type testEnv struct {
	adapter *Adapter
	chatAPI *fakeChat
	token   *fakeToken
	delays  *[]time.Duration
}

// newTestEnv builds an adapter against fresh fakes with recorded zero-length
// sleeps.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	chatAPI := newFakeChat(t)
	token := newFakeToken(t)
	cert := newCertServer(t, nil)
	adapter, err := New(Options{
		ProjectNumber:   testProjectNumber,
		CredentialsJSON: testCredentialsJSON(t),
		BaseURL:         chatAPI.server.URL,
		TokenURL:        token.server.URL,
		CertURL:         cert.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	delays := &[]time.Duration{}
	adapter.sleep = func(ctx context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return nil
	}
	return &testEnv{adapter: adapter, chatAPI: chatAPI, token: token, delays: delays}
}
