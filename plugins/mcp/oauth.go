package mcp

// OAuth for remote MCP servers, after pi 1.0's (itself adapted from the MCP
// TypeScript SDK). Connections never open a browser: they send the stored
// access token and, after a 401, try the stored refresh token; otherwise the
// server needs a sign-in, which /mcp login or `orb mcp login` runs with the
// authorization code flow (PKCE, dynamic client registration) against a
// loopback callback. Credentials live in mcp-auth.json in the agent directory,
// keyed by server name and URL, in the shape pi writes.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	configpkg "github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/ai/auth/oauth"
	"github.com/OrdalieTech/orb/internal/filelock"
)

const (
	callbackHost    = "127.0.0.1"
	callbackPath    = "/callback"
	refreshSkew     = 30 * time.Second
	refreshTimeout  = 15 * time.Second
	protocolVersion = "2025-11-25"
)

// errSignInRequired means the server needs the user to sign in, or to sign
// in again for more scope.
var errSignInRequired = errors.New("sign-in required")

// errSignInCancelled ends a sign-in the user abandoned.
var errSignInCancelled = errors.New("sign-in cancelled")

type oauthTokens struct {
	AccessToken  string   `json:"access_token"`
	TokenType    string   `json:"token_type"`
	ExpiresIn    *seconds `json:"expires_in,omitempty"`
	Scope        string   `json:"scope,omitempty"`
	RefreshToken string   `json:"refresh_token,omitempty"`
	IDToken      string   `json:"id_token,omitempty"`
}

// seconds accepts a number, a numeric string or null, as servers send all three.
type seconds float64

func (value *seconds) UnmarshalJSON(data []byte) error {
	text := strings.Trim(string(data), `"`)
	if text == "null" || text == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return errors.New("invalid expires_in")
	}
	*value = seconds(parsed)
	return nil
}

type oauthClient struct {
	ClientID                string   `json:"client_id"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	RedirectURIs            []string `json:"redirect_uris,omitempty"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method,omitempty"`
}

type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
	ISSParameterSupported             bool     `json:"authorization_response_iss_parameter_supported,omitempty"`
}

type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers,omitempty"`
	ScopesSupported      []string `json:"scopes_supported,omitempty"`
}

type discoveryState struct {
	AuthorizationServerURL      string              `json:"authorizationServerUrl"`
	AuthorizationServerMetadata *authServerMetadata `json:"authorizationServerMetadata,omitempty"`
	ResourceMetadata            *resourceMetadata   `json:"resourceMetadata,omitempty"`
	ResourceMetadataURL         string              `json:"resourceMetadataUrl,omitempty"`
}

// oauthState is one server's entry in mcp-auth.json.
type oauthState struct {
	ServerURL         string          `json:"serverUrl"`
	ClientInformation *oauthClient    `json:"clientInformation,omitempty"`
	Tokens            *oauthTokens    `json:"tokens,omitempty"`
	TokensExpireAt    *int64          `json:"tokensExpireAt,omitempty"`
	CodeVerifier      string          `json:"codeVerifier,omitempty"`
	OAuthState        string          `json:"oauthState,omitempty"`
	Discovery         *discoveryState `json:"discovery,omitempty"`
}

// challenge is a server's WWW-Authenticate challenge.
type challenge struct {
	ResourceMetadataURL, Scope, Error string
}

func parseChallenge(header string) challenge {
	fields := strings.Fields(header)
	if len(fields) == 0 || !strings.EqualFold(fields[0], "bearer") && !strings.EqualFold(fields[0], "dpop") {
		return challenge{}
	}
	field := func(name string) string {
		// An empty value carries no information, so it counts as absent.
		match := regexp.MustCompile(`(?i)(?:^|[,\s])` + name + `=(?:"([^"]*)"|([^\s,]+))`).FindStringSubmatch(header)
		if match == nil {
			return ""
		}
		return match[1] + match[2]
	}
	result := challenge{Scope: field("scope"), Error: field("error")}
	if value := field("resource_metadata"); value != "" {
		if _, err := url.Parse(value); err == nil {
			result.ResourceMetadataURL = value
		}
	}
	return result
}

// credentialStore is mcp-auth.json.
type credentialStore struct {
	path string
}

func newCredentialStore(agentDir string) credentialStore {
	return credentialStore{path: filepath.Join(agentDir, "mcp-auth.json")}
}

// normalizeServerURL is the server URL as pi keys it: bare origins end in /.
func normalizeServerURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return value
	}
	parsed.Scheme, parsed.Host = strings.ToLower(parsed.Scheme), strings.ToLower(parsed.Host)
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String()
}

func credentialKeys(name, serverURL string) (key, legacy string) {
	legacy = normalizeServerURL(serverURL)
	return Namespace(name) + "|" + legacy, legacy
}

// edit runs change on the stored states under the file lock and saves them
// when it reports a change.
func (store credentialStore) edit(change func(map[string]*oauthState) bool) error {
	return filelock.File{Path: store.path, Perm: 0o600}.Update(context.Background(), func(data []byte) ([]byte, error) {
		states := map[string]*oauthState{}
		if len(bytes.TrimSpace(data)) > 0 {
			if err := json.Unmarshal(data, &states); err != nil {
				return nil, fmt.Errorf("%s: %w", store.path, err)
			}
		}
		if !change(states) {
			return data, nil
		}
		encoded, err := json.MarshalIndent(states, "", "  ")
		return append(encoded, '\n'), err
	})
}

// load returns a server's state, taking over pi's legacy entry keyed by URL
// alone. State stored for another URL is never returned.
func (store credentialStore) load(name, serverURL string) (oauthState, error) {
	key, legacy := credentialKeys(name, serverURL)
	var state *oauthState
	err := store.edit(func(states map[string]*oauthState) bool {
		state = states[key]
		if state != nil || states[legacy] == nil {
			return false
		}
		state, states[key] = states[legacy], states[legacy]
		delete(states, legacy)
		return true
	})
	if state == nil || state.ServerURL != normalizeServerURL(serverURL) {
		return oauthState{ServerURL: normalizeServerURL(serverURL)}, err
	}
	return *state, err
}

func (store credentialStore) update(name, serverURL string, change func(*oauthState)) error {
	key, _ := credentialKeys(name, serverURL)
	return store.edit(func(states map[string]*oauthState) bool {
		state := states[key]
		if state == nil || state.ServerURL != normalizeServerURL(serverURL) {
			state = &oauthState{ServerURL: normalizeServerURL(serverURL)}
		}
		change(state)
		states[key] = state
		return true
	})
}

// remove deletes a server's credentials and reports whether there were any.
func (store credentialStore) remove(name, serverURL string) (bool, error) {
	key, legacy := credentialKeys(name, serverURL)
	removed := false
	err := store.edit(func(states map[string]*oauthState) bool {
		for _, candidate := range []string{key, legacy} {
			if _, ok := states[candidate]; ok {
				delete(states, candidate)
				removed = true
				return true
			}
		}
		return false
	})
	return removed, err
}

// withRefreshLock keeps other processes from refreshing the same server's
// tokens: many servers rotate refresh tokens, so two refreshes lose the grant.
func (store credentialStore) withRefreshLock(name, serverURL string, run func() error) error {
	key, _ := credentialKeys(name, serverURL)
	digest := sha256.Sum256([]byte(key))
	release, err := filelock.Acquire(filepath.Join(filepath.Dir(store.path), "mcp-auth-refresh-"+hex.EncodeToString(digest[:8])))
	if err != nil {
		return err
	}
	defer func() { _ = release() }()
	return run()
}

// oauthFlow is one server's authorization as pi's runFlow performs it.
type oauthFlow struct {
	name, serverURL string
	store           credentialStore
	// settings has the client secret resolved.
	settings    OAuthConfig
	redirectURL string
	client      *http.Client
	// onRedirect receives the authorization URL when the user must sign in.
	onRedirect func(string)
}

type flowOptions struct {
	code, iss, scope, resourceMetadataURL string
	skipRefresh                           bool
}

func (flow oauthFlow) clientMetadata() map[string]any {
	method := "none"
	if flow.settings.ClientSecret != "" {
		method = "client_secret_post"
	}
	return map[string]any{
		"client_name": cmpOrDefault(flow.settings.ClientName, "orb"), "redirect_uris": []string{flow.redirectURL},
		"grant_types": []string{"authorization_code", "refresh_token"}, "response_types": []string{"code"},
		"token_endpoint_auth_method": method,
	}
}

func cmpOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (flow oauthFlow) update(change func(*oauthState)) error {
	return flow.store.update(flow.name, flow.serverURL, change)
}

// authorize refreshes or exchanges, or reports the redirect the user must
// follow (authorized false). Rejected clients are registered again and
// rejected grants dropped, once.
func (flow oauthFlow) authorize(ctx context.Context, options flowOptions) (bool, error) {
	authorized, err := flow.run(ctx, options)
	var oauthErr *oauthError
	if errors.As(err, &oauthErr) {
		switch oauthErr.code {
		case "invalid_client", "unauthorized_client":
			_ = flow.update(func(state *oauthState) { *state = oauthState{ServerURL: state.ServerURL} })
			return flow.run(ctx, options)
		case "invalid_grant":
			_ = flow.update(func(state *oauthState) { state.Tokens, state.TokensExpireAt = nil, nil })
			return flow.run(ctx, options)
		}
	}
	return authorized, err
}

func (flow oauthFlow) run(ctx context.Context, options flowOptions) (bool, error) {
	state, err := flow.store.load(flow.name, flow.serverURL)
	if err != nil {
		return false, err
	}
	var discovered discoveryState
	switch {
	case flow.settings.AuthServerMetadataURL != "":
		// A configured metadata document is trusted as configured and not cached.
		discovered.ResourceMetadata = flow.protectedResourceMetadata(ctx, options.resourceMetadataURL)
		metadata, err := flow.fetchAuthServerMetadata(ctx, flow.settings.AuthServerMetadataURL)
		if err != nil {
			return false, err
		}
		if metadata == nil {
			return false, fmt.Errorf("no authorization server metadata at %s", flow.settings.AuthServerMetadataURL)
		}
		discovered.AuthorizationServerURL, discovered.AuthorizationServerMetadata = metadata.Issuer, metadata
	case state.Discovery != nil && state.Discovery.AuthorizationServerURL != "":
		discovered = *state.Discovery
		if discovered.AuthorizationServerMetadata == nil {
			if discovered.AuthorizationServerMetadata, err = flow.discoverAuthServerMetadata(ctx, discovered.AuthorizationServerURL); err != nil {
				return false, err
			}
		}
	default:
		discovered.ResourceMetadata = flow.protectedResourceMetadata(ctx, options.resourceMetadataURL)
		discovered.AuthorizationServerURL = rootURL(flow.serverURL)
		if discovered.ResourceMetadata != nil && len(discovered.ResourceMetadata.AuthorizationServers) > 0 {
			discovered.AuthorizationServerURL = discovered.ResourceMetadata.AuthorizationServers[0]
		}
		if discovered.AuthorizationServerMetadata, err = flow.discoverAuthServerMetadata(ctx, discovered.AuthorizationServerURL); err != nil {
			return false, err
		}
		discovered.ResourceMetadataURL = options.resourceMetadataURL
		if err := flow.update(func(state *oauthState) { state.Discovery = &discovered }); err != nil {
			return false, err
		}
	}
	metadata := discovered.AuthorizationServerMetadata
	resource, err := selectResource(flow.serverURL, discovered.ResourceMetadata)
	if err != nil {
		return false, err
	}
	scope := options.scope
	if scope == "" && discovered.ResourceMetadata != nil {
		scope = strings.Join(discovered.ResourceMetadata.ScopesSupported, " ")
	}
	client := state.ClientInformation
	if flow.settings.ClientID != "" {
		client = &oauthClient{ClientID: flow.settings.ClientID, ClientSecret: flow.settings.ClientSecret}
	}
	if client == nil {
		if options.code != "" {
			return false, errors.New("OAuth client information is missing during code exchange")
		}
		if client, err = flow.register(ctx, discovered.AuthorizationServerURL, metadata, scope); err != nil {
			return false, err
		}
		if err := flow.update(func(state *oauthState) { state.ClientInformation = client }); err != nil {
			return false, err
		}
	}
	tokenEndpoint := endpoint(metadata, discovered.AuthorizationServerURL, "/token", func(metadata *authServerMetadata) string { return metadata.TokenEndpoint })
	if options.code != "" {
		// RFC 9207: never send a code from another authorization server to this one.
		if metadata != nil && (options.iss != "" || metadata.ISSParameterSupported) && options.iss != metadata.Issuer {
			return false, fmt.Errorf("authorization response issuer %q does not match %q", options.iss, metadata.Issuer)
		}
		if state.CodeVerifier == "" {
			return false, errors.New("no OAuth PKCE code verifier is stored")
		}
		tokens, err := flow.tokenRequest(ctx, tokenEndpoint, metadata, client, resource, url.Values{
			"grant_type": {"authorization_code"}, "code": {options.code}, "code_verifier": {state.CodeVerifier}, "redirect_uri": {flow.redirectURL},
		})
		if err != nil {
			return false, err
		}
		// A response without scope grants the requested scope (RFC 6749 §5.1).
		return true, flow.saveTokens(tokens, scope)
	}
	if existing := state.Tokens; !options.skipRefresh && existing != nil && existing.RefreshToken != "" {
		tokens, err := flow.tokenRequest(ctx, tokenEndpoint, metadata, client, resource, url.Values{
			"grant_type": {"refresh_token"}, "refresh_token": {existing.RefreshToken},
		})
		if err == nil {
			tokens.RefreshToken = cmpOrDefault(tokens.RefreshToken, existing.RefreshToken)
			// A refresh without scope keeps the grant's (RFC 6749 §6).
			return true, flow.saveTokens(tokens, existing.Scope)
		}
		var oauthErr *oauthError
		if errors.As(err, &oauthErr) && oauthErr.code != "server_error" {
			return false, err
		}
	}
	if metadata != nil && !slices.Contains(metadata.ResponseTypesSupported, "code") {
		return false, errors.New("authorization server does not support authorization codes")
	}
	if metadata != nil && len(metadata.CodeChallengeMethodsSupported) > 0 && !slices.Contains(metadata.CodeChallengeMethodsSupported, "S256") {
		return false, errors.New("authorization server does not support PKCE S256")
	}
	verifier, codeChallenge, err := oauth.GeneratePKCE(nil)
	if err != nil {
		return false, err
	}
	authorization, err := url.Parse(endpoint(metadata, discovered.AuthorizationServerURL, "/authorize", func(metadata *authServerMetadata) string { return metadata.AuthorizationEndpoint }))
	if err != nil {
		return false, err
	}
	query := authorization.Query()
	query.Set("response_type", "code")
	query.Set("client_id", client.ClientID)
	query.Set("code_challenge", codeChallenge)
	query.Set("code_challenge_method", "S256")
	query.Set("redirect_uri", flow.redirectURL)
	if state.OAuthState != "" {
		query.Set("state", state.OAuthState)
	}
	if scope != "" {
		query.Set("scope", scope)
		if slices.Contains(strings.Fields(scope), "offline_access") {
			query.Set("prompt", "consent")
		}
	}
	if resource != "" {
		query.Set("resource", resource)
	}
	authorization.RawQuery = query.Encode()
	if err := flow.update(func(state *oauthState) { state.CodeVerifier = verifier }); err != nil {
		return false, err
	}
	flow.onRedirect(authorization.String())
	return false, nil
}

func (flow oauthFlow) saveTokens(tokens *oauthTokens, scope string) error {
	if tokens.Scope == "" {
		tokens.Scope = scope
	}
	return flow.update(func(state *oauthState) {
		state.Tokens, state.TokensExpireAt = tokens, nil
		if tokens.ExpiresIn != nil {
			expires := time.Now().Add(time.Duration(float64(*tokens.ExpiresIn) * float64(time.Second))).UnixMilli()
			state.TokensExpireAt = &expires
		}
	})
}

func endpoint(metadata *authServerMetadata, server, path string, pick func(*authServerMetadata) string) string {
	if metadata != nil {
		return pick(metadata)
	}
	base, err := url.Parse(server)
	if err != nil {
		return server + path
	}
	return base.ResolveReference(&url.URL{Path: path}).String()
}

func rootURL(serverURL string) string {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return serverURL
	}
	return parsed.Scheme + "://" + parsed.Host + "/"
}

func secureEndpoint(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return fmt.Errorf("refusing insecure OAuth endpoint %s", value)
	}
	return nil
}

func (flow oauthFlow) getJSON(ctx context.Context, target string) (*http.Response, []byte, error) {
	return flow.send(ctx, http.MethodGet, target, nil, http.Header{"Accept": {"application/json"}, "Mcp-Protocol-Version": {protocolVersion}})
}

// send makes one request and reads up to 1 MiB of the response; the response
// comes back with a read error too.
func (flow oauthFlow) send(ctx context.Context, method, target string, body []byte, header http.Header) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header = header
	response, err := flow.client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	return response, contents, err
}

// discoveryMiss is a status that means "not here": 4xx and 502.
func discoveryMiss(status int) bool {
	return status >= 400 && status < 500 || status == http.StatusBadGateway
}

// protectedResourceMetadata is the server's RFC 9728 metadata, or nil.
func (flow oauthFlow) protectedResourceMetadata(ctx context.Context, configured string) *resourceMetadata {
	server, err := url.Parse(flow.serverURL)
	if err != nil {
		return nil
	}
	candidates := []string{configured}
	if configured == "" {
		path := strings.TrimSuffix(server.Path, "/")
		candidates = []string{server.Scheme + "://" + server.Host + "/.well-known/oauth-protected-resource" + path}
		if path != "" {
			candidates = append(candidates, server.Scheme+"://"+server.Host+"/.well-known/oauth-protected-resource")
		}
	}
	for _, candidate := range candidates {
		response, body, err := flow.getJSON(ctx, candidate)
		if err != nil {
			return nil
		}
		if response.StatusCode == http.StatusOK {
			var metadata resourceMetadata
			if json.Unmarshal(body, &metadata) != nil || metadata.Resource == "" {
				return nil
			}
			return &metadata
		}
		if !discoveryMiss(response.StatusCode) {
			return nil
		}
	}
	return nil
}

// discoverAuthServerMetadata tries RFC 8414 and OpenID discovery; nil when
// the server publishes none.
func (flow oauthFlow) discoverAuthServerMetadata(ctx context.Context, server string) (*authServerMetadata, error) {
	issuer, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	origin, path := issuer.Scheme+"://"+issuer.Host, strings.TrimSuffix(issuer.Path, "/")
	candidates := []string{origin + "/.well-known/oauth-authorization-server" + path, origin + "/.well-known/openid-configuration" + path}
	if path != "" {
		candidates = append(candidates, origin+path+"/.well-known/openid-configuration")
	}
	for _, candidate := range candidates {
		metadata, err := flow.fetchAuthServerMetadata(ctx, candidate)
		if err != nil {
			return nil, err
		}
		if metadata == nil {
			continue
		}
		if strings.TrimSuffix(metadata.Issuer, "/") != strings.TrimSuffix(server, "/") {
			return nil, fmt.Errorf("authorization server issuer %q does not match %q", metadata.Issuer, server)
		}
		return metadata, nil
	}
	return nil, nil
}

func (flow oauthFlow) fetchAuthServerMetadata(ctx context.Context, target string) (*authServerMetadata, error) {
	response, body, err := flow.getJSON(ctx, target)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		if discoveryMiss(response.StatusCode) {
			return nil, nil
		}
		return nil, fmt.Errorf("HTTP %d loading authorization server metadata from %s", response.StatusCode, target)
	}
	var metadata authServerMetadata
	if err := json.Unmarshal(body, &metadata); err != nil {
		return nil, fmt.Errorf("invalid authorization server metadata from %s: %w", target, err)
	}
	if metadata.Issuer == "" || metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" || metadata.ResponseTypesSupported == nil {
		return nil, fmt.Errorf("incomplete authorization server metadata from %s", target)
	}
	for _, value := range []string{metadata.Issuer, metadata.AuthorizationEndpoint, metadata.TokenEndpoint, metadata.RegistrationEndpoint} {
		if parsed, err := url.Parse(value); value != "" && (err != nil || parsed.Scheme != "https" && parsed.Scheme != "http") {
			return nil, fmt.Errorf("invalid URL %q in authorization server metadata", value)
		}
	}
	return &metadata, nil
}

// selectResource is the RFC 8707 resource for the server: the protected
// resource's identifier, which must contain the server URL.
func selectResource(serverURL string, metadata *resourceMetadata) (string, error) {
	if metadata == nil {
		return "", nil
	}
	requested, err := url.Parse(serverURL)
	configured, configuredErr := url.Parse(metadata.Resource)
	if err != nil || configuredErr != nil || !strings.EqualFold(requested.Scheme+"://"+requested.Host, configured.Scheme+"://"+configured.Host) ||
		!strings.HasPrefix(strings.TrimSuffix(requested.Path, "/")+"/", strings.TrimSuffix(configured.Path, "/")+"/") {
		return "", fmt.Errorf("protected resource %s does not match MCP server %s", metadata.Resource, serverURL)
	}
	return metadata.Resource, nil
}

func (flow oauthFlow) register(ctx context.Context, server string, metadata *authServerMetadata, scope string) (*oauthClient, error) {
	if metadata != nil && metadata.RegistrationEndpoint == "" {
		return nil, errors.New("authorization server does not support dynamic client registration; set oauth.clientId")
	}
	body := flow.clientMetadata()
	if scope != "" {
		body["scope"] = scope
	}
	data, _ := json.Marshal(body)
	target := endpoint(metadata, server, "/register", func(metadata *authServerMetadata) string { return metadata.RegistrationEndpoint })
	response, answer, err := flow.send(ctx, http.MethodPost, target, data, http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}})
	if response == nil {
		return nil, err
	}
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("client registration failed: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(answer)))
	}
	var client oauthClient
	if err := json.Unmarshal(answer, &client); err != nil || client.ClientID == "" {
		return nil, fmt.Errorf("invalid client registration response: %s", strings.TrimSpace(string(answer)))
	}
	return &client, nil
}

type oauthError struct{ code, description string }

func (err *oauthError) Error() string { return err.description }

func (flow oauthFlow) tokenRequest(ctx context.Context, target string, metadata *authServerMetadata, client *oauthClient, resource string, params url.Values) (*oauthTokens, error) {
	if err := secureEndpoint(target); err != nil {
		return nil, err
	}
	if resource != "" {
		params.Set("resource", resource)
	}
	var supported []string
	if metadata != nil {
		supported = metadata.TokenEndpointAuthMethodsSupported
	}
	method := clientAuthMethod(client, supported)
	if method != "client_secret_basic" {
		params.Set("client_id", client.ClientID)
		if method == "client_secret_post" {
			params.Set("client_secret", client.ClientSecret)
		}
	}
	header := http.Header{"Accept": {"application/json"}, "Content-Type": {"application/x-www-form-urlencoded"}}
	if method == "client_secret_basic" {
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(client.ClientID+":"+client.ClientSecret)))
	}
	response, body, err := flow.send(ctx, http.MethodPost, target, []byte(params.Encode()), header)
	if response == nil {
		return nil, err
	}
	// Servers report OAuth errors with any status, so the body comes first.
	var failure struct {
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if json.Unmarshal(body, &failure) == nil && failure.Error != "" {
		return nil, &oauthError{code: failure.Error, description: cmpOrDefault(failure.Description, failure.Error)}
	}
	if response.StatusCode/100 != 2 {
		return nil, &oauthError{code: "server_error", description: fmt.Sprintf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))}
	}
	var tokens oauthTokens
	if err := json.Unmarshal(body, &tokens); err != nil || tokens.AccessToken == "" || tokens.TokenType == "" {
		return nil, fmt.Errorf("invalid OAuth token response: %s", strings.TrimSpace(string(body)))
	}
	return &tokens, nil
}

func clientAuthMethod(client *oauthClient, supported []string) string {
	if hinted := client.TokenEndpointAuthMethod; slices.Contains([]string{"client_secret_basic", "client_secret_post", "none"}, hinted) &&
		(len(supported) == 0 || slices.Contains(supported, hinted)) {
		return hinted
	}
	secret := client.ClientSecret != ""
	switch {
	case len(supported) == 0 && secret:
		return "client_secret_basic"
	case len(supported) == 0:
		return "none"
	case secret && slices.Contains(supported, "client_secret_basic"):
		return "client_secret_basic"
	case secret && slices.Contains(supported, "client_secret_post"):
		return "client_secret_post"
	case slices.Contains(supported, "none"):
		return "none"
	case secret:
		return "client_secret_post"
	}
	return "none"
}

// callbackSettings is where the loopback callback listens and the redirect
// URI it serves; fixed is set when the port is.
func callbackSettings(settings OAuthConfig) (host, redirectHost, path string, port int, fixed string) {
	configured := settings.CallbackURL
	if configured == "" {
		configured = "http://" + callbackHost + callbackPath
	}
	parsed, _ := url.Parse(configured)
	redirectHost = parsed.Hostname()
	host = redirectHost
	if host == "localhost" {
		host = callbackHost
	}
	path = cmpOrDefault(parsed.Path, "/")
	port = settings.CallbackPort
	if parsed.Port() != "" {
		port, _ = strconv.Atoi(parsed.Port())
		// A configured URI with a port is sent as written: servers compare it as a string.
		fixed = settings.CallbackURL
	} else if port != 0 {
		parsed.Host = joinHostPort(parsed.Hostname(), port)
		fixed = parsed.String()
	}
	return host, redirectHost, path, port, fixed
}

func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return host + ":" + strconv.Itoa(port)
}

// signInPrompt shows the authorization URL and asks for the redirect URL
// when the browser cannot reach the loopback callback; paste returns "" when
// the user cancels and ends when ctx does.
type signInPrompt struct {
	show  func(authorizationURL string)
	paste func(ctx context.Context) (string, error)
}

// signIn runs the browser sign-in for a server and saves its tokens.
func signIn(ctx context.Context, flow oauthFlow, stepUp *challenge, prompt signInPrompt) error {
	stored, err := flow.store.load(flow.name, flow.serverURL)
	if err != nil {
		return err
	}
	host, redirectHost, path, port, fixed := callbackSettings(flow.settings)
	required := port != 0
	if !required && stored.ClientInformation != nil && len(stored.ClientInformation.RedirectURIs) > 0 {
		// Reuse the registered redirect port so the registered client stays valid.
		if registered, err := url.Parse(stored.ClientInformation.RedirectURIs[0]); err == nil {
			port, _ = strconv.Atoi(registered.Port())
		}
	}
	stateBytes := make([]byte, 32)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)
	scope := mergeScopes(flow.settings.Scope)
	if stepUp != nil {
		scope = mergeScopes(flow.settings.Scope, stepUpScope(stored, *stepUp))
	}
	options := flowOptions{scope: scope, skipRefresh: stepUp != nil && stepUp.Error == "insufficient_scope"}
	if stepUp != nil {
		options.resourceMetadataURL = stepUp.ResourceMetadataURL
	}
	exchange := func(query url.Values) error {
		options := options
		options.code, options.iss = query.Get("code"), query.Get("iss")
		_, err := flow.authorize(ctx, options)
		return err
	}
	listen := func(port int) (*oauth.Loopback, error) {
		return oauth.ListenLoopback(oauth.LoopbackOptions{
			Provider: "MCP server " + flow.name, Host: host, RedirectHost: redirectHost, Path: path, Port: port, State: state, Complete: exchange,
		})
	}
	loopback, err := listen(port)
	if err != nil && !required && port != 0 {
		loopback, err = listen(0)
	}
	if err != nil {
		return err
	}
	defer loopback.Close()
	flow.redirectURL = cmpOrDefault(fixed, loopback.RedirectURI())
	err = flow.update(func(state *oauthState) {
		// A registered client cannot use another redirect URI, and its tokens belong to it.
		if flow.settings.ClientID == "" && (state.ClientInformation == nil || !slices.Contains(state.ClientInformation.RedirectURIs, flow.redirectURL)) {
			state.ClientInformation, state.Tokens, state.TokensExpireAt = nil, nil, nil
		}
	})
	if err != nil {
		return err
	}
	// Every sign-in gets a fresh state parameter.
	if err := flow.update(func(stored *oauthState) { stored.OAuthState = state }); err != nil {
		return err
	}
	var authorizationURL string
	flow.onRedirect = func(target string) { authorizationURL = target }
	authorized, err := flow.authorize(ctx, options)
	if err != nil || authorized {
		return err
	}
	prompt.show(authorizationURL)
	pasteCtx, cancelPaste := context.WithCancel(ctx)
	defer cancelPaste()
	pasted := make(chan string, 1)
	go func() {
		input, _ := prompt.paste(pasteCtx)
		pasted <- input
	}()
	callback := make(chan error, 1)
	go func() { callback <- loopback.Wait(pasteCtx) }()
	select {
	case err := <-callback:
		return err
	case input := <-pasted:
		// A callback already being completed wins over the paste.
		if !loopback.Cancel() {
			return <-callback
		}
		if strings.TrimSpace(input) == "" {
			return errSignInCancelled
		}
		query, err := redirectQuery(input, state)
		if err != nil {
			return err
		}
		return exchange(query)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func redirectQuery(input, state string) (url.Values, error) {
	parsed, err := url.Parse(strings.TrimSpace(input))
	if err != nil || parsed.Scheme == "" {
		return nil, errors.New("expected the full redirect URL from the browser address bar")
	}
	query := parsed.Query()
	if failure := query.Get("error"); failure != "" {
		return nil, errors.New(cmpOrDefault(query.Get("error_description"), failure))
	}
	if query.Get("state") != state {
		return nil, errors.New("the redirect URL belongs to a different sign-in")
	}
	if query.Get("code") == "" {
		return nil, errors.New("the redirect URL does not contain an authorization code")
	}
	return query, nil
}

func mergeScopes(scopes ...string) string {
	var merged []string
	for _, scope := range scopes {
		for _, item := range strings.Fields(scope) {
			if !slices.Contains(merged, item) {
				merged = append(merged, item)
			}
		}
	}
	return strings.Join(merged, " ")
}

// stepUpScope keeps the granted scopes when a server asks for more, since a
// challenge may list only the missing ones (SEP-2350).
func stepUpScope(stored oauthState, challenged challenge) string {
	if challenged.Error != "insufficient_scope" {
		return challenged.Scope
	}
	if challenged.Scope == "" {
		return ""
	}
	granted := ""
	if stored.Tokens != nil {
		granted = stored.Tokens.Scope
	}
	return mergeScopes(granted, challenged.Scope)
}

// oauthTransport sends a server's stored access token, refreshing it near
// expiry or after a 401; when the user has to sign in, requests fail with
// errSignInRequired.
type oauthTransport struct {
	base        http.RoundTripper
	flow        oauthFlow
	onChallenge func(challenge)
	mu          sync.Mutex
}

func (transport *oauthTransport) token(ctx context.Context) string {
	state, _ := transport.flow.store.load(transport.flow.name, transport.flow.serverURL)
	if state.Tokens == nil {
		return ""
	}
	if state.TokensExpireAt != nil && time.UnixMilli(*state.TokensExpireAt).Add(-refreshSkew).Before(time.Now()) && state.Tokens.RefreshToken != "" {
		// A failed refresh falls through: the request goes out and a 401 decides.
		_ = transport.refresh(ctx, state.Tokens.AccessToken, challenge{})
		state, _ = transport.flow.store.load(transport.flow.name, transport.flow.serverURL)
	}
	if state.Tokens == nil {
		return ""
	}
	return state.Tokens.AccessToken
}

// refresh replaces stale, the token that expired or was rejected, unless
// another request or process replaced it meanwhile.
func (transport *oauthTransport) refresh(ctx context.Context, stale string, challenged challenge) error {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.flow.store.withRefreshLock(transport.flow.name, transport.flow.serverURL, func() error {
		state, err := transport.flow.store.load(transport.flow.name, transport.flow.serverURL)
		if err != nil {
			return err
		}
		if state.Tokens != nil && state.Tokens.AccessToken != stale {
			return nil
		}
		if state.Tokens == nil || state.Tokens.RefreshToken == "" {
			return errSignInRequired
		}
		flow := transport.flow
		flow.redirectURL = "http://" + callbackHost + callbackPath
		if _, _, _, _, fixed := callbackSettings(flow.settings); fixed != "" {
			flow.redirectURL = fixed
		} else if state.ClientInformation != nil && len(state.ClientInformation.RedirectURIs) > 0 {
			flow.redirectURL = state.ClientInformation.RedirectURIs[0]
		}
		redirected := false
		flow.onRedirect = func(string) { redirected = true }
		refreshCtx, cancel := context.WithTimeout(ctx, refreshTimeout)
		defer cancel()
		if _, err := flow.authorize(refreshCtx, flowOptions{resourceMetadataURL: challenged.ResourceMetadataURL, scope: challenged.Scope}); err != nil {
			return err
		}
		if redirected {
			return errSignInRequired
		}
		return nil
	})
}

func (transport *oauthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	send := func(token string) (*http.Response, error) {
		copy := request.Clone(request.Context())
		if request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			copy.Body = body
		}
		if token != "" {
			copy.Header.Set("Authorization", "Bearer "+token)
		}
		return transport.base.RoundTrip(copy)
	}
	token := transport.token(request.Context())
	response, err := send(token)
	if err != nil || response.StatusCode != http.StatusUnauthorized && response.StatusCode != http.StatusForbidden {
		return response, err
	}
	challenged := parseChallenge(response.Header.Get("WWW-Authenticate"))
	if response.StatusCode == http.StatusForbidden && challenged.Error != "insufficient_scope" {
		return response, nil
	}
	_ = response.Body.Close()
	transport.onChallenge(challenged)
	// A refresh keeps the granted scope, so more scope needs a new sign-in.
	if challenged.Error == "insufficient_scope" || request.Body != nil && request.GetBody == nil {
		return nil, errSignInRequired
	}
	if err := transport.refresh(request.Context(), token, challenged); err != nil {
		return nil, fmt.Errorf("%w: %w", errSignInRequired, err)
	}
	response, err = send(transport.token(request.Context()))
	if err == nil && response.StatusCode == http.StatusUnauthorized {
		_ = response.Body.Close()
		return nil, errSignInRequired
	}
	return response, err
}

// UsesOAuth reports a server that signs in with OAuth: HTTP without an
// Authorization header or auth.provider.
func (config ServerConfig) UsesOAuth() bool {
	if !config.IsHTTP() || config.Auth != nil {
		return false
	}
	for name := range config.Headers {
		if strings.EqualFold(name, "Authorization") {
			return false
		}
	}
	return true
}

// oauthFlowFor is a server's flow with its client secret resolved.
func (manager *Manager) oauthFlowFor(name string, config ServerConfig) (oauthFlow, error) {
	settings := OAuthConfig{}
	if config.OAuth != nil {
		settings = *config.OAuth
	}
	if settings.ClientSecret != "" {
		secret, err := configpkg.ResolveConfigValueOrThrow(settings.ClientSecret, fmt.Sprintf("MCP server %q oauth.clientSecret", name), nil)
		if err != nil {
			return oauthFlow{}, err
		}
		settings.ClientSecret = secret
	}
	return oauthFlow{name: name, serverURL: config.URL, store: newCredentialStore(manager.agentDir), settings: settings, client: http.DefaultClient}, nil
}

// authTransport adds a server's authorization: the token of its auth.provider,
// or its stored OAuth credentials.
func (manager *Manager) authTransport(name string, config ServerConfig, base http.RoundTripper) (http.RoundTripper, error) {
	if config.Auth != nil {
		manager.mu.Lock()
		token := manager.providerToken
		manager.mu.Unlock()
		return providerTransport{base: base, provider: config.Auth.Provider, token: token}, nil
	}
	if !config.UsesOAuth() {
		return base, nil
	}
	flow, err := manager.oauthFlowFor(name, config)
	if err != nil {
		return nil, err
	}
	return &oauthTransport{base: base, flow: flow, onChallenge: func(challenged challenge) {
		manager.mu.Lock()
		if connection := manager.servers[name]; connection != nil {
			connection.challenge = &challenged
		}
		manager.mu.Unlock()
	}}, nil
}

// providerTransport sends the token of an `orb login` provider.
type providerTransport struct {
	base     http.RoundTripper
	provider string
	token    func(context.Context, string) (string, error)
}

func (transport providerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport.token == nil {
		return nil, fmt.Errorf("auth.provider %q is only available in a session", transport.provider)
	}
	token, err := transport.token(request.Context(), transport.provider)
	if err != nil || token == "" {
		return nil, errors.Join(fmt.Errorf("no credentials for %q; run /login %s", transport.provider, transport.provider), err)
	}
	copy := request.Clone(request.Context())
	copy.Header.Set("Authorization", "Bearer "+token)
	return transport.base.RoundTrip(copy)
}

// storedTokens are a server's stored tokens as text, to compare later.
func (manager *Manager) storedTokens(name string, config ServerConfig) string {
	state, _ := newCredentialStore(manager.agentDir).load(name, config.URL)
	data, _ := json.Marshal(state.Tokens)
	return string(data)
}

// signedInElsewhere are servers waiting for a sign-in whose credentials were
// stored since, by another process.
func (manager *Manager) signedInElsewhere() []string {
	manager.mu.Lock()
	waiting := map[string]string{}
	configs := map[string]ServerConfig{}
	for name, connection := range manager.servers {
		if connection.state == ServerNeedsAuth {
			waiting[name], configs[name] = connection.tokensAtSignIn, connection.entry.Config
		}
	}
	manager.mu.Unlock()
	var names []string
	for name, tokens := range waiting {
		if manager.storedTokens(name, configs[name]) != tokens {
			names = append(names, name)
		}
	}
	return names
}

// SignIn signs a server in through the browser and reconnects it.
func (manager *Manager) SignIn(ctx context.Context, name string, prompt signInPrompt) error {
	manager.mu.Lock()
	connection := manager.servers[name]
	if connection == nil {
		manager.mu.Unlock()
		return fmt.Errorf("no MCP server named %q", name)
	}
	config, challenged := connection.entry.Config, connection.challenge
	manager.mu.Unlock()
	if !config.UsesOAuth() {
		return fmt.Errorf("MCP server %q does not use OAuth; only HTTP servers without an Authorization header do", name)
	}
	flow, err := manager.oauthFlowFor(name, config)
	if err != nil {
		return err
	}
	if err := signIn(ctx, flow, challenged, prompt); err != nil {
		return err
	}
	// The challenge that asked for this sign-in is answered.
	manager.mu.Lock()
	connection.challenge = nil
	manager.mu.Unlock()
	if err := manager.connectServer(ctx, name, true); err != nil {
		return fmt.Errorf("signed in, but %w", err)
	}
	return nil
}

// SignOut deletes a server's stored credentials and disconnects it; it
// reports whether there were any.
func (manager *Manager) SignOut(name string) (bool, error) {
	manager.mu.Lock()
	connection := manager.servers[name]
	if connection == nil {
		manager.mu.Unlock()
		return false, fmt.Errorf("no MCP server named %q", name)
	}
	config, session := connection.entry.Config, connection.session
	manager.mu.Unlock()
	if !config.UsesOAuth() {
		return false, fmt.Errorf("MCP server %q does not use OAuth; only HTTP servers without an Authorization header do", name)
	}
	removed, err := newCredentialStore(manager.agentDir).remove(name, config.URL)
	if session != nil {
		_ = session.Close()
	}
	manager.setServerFailed(name, session, errSignInRequired)
	return removed, err
}

// RemoveCredentials deletes a server's stored OAuth credentials outside a session.
func RemoveCredentials(agentDir, name, serverURL string) (bool, error) {
	return newCredentialStore(agentDir).remove(name, serverURL)
}

// ErrSignInCancelled reports a sign-in the user abandoned or did not finish in time.
var ErrSignInCancelled = errSignInCancelled

// Login signs a server in outside a session, unless it already accepts the
// stored credentials, and reports the tools it then offers. show receives
// the authorization URL; paste asks for the redirect URL and ends with ctx.
func Login(ctx context.Context, cwd, agentDir string, entry Entry, show func(string), paste func(context.Context) (string, error)) (tools int, already bool, err error) {
	if !entry.Config.UsesOAuth() {
		return 0, false, fmt.Errorf("MCP server %q does not use OAuth; only HTTP servers without an Authorization header do", entry.Name)
	}
	manager := newManager(agentDir, nil)
	manager.configure(cwd, []Entry{entry})
	defer func() { _ = manager.Close() }()
	// Connecting first answers whether a sign-in is needed and records the server's challenge.
	if err := manager.connectServer(ctx, entry.Name, false); err == nil {
		return len(manager.statusOf(entry.Name).Tools), true, nil
	} else if status := manager.statusOf(entry.Name); status.State != ServerNeedsAuth {
		return 0, false, fmt.Errorf("MCP server %q failed to connect: %w", entry.Name, err)
	}
	if err := manager.SignIn(ctx, entry.Name, signInPrompt{show: show, paste: paste}); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = errSignInCancelled
		}
		return 0, false, err
	}
	return len(manager.statusOf(entry.Name).Tools), false, nil
}
