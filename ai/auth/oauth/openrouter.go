package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

// OpenRouter ports upstream openRouterOAuth (ai/src/auth/oauth/openrouter.ts):
// a PKCE flow whose authorization code is exchanged for a permanent,
// user-controlled API key rather than an expiring access/refresh token pair.
// The callback is handled by a one-shot loopback server on an ephemeral port.
const (
	defaultOpenRouterAuthorizeURL  = "https://openrouter.ai/auth"
	defaultOpenRouterTokenURL      = "https://openrouter.ai/api/v1/auth/keys"
	openRouterLoginTimeout         = 5 * time.Minute
	openRouterTokenExchangeTimeout = 30 * time.Second
	// Number.MAX_SAFE_INTEGER: the minted key never expires on its own.
	openRouterKeyExpires = int64(9007199254740991)
)

type OpenRouterOptions struct {
	AuthorizeURL string
	TokenURL     string
	CallbackHost string
	LoginTimeout time.Duration
	HTTPClient   *http.Client
	Random       io.Reader
	Listen       func(network, address string) (net.Listener, error)
}

type OpenRouter struct{ options OpenRouterOptions }

func NewOpenRouter(options *OpenRouterOptions) *OpenRouter {
	configured := OpenRouterOptions{}
	if options != nil {
		configured = *options
	}
	if configured.AuthorizeURL == "" {
		configured.AuthorizeURL = defaultOpenRouterAuthorizeURL
	}
	if configured.TokenURL == "" {
		configured.TokenURL = defaultOpenRouterTokenURL
	}
	if configured.LoginTimeout == 0 {
		configured.LoginTimeout = openRouterLoginTimeout
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = defaultHTTPClient
	}
	if configured.Random == nil {
		configured.Random = rand.Reader
	}
	if configured.Listen == nil {
		configured.Listen = defaultListen
	}
	return &OpenRouter{options: configured}
}

func (*OpenRouter) Name() string { return "OpenRouter OAuth" }

func (*OpenRouter) LoginLabel() string { return "Sign in with OpenRouter" }

func (flow *OpenRouter) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	if ctx.Err() != nil {
		return nil, errors.New(deviceCodeCancelMessage)
	}
	verifier, challenge, err := GeneratePKCE(flow.options.Random)
	if err != nil {
		return nil, err
	}
	uuid, err := randomUUID(flow.options.Random)
	if err != nil {
		return nil, err
	}
	// OpenRouter sends no state; the random path keeps stray requests from completing the sign-in.
	server, err := startCallbackServer(callbackOptions[*auth.Credential]{
		provider: "OpenRouter", listen: flow.options.Listen, host: flow.callbackHost(), port: 0,
		path: "/oauth/callback/" + uuid, timeout: flow.options.LoginTimeout,
		complete: func(query url.Values) (*auth.Credential, error) {
			return flow.exchangeAuthorizationCode(ctx, query.Get("code"), verifier)
		},
	})
	if err != nil {
		return nil, err
	}
	defer server.close()
	authorizeURL := appendOrderedQuery(flow.options.AuthorizeURL,
		"callback_url", server.redirectURI,
		"code_challenge", challenge,
		"code_challenge_method", "S256",
	)
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Listening for OpenRouter OAuth callback on " + server.redirectURI})
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventAuthURL, URL: authorizeURL,
		Instructions: "Complete sign-in in your browser. If the browser is on another machine, paste the final redirect URL here.",
	})
	credential, input, fromCallback, err := waitForCallbackOrManualInput(ctx, interaction, server, auth.AuthPrompt{
		Message: "Complete sign-in in your browser, or paste the authorization code / redirect URL here:", Placeholder: server.redirectURI,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, errors.New(deviceCodeCancelMessage)
		}
		return nil, err
	}
	if fromCallback {
		return credential, nil
	}
	code := parseOpenRouterAuthorizationInput(input)
	if code == "" {
		return nil, errMissingAuthorizationCode
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Exchanging authorization code for an API key..."})
	return flow.exchangeAuthorizationCode(ctx, code, verifier)
}

// Refresh is a no-op: the OAuth flow mints a permanent API key.
func (*OpenRouter) Refresh(_ context.Context, credential *auth.Credential) (*auth.Credential, error) {
	return credential, nil
}

func (*OpenRouter) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("OpenRouter OAuth credential is required")
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

func (flow *OpenRouter) callbackHost() string {
	if flow.options.CallbackHost != "" {
		return flow.options.CallbackHost
	}
	return callbackHost()
}

func parseOpenRouterAuthorizationInput(input string) string {
	value := strings.TrimSpace(input)
	if value == "" {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.IsAbs() {
		return parsed.Query().Get("code")
	}
	if strings.Contains(value, "code=") {
		if query, err := url.ParseQuery(strings.TrimPrefix(value, "?")); err == nil {
			return query.Get("code")
		}
	}
	return value
}

func (flow *OpenRouter) exchangeAuthorizationCode(ctx context.Context, code, verifier string) (*auth.Credential, error) {
	if ctx.Err() != nil {
		return nil, errors.New(deviceCodeCancelMessage)
	}
	exchangeCtx, cancel := context.WithTimeout(ctx, openRouterTokenExchangeTimeout)
	defer cancel()
	body := orderedJSON(
		"code", code,
		"code_verifier", verifier,
		"code_challenge_method", "S256",
	)
	request, err := http.NewRequestWithContext(exchangeCtx, http.MethodPost, flow.options.TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return nil, openRouterExchangeFailure(ctx, exchangeCtx, err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, openRouterExchangeFailure(ctx, exchangeCtx, err)
	}
	ok := response.StatusCode >= 200 && response.StatusCode < 300
	responseBody := map[string]any{}
	var decoded any
	if json.Unmarshal(contents, &decoded) == nil {
		if typed, isObject := decoded.(map[string]any); isObject {
			responseBody = typed
		}
	} else if ok {
		return nil, errors.New("OpenRouter OAuth returned invalid JSON")
	}

	if !ok {
		detail := openRouterErrorDetail(responseBody)
		if detail != "" {
			detail = ": " + detail
		}
		return nil, fmt.Errorf("OpenRouter OAuth key exchange failed (HTTP %d)%s", response.StatusCode, detail)
	}
	key, _ := responseBody["key"].(string)
	if key == "" {
		return nil, errors.New(`OpenRouter OAuth response carries no "key"`)
	}
	return auth.OAuthCredentialAccessFirst(key, "", openRouterKeyExpires), nil
}

func openRouterExchangeFailure(ctx, exchangeCtx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New(deviceCodeCancelMessage)
	}
	if exchangeCtx.Err() != nil {
		return errors.New("OpenRouter OAuth token exchange timed out")
	}
	return err
}

func openRouterErrorDetail(body map[string]any) string {
	if detail, ok := body["error_description"].(string); ok {
		return detail
	}
	if detail, ok := body["message"].(string); ok {
		return detail
	}
	if detail, ok := body["error"].(string); ok {
		return detail
	}
	if nested, ok := body["error"].(map[string]any); ok {
		if message, ok := nested["message"].(string); ok {
			return message
		}
	}
	return ""
}

// randomUUID mirrors crypto.randomUUID(): a lowercase-hex UUIDv4.
func randomUUID(random io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(random, value[:]); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}
