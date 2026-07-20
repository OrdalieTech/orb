package teams

// env_test.go provides the shared fakes: an identity provider serving OpenID
// metadata + JWKS and minting RS256 tokens with stdlib crypto/rsa, a
// connector recording every activity call with scriptable responses, and a
// token endpoint counting client-credential grants.

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAppID = "11111111-2222-3333-4444-555555555555"
	testKid   = "test-1"
)

var (
	rsaKeysOnce sync.Once
	rsaKeyA     *rsa.PrivateKey
	rsaKeyB     *rsa.PrivateKey
)

// testKeys returns two process-wide RSA keys (generated once: keygen
// dominates test time otherwise).
func testKeys(t *testing.T) (*rsa.PrivateKey, *rsa.PrivateKey) {
	t.Helper()
	rsaKeysOnce.Do(func() {
		var err error
		if rsaKeyA, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
		if rsaKeyB, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return rsaKeyA, rsaKeyB
}

type idpKey struct {
	key          *rsa.PrivateKey
	endorsements []string
}

// fakeIDP serves the OpenID metadata document and the JWKS.
type fakeIDP struct {
	server *httptest.Server

	mu      sync.Mutex
	keys    map[string]idpKey
	fetches int
}

func newFakeIDP(t *testing.T) *fakeIDP {
	t.Helper()
	keyA, _ := testKeys(t)
	f := &fakeIDP{keys: map[string]idpKey{testKid: {key: keyA, endorsements: []string{"msteams"}}}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openidconfiguration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                DefaultIssuer,
			"jwks_uri":                              f.server.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.fetches++
		keys := make([]map[string]any, 0, len(f.keys))
		for kid, entry := range f.keys {
			pub := entry.key.Public().(*rsa.PublicKey)
			keys = append(keys, map[string]any{
				"kty":          "RSA",
				"kid":          kid,
				"n":            base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
				"e":            base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
				"endorsements": entry.endorsements,
			})
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeIDP) metadataURL() string { return f.server.URL + "/.well-known/openidconfiguration" }

func (f *fakeIDP) setKey(kid string, key *rsa.PrivateKey, endorsements []string) {
	f.mu.Lock()
	f.keys[kid] = idpKey{key: key, endorsements: endorsements}
	f.mu.Unlock()
}

func (f *fakeIDP) setEndorsements(kid string, endorsements []string) {
	f.mu.Lock()
	entry := f.keys[kid]
	entry.endorsements = endorsements
	f.keys[kid] = entry
	f.mu.Unlock()
}

func (f *fakeIDP) keyFetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

// mintToken builds a JWT signed with key (signature bytes are fake for
// non-RS256 algs, which must be rejected before verification anyway).
func mintToken(t *testing.T, key *rsa.PrivateKey, kid, alg string, claims map[string]any) string {
	t.Helper()
	header := map[string]any{"alg": alg, "typ": "JWT"}
	if kid != "" {
		header["kid"] = kid
	}
	encode := func(v any) string {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(data)
	}
	signing := encode(header) + "." + encode(claims)
	signature := []byte("unsigned")
	if alg == "RS256" {
		digest := sha256.Sum256([]byte(signing))
		var err error
		signature, err = rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
	}
	return signing + "." + base64.RawURLEncoding.EncodeToString(signature)
}

// defaultClaims are valid inbound-token claims for serviceURL.
func defaultClaims(serviceURL string) map[string]any {
	now := time.Now()
	return map[string]any{
		"iss":        DefaultIssuer,
		"aud":        testAppID,
		"serviceurl": serviceURL,
		"exp":        now.Add(time.Hour).Unix(),
		"nbf":        now.Add(-time.Minute).Unix(),
	}
}

type connectorCall struct {
	method   string
	path     string
	auth     string
	activity map[string]any
}

type connectorStub struct {
	status int
	body   string
	header map[string]string
}

// fakeConnector records every connector call in order and serves default
// ResourceResponses unless a call ordinal is stubbed.
type fakeConnector struct {
	server *httptest.Server

	mu     sync.Mutex
	calls  []connectorCall
	stubs  map[int]connectorStub // 1-based call ordinal → response
	nextID int
}

func newFakeConnector(t *testing.T) *fakeConnector {
	t.Helper()
	f := &fakeConnector{stubs: map[int]connectorStub{}, nextID: 1}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeConnector) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var activity map[string]any
	_ = json.Unmarshal(body, &activity)
	f.mu.Lock()
	f.calls = append(f.calls, connectorCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization"), activity: activity})
	stub, stubbed := f.stubs[len(f.calls)]
	id := fmt.Sprintf("act-%d", f.nextID)
	f.nextID++
	f.mu.Unlock()
	switch {
	case stubbed:
		for key, value := range stub.header {
			w.Header().Set(key, value)
		}
		w.WriteHeader(stub.status)
		_, _ = io.WriteString(w, stub.body)
	case r.Method == http.MethodGet:
		w.Header().Set("Content-Type", "image/png")
		_, _ = io.WriteString(w, "IMG")
	case r.Method == http.MethodPut:
		_ = json.NewEncoder(w).Encode(map[string]string{"id": path.Base(r.URL.Path)})
	default:
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
	}
}

// stubAt scripts the response for the n-th (1-based) connector call.
func (f *fakeConnector) stubAt(n int, status int, body string, header map[string]string) {
	f.mu.Lock()
	f.stubs[n] = connectorStub{status: status, body: body, header: header}
	f.mu.Unlock()
}

func (f *fakeConnector) callList() []connectorCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]connectorCall{}, f.calls...)
}

// testEnv wires an adapter to the fakes with fast, deterministic timings.
type testEnv struct {
	adapter    *Adapter
	idp        *fakeIDP
	connector  *fakeConnector
	tokenCalls *atomic.Int32

	mu     sync.Mutex
	sleeps []time.Duration
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	idp := newFakeIDP(t)
	connector := newFakeConnector(t)
	tokenCalls := &atomic.Int32{}
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := tokenCalls.Add(1)
		_, _ = fmt.Fprintf(w, `{"token_type":"Bearer","expires_in":3600,"access_token":"test-token-%d"}`, n)
	}))
	t.Cleanup(tokenServer.Close)
	adapter, err := New(Options{
		AppID:             testAppID,
		AppPassword:       "app-password",
		TokenURL:          tokenServer.URL,
		OpenIDMetadataURL: idp.metadataURL(),
		TypingInterval:    time.Hour, // no background refreshes during tests
		ChunkDelay:        time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	env := &testEnv{adapter: adapter, idp: idp, connector: connector, tokenCalls: tokenCalls}
	adapter.client.backoffBase = time.Millisecond
	adapter.client.backoffCap = 2 * time.Millisecond
	adapter.client.jitter = func(d time.Duration) time.Duration { return d }
	adapter.client.sleep = func(ctx context.Context, d time.Duration) error {
		env.mu.Lock()
		env.sleeps = append(env.sleeps, d)
		env.mu.Unlock()
		return nil
	}
	return env
}

func (env *testEnv) sleepList() []time.Duration {
	env.mu.Lock()
	defer env.mu.Unlock()
	return append([]time.Duration{}, env.sleeps...)
}

// bearer mints a valid Authorization header for serviceURL, with optional
// claim mutation.
func (env *testEnv) bearer(t *testing.T, serviceURL string, mutate func(map[string]any)) string {
	t.Helper()
	keyA, _ := testKeys(t)
	claims := defaultClaims(serviceURL)
	if mutate != nil {
		mutate(claims)
	}
	return "Bearer " + mintToken(t, keyA, testKid, "RS256", claims)
}

// postActivity serves one webhook POST and returns the recorder.
func postActivity(t *testing.T, handler http.Handler, activity map[string]any, auth string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(activity)
	if err != nil {
		t.Fatalf("marshal activity: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/messages", bytes.NewReader(body))
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// personalActivity is a canonical inbound personal-chat message.
func personalActivity(serviceURL string) map[string]any {
	return map[string]any{
		"type":       "message",
		"id":         "1481567603816",
		"timestamp":  "2026-07-19T10:00:00.000Z",
		"serviceUrl": serviceURL,
		"channelId":  "msteams",
		"from":       map[string]any{"id": "29:1abc", "name": "Jane Smith", "aadObjectId": "6aeb"},
		"recipient":  map[string]any{"id": "28:" + testAppID, "name": "botname"},
		"conversation": map[string]any{
			"id":               "a:1personal",
			"conversationType": "personal",
			"tenantId":         "tenant-1",
		},
		"text": "hello bot",
	}
}

// channelActivity is a canonical inbound channel message mentioning the bot.
func channelActivity(serviceURL string) map[string]any {
	return map[string]any{
		"type":       "message",
		"id":         "1481567603999",
		"timestamp":  "2026-07-19T10:05:00.000Z",
		"serviceUrl": serviceURL,
		"channelId":  "msteams",
		"from":       map[string]any{"id": "29:2def", "name": "Bob"},
		"recipient":  map[string]any{"id": "28:" + testAppID, "name": "botname"},
		"conversation": map[string]any{
			"id":               "19:chan@thread.tacv2;messageid=1481567603816",
			"conversationType": "channel",
			"tenantId":         "tenant-1",
		},
		"text":      "<at>botname</at> do the thing",
		"replyToId": "1481567603816",
		"entities": []any{map[string]any{
			"type":      "mention",
			"text":      "<at>botname</at>",
			"mentioned": map[string]any{"id": "28:" + testAppID, "name": "botname"},
		}},
	}
}
