package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeOAuthServer is an MCP server behind OAuth with its own authorization
// server: protected resource metadata, RFC 8414 metadata, dynamic client
// registration, PKCE codes and refresh tokens.
type fakeOAuthServer struct {
	*httptest.Server
	mu           sync.Mutex
	challenges   map[string]string // code → PKCE challenge
	tokens       map[string]bool   // valid access tokens
	refreshes    int
	registered   []map[string]any
	issued       int
	requireScope string
}

func newFakeOAuthServer(t *testing.T) *fakeOAuthServer {
	t.Helper()
	fake := &fakeOAuthServer{challenges: map[string]string{}, tokens: map[string]bool{}}
	mcpServer := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "protected", Version: "1"}, nil)
	addTextTool(mcpServer, "ping")
	mcpHandler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return mcpServer }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	mux := http.NewServeMux()
	writeJSON := func(writer http.ResponseWriter, value any) {
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(value)
	}
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{"resource": fake.URL + "/mcp", "authorization_servers": []string{fake.URL}, "scopes_supported": []string{"read"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(writer http.ResponseWriter, _ *http.Request) {
		writeJSON(writer, map[string]any{
			"issuer": fake.URL, "authorization_endpoint": fake.URL + "/authorize", "token_endpoint": fake.URL + "/token",
			"registration_endpoint": fake.URL + "/register", "response_types_supported": []string{"code"},
			"code_challenge_methods_supported": []string{"S256"}, "authorization_response_iss_parameter_supported": true,
			"scopes_supported": nil,
		})
	})
	mux.HandleFunc("/register", func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(request.Body).Decode(&body)
		fake.mu.Lock()
		fake.registered = append(fake.registered, body)
		fake.mu.Unlock()
		writer.WriteHeader(http.StatusCreated)
		// Empty optional fields, as some servers send them.
		writeJSON(writer, map[string]any{"client_id": "client-1", "client_secret": "", "redirect_uris": body["redirect_uris"], "client_id_issued_at": nil})
	})
	mux.HandleFunc("/authorize", func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		fake.mu.Lock()
		fake.challenges["code-1"] = query.Get("code_challenge")
		fake.mu.Unlock()
		redirect, _ := url.Parse(query.Get("redirect_uri"))
		values := url.Values{"code": {"code-1"}, "state": {query.Get("state")}, "iss": {fake.URL}}
		redirect.RawQuery = values.Encode()
		http.Redirect(writer, request, redirect.String(), http.StatusFound)
	})
	mux.HandleFunc("/token", func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch request.Form.Get("grant_type") {
		case "authorization_code":
			digest := sha256.Sum256([]byte(request.Form.Get("code_verifier")))
			if fake.challenges[request.Form.Get("code")] != base64.RawURLEncoding.EncodeToString(digest[:]) || request.Form.Get("resource") != fake.URL+"/mcp" {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSON(writer, map[string]any{"error": "invalid_grant"})
				return
			}
		case "refresh_token":
			if request.Form.Get("refresh_token") != "refresh" {
				writer.WriteHeader(http.StatusBadRequest)
				writeJSON(writer, map[string]any{"error": "invalid_grant"})
				return
			}
			fake.refreshes++
		}
		fake.issued++
		token := "access-" + string(rune('0'+fake.issued))
		fake.tokens[token] = true
		writeJSON(writer, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": "3600", "refresh_token": "refresh", "scope": nil})
	})
	mux.HandleFunc("/mcp", func(writer http.ResponseWriter, request *http.Request) {
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		fake.mu.Lock()
		valid, scope := fake.tokens[token], fake.requireScope
		fake.mu.Unlock()
		if !valid {
			writer.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+fake.URL+`/.well-known/oauth-protected-resource/mcp"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		if scope != "" {
			writer.Header().Set("WWW-Authenticate", `Bearer error="insufficient_scope", scope="`+scope+`"`)
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		mcpHandler.ServeHTTP(writer, request)
	})
	fake.Server = httptest.NewServer(mux)
	t.Cleanup(fake.Close)
	return fake
}

func TestOAuthSignInConnectsAndRefreshes(t *testing.T) {
	fake := newFakeOAuthServer(t)
	agentDir, cwd := t.TempDir(), t.TempDir()
	entry := Entry{Name: "protected", Scope: "global", Config: ServerConfig{URL: fake.URL + "/mcp", Exposure: ExposureDirect, OAuth: &OAuthConfig{ClientName: "orb-test"}}}
	follow := func(target string) {
		response, err := http.Get(target)
		if err != nil {
			t.Error(err)
			return
		}
		_ = response.Body.Close()
	}
	never := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", nil
	}

	statuses := Probe(context.Background(), cwd, agentDir, []Entry{entry})
	if statuses[0].State != ServerNeedsAuth {
		t.Fatalf("unsigned status = %#v", statuses[0])
	}
	tools, already, err := Login(context.Background(), cwd, agentDir, entry, follow, never)
	if err != nil || already || tools != 1 {
		t.Fatalf("login: tools %d, already %v, err %v", tools, already, err)
	}
	if len(fake.registered) != 1 || fake.registered[0]["client_name"] != "orb-test" {
		t.Fatalf("registration = %#v", fake.registered)
	}
	data, _ := os.ReadFile(filepath.Join(agentDir, "mcp-auth.json"))
	var stored map[string]oauthState
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	state := stored["mcp__protected|"+fake.URL+"/mcp"]
	if state.Tokens == nil || state.Tokens.Scope != "read" || state.TokensExpireAt == nil || state.ClientInformation.ClientID != "client-1" {
		t.Fatalf("stored state = %s", data)
	}
	if _, already, err := Login(context.Background(), cwd, agentDir, entry, follow, never); err != nil || !already {
		t.Fatalf("second login: already %v, err %v", already, err)
	}

	// A rejected token is refreshed once and the request retried.
	fake.mu.Lock()
	fake.tokens = map[string]bool{}
	fake.mu.Unlock()
	if statuses := Probe(context.Background(), cwd, agentDir, []Entry{entry}); statuses[0].State != ServerConnected || fake.refreshes != 1 {
		t.Fatalf("after refresh: %#v, refreshes %d", statuses[0], fake.refreshes)
	}

	// More scope needs a new sign-in that keeps the granted scope.
	fake.mu.Lock()
	fake.requireScope = "write"
	fake.mu.Unlock()
	manager := newManager(agentDir, nil)
	manager.configure(cwd, []Entry{entry})
	defer func() { _ = manager.Close() }()
	if err := manager.connectServer(context.Background(), "protected", false); err == nil || manager.statusOf("protected").State != ServerNeedsAuth {
		t.Fatalf("step-up: %v, %#v", err, manager.statusOf("protected"))
	}
	if manager.servers["protected"].challenge == nil || manager.servers["protected"].challenge.Scope != "write" {
		t.Fatalf("challenge = %#v", manager.servers["protected"].challenge)
	}

	if removed, err := RemoveCredentials(agentDir, "protected", entry.Config.URL); err != nil || !removed {
		t.Fatalf("logout: %v, %v", removed, err)
	}
}

func TestProviderAuthSendsTheProviderToken(t *testing.T) {
	server := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "keyed", Version: "1"}, nil)
	addTextTool(server, "ping")
	handler := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return server }, &mcpsdk.StreamableHTTPOptions{JSONResponse: true})
	httpServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer provider-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(writer, request)
	}))
	defer httpServer.Close()
	manager := newManager(t.TempDir(), nil)
	manager.configure(t.TempDir(), []Entry{{Name: "keyed", Config: ServerConfig{URL: httpServer.URL, Auth: &ProviderAuth{Provider: "anthropic"}}}})
	defer func() { _ = manager.Close() }()
	if err := manager.connectServer(context.Background(), "keyed", false); err == nil || !strings.Contains(err.Error(), "only available in a session") {
		t.Fatalf("without a registry: %v", err)
	}
	manager.providerToken = func(_ context.Context, provider string) (string, error) {
		if provider != "anthropic" {
			t.Errorf("provider = %q", provider)
		}
		return "provider-token", nil
	}
	if err := manager.connectServer(context.Background(), "keyed", true); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialStoreTakesOverLegacyEntriesForItsURLOnly(t *testing.T) {
	agentDir := t.TempDir()
	store := newCredentialStore(agentDir)
	legacy := `{"https://example.com/":{"serverUrl":"https://example.com/","tokens":{"access_token":"a","token_type":"Bearer"}}}`
	if err := os.WriteFile(store.path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.load("docs", "https://EXAMPLE.com")
	if err != nil || state.Tokens == nil || state.Tokens.AccessToken != "a" {
		t.Fatalf("legacy state = %#v, %v", state, err)
	}
	if state, _ := store.load("other", "https://example.com"); state.Tokens != nil {
		t.Fatalf("another server took the moved state: %#v", state)
	}
	data, _ := os.ReadFile(store.path)
	if !strings.Contains(string(data), `"mcp__docs|https://example.com/"`) {
		t.Fatalf("store = %s", data)
	}
}

func TestParseChallengeAndCallbackSettings(t *testing.T) {
	got := parseChallenge(`Bearer error="insufficient_scope", scope="a b", resource_metadata="https://x/.well-known/oauth-protected-resource"`)
	if got.Error != "insufficient_scope" || got.Scope != "a b" || got.ResourceMetadataURL != "https://x/.well-known/oauth-protected-resource" {
		t.Fatalf("challenge = %#v", got)
	}
	if got := parseChallenge(`Basic realm="x"`); got != (challenge{}) {
		t.Fatalf("basic challenge = %#v", got)
	}
	host, redirectHost, path, port, fixed := callbackSettings(OAuthConfig{CallbackURL: "http://localhost/cb", CallbackPort: 4567})
	if host != "127.0.0.1" || redirectHost != "localhost" || path != "/cb" || port != 4567 || fixed != "http://localhost:4567/cb" {
		t.Fatalf("callback = %q %q %q %d %q", host, redirectHost, path, port, fixed)
	}
	if got := stepUpScope(oauthState{Tokens: &oauthTokens{Scope: "read"}}, challenge{Error: "insufficient_scope", Scope: "write"}); got != "read write" {
		t.Fatalf("step-up scope = %q", got)
	}
}
