package oauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

// Sign in with ChatGPT shares a ChatGPT subscription with the OpenAI
// Responses API: a public-client PKCE flow whose user access token is sent
// directly to api.openai.com. Every login registers a new client, and OpenAI
// returns the issued client ID in the callback.
const (
	openAIChatGPTDynamicClientID = "dynamic_agent_client"
	openAIChatGPTAgentNameHint   = "Orb"
	openAIChatGPTResource        = "https://api.openai.com/v1"
	openAIChatGPTDirectScope     = "chatgpt.tokens.use.direct"
	openAIChatGPTScope           = "openid profile email offline_access resource.invoke " + openAIChatGPTDirectScope
	openAIChatGPTCallbackPath    = "/auth/callback"
	openAIChatGPTCallbackPort    = 1455
	// Refresh this long before the real expiry so no request starts with a
	// token about to expire.
	openAIChatGPTExpiryMargin = 3 * time.Minute
)

var uuidPattern = lazyregexp.New(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// DeviceIDSource is implemented by login interactions that can supply a
// stable installation ID (a UUID); Sign in with ChatGPT sends it to OpenAI as
// the agent host ID.
type DeviceIDSource interface {
	DeviceID() (string, error)
}

type OpenAIChatGPTOptions struct {
	AuthorizeURL string
	TokenURL     string
	CallbackHost string
	CallbackPort int
	HTTPClient   *http.Client
	Random       io.Reader
	Now          func() time.Time
	Listen       func(network, address string) (net.Listener, error)
}

type OpenAIChatGPT struct{ options OpenAIChatGPTOptions }

func NewOpenAIChatGPT(options *OpenAIChatGPTOptions) *OpenAIChatGPT {
	configured := OpenAIChatGPTOptions{}
	if options != nil {
		configured = *options
	}
	if configured.AuthorizeURL == "" {
		configured.AuthorizeURL = "https://auth.openai.com/api/accounts/authorize"
	}
	if configured.TokenURL == "" {
		configured.TokenURL = "https://auth.openai.com/api/accounts/oauth/token"
	}
	if configured.CallbackHost == "" {
		configured.CallbackHost = callbackHost()
	}
	if configured.CallbackPort == 0 {
		configured.CallbackPort = openAIChatGPTCallbackPort
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = defaultHTTPClient
	}
	if configured.Random == nil {
		configured.Random = rand.Reader
	}
	if configured.Now == nil {
		configured.Now = time.Now
	}
	if configured.Listen == nil {
		configured.Listen = defaultListen
	}
	return &OpenAIChatGPT{options: configured}
}

func (*OpenAIChatGPT) Name() string { return "OpenAI (ChatGPT subscription)" }

func (*OpenAIChatGPT) LoginLabel() string { return "Sign in with ChatGPT" }

func (flow *OpenAIChatGPT) redirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", flow.options.CallbackPort, openAIChatGPTCallbackPath)
}

func (flow *OpenAIChatGPT) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	credential, err := flow.login(ctx, interaction)
	if err != nil && ctx.Err() != nil {
		return nil, errors.New(deviceCodeCancelMessage)
	}
	return credential, err
}

type chatGPTAuthorization struct{ code, clientID string }

func (flow *OpenAIChatGPT) login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	var deviceID string
	if source, ok := interaction.(DeviceIDSource); ok {
		var err error
		if deviceID, err = source.DeviceID(); err != nil {
			return nil, err
		}
	}
	if !uuidPattern().MatchString(deviceID) {
		return nil, errors.New("Sign in with ChatGPT requires a device ID (UUID) for this installation") //nolint:staticcheck // Upstream capitalization is observable.
	}
	verifier, challenge, err := GeneratePKCE(flow.options.Random)
	if err != nil {
		return nil, err
	}
	state, err := flow.randomValue()
	if err != nil {
		return nil, err
	}
	nonce, err := flow.randomValue()
	if err != nil {
		return nil, err
	}
	server, listenErr := startCallbackServer(callbackOptions[chatGPTAuthorization]{
		provider: "ChatGPT", listen: flow.options.Listen, host: flow.options.CallbackHost, port: flow.options.CallbackPort,
		path: openAIChatGPTCallbackPath, state: state,
		complete: func(query url.Values) (chatGPTAuthorization, error) {
			return chatGPTAuthorizationFromQuery(query, state)
		},
	})
	if listenErr != nil {
		interaction.Notify(auth.AuthEvent{Type: auth.EventInfo, Message: fmt.Sprintf("Could not listen on %s; paste the final redirect URL to continue. %s", flow.redirectURI(), listenErr)})
	} else {
		defer server.close()
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventAuthURL, URL: appendOrderedQuery(flow.options.AuthorizeURL,
			"client_id", openAIChatGPTDynamicClientID,
			"agent_name_hint", openAIChatGPTAgentNameHint,
			"ext_agent_host_id", "urn:uuid:"+strings.ToLower(deviceID),
			"response_type", "code",
			"redirect_uri", flow.redirectURI(),
			"resource", openAIChatGPTResource,
			"scope", openAIChatGPTScope,
			"state", state,
			"code_challenge", challenge,
			"code_challenge_method", "S256",
			"nonce", nonce,
		),
		Instructions: "Complete sign-in in your browser. If the callback does not complete, paste the final redirect URL here.",
	})
	result, input, fromCallback, err := waitForCallbackOrManualInput(ctx, interaction, server, auth.AuthPrompt{
		Message: "Complete login in your browser, or paste the final redirect URL here:", Placeholder: flow.redirectURI(),
	})
	if err != nil {
		return nil, err
	}
	if !fromCallback {
		if result, err = flow.parseManual(input, state); err != nil {
			return nil, err
		}
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Exchanging authorization code for tokens..."})
	token, err := flow.requestToken(ctx, orderedForm(
		"grant_type", "authorization_code",
		"client_id", result.clientID,
		"code", result.code,
		"code_verifier", verifier,
		"redirect_uri", flow.redirectURI(),
		"resource", openAIChatGPTResource,
	))
	if err != nil {
		return nil, err
	}
	// The ID token is not used to identify the user; its presence is part of
	// the token-response contract.
	if idToken, _ := token["id_token"].(string); strings.TrimSpace(idToken) == "" {
		return nil, errors.New("OpenAI OAuth token response did not contain an ID token") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return flow.credential(token, result.clientID)
}

func (flow *OpenAIChatGPT) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("OpenAI OAuth refresh requires an OAuth credential") //nolint:staticcheck // Upstream capitalization is observable.
	}
	var clientID string
	_ = json.Unmarshal(credential.Extra["clientId"], &clientID)
	if strings.TrimSpace(clientID) == "" {
		return nil, errors.New("Stored OpenAI OAuth credential does not contain an issued client ID; reconnect ChatGPT") //nolint:staticcheck // Upstream capitalization is observable.
	}
	token, err := flow.requestToken(ctx, orderedForm(
		"grant_type", "refresh_token",
		"client_id", clientID,
		"refresh_token", credential.Refresh,
		"resource", openAIChatGPTResource,
	))
	if err != nil {
		return nil, err
	}
	return flow.credential(token, clientID)
}

func (*OpenAIChatGPT) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("OpenAI OAuth credential is required") //nolint:staticcheck // Upstream capitalization is observable.
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

func (flow *OpenAIChatGPT) randomValue() (string, error) {
	value := make([]byte, 32)
	if _, err := io.ReadFull(flow.options.Random, value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func chatGPTAuthorizationFromQuery(query url.Values, expectedState string) (chatGPTAuthorization, error) {
	code := query.Get("code")
	if code == "" {
		return chatGPTAuthorization{}, errors.New("Missing authorization code") //nolint:staticcheck // Upstream capitalization is observable.
	}
	state := query.Get("state")
	if state == "" {
		return chatGPTAuthorization{}, errors.New("Missing OAuth state") //nolint:staticcheck // Upstream capitalization is observable.
	}
	if state != expectedState {
		return chatGPTAuthorization{}, errors.New("OAuth state mismatch") //nolint:staticcheck // Upstream capitalization is observable.
	}
	clientID := strings.TrimSpace(query.Get("client_id"))
	if clientID == "" {
		return chatGPTAuthorization{}, errors.New("OpenAI OAuth registration callback did not contain an issued client ID") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return chatGPTAuthorization{code: code, clientID: clientID}, nil
}

func (flow *OpenAIChatGPT) parseManual(input, expectedState string) (chatGPTAuthorization, error) {
	parsed, err := url.Parse(strings.TrimSpace(input))
	if err != nil || parsed.Scheme == "" {
		return chatGPTAuthorization{}, errors.New("Paste the full callback URL from the browser") //nolint:staticcheck // Upstream capitalization is observable.
	}
	expected, _ := url.Parse(flow.redirectURI())
	if parsed.Scheme != expected.Scheme || parsed.Host != expected.Host || parsed.Path != expected.Path {
		return chatGPTAuthorization{}, fmt.Errorf("The pasted callback URL must start with %s", flow.redirectURI()) //nolint:staticcheck // Upstream capitalization is observable.
	}
	if failure := parsed.Query().Get("error"); failure != "" {
		return chatGPTAuthorization{}, fmt.Errorf("ChatGPT authorization failed: %s", failure)
	}
	return chatGPTAuthorizationFromQuery(parsed.Query(), expectedState)
}

func (flow *OpenAIChatGPT) requestToken(ctx context.Context, body []byte) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.options.TokenURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("accept", "application/json")
	request.Header.Set("content-type", "application/x-www-form-urlencoded")
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return nil, cancelledLoginError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		detail := strings.TrimSpace(string(contents))
		if detail == "" {
			detail = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("OpenAI OAuth token request failed (%d): %s", response.StatusCode, detail)
	}
	var token map[string]any
	if json.Unmarshal(contents, &token) != nil || token == nil {
		return nil, errors.New("OpenAI OAuth token response must be an object") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return token, nil
}

func (flow *OpenAIChatGPT) credential(token map[string]any, clientID string) (*auth.Credential, error) {
	field := func(name string) (string, error) {
		value, _ := token[name].(string)
		if strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("OpenAI OAuth token response has invalid %s", name)
		}
		return value, nil
	}
	access, err := field("access_token")
	if err != nil {
		return nil, err
	}
	refresh, err := field("refresh_token")
	if err != nil {
		return nil, err
	}
	scope, err := field("scope")
	if err != nil {
		return nil, err
	}
	expiresIn, ok := token["expires_in"].(float64)
	if !ok || expiresIn <= 0 || mathInvalid(expiresIn) {
		return nil, errors.New("OpenAI OAuth token response has invalid expires_in") //nolint:staticcheck // Upstream capitalization is observable.
	}
	scopes := strings.Fields(scope)
	if !slices.Contains(scopes, openAIChatGPTDirectScope) {
		return nil, fmt.Errorf("OpenAI OAuth grant did not include %s", openAIChatGPTDirectScope)
	}
	expires := flow.options.Now().Add(time.Duration(expiresIn*float64(time.Second)) - openAIChatGPTExpiryMargin).UnixMilli()
	credential := auth.OAuthCredentialAccessFirst(access, refresh, expires)
	encodedClient, _ := json.Marshal(clientID)
	credential.SetExtra("clientId", encodedClient)
	encodedScopes, _ := json.Marshal(scopes)
	credential.SetExtra("scopes", encodedScopes)
	return credential, nil
}
