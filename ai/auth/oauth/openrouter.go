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
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
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
		configured.HTTPClient = http.DefaultClient
	}
	if configured.Random == nil {
		configured.Random = rand.Reader
	}
	if configured.Listen == nil {
		configured.Listen = net.Listen
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
	callbackPath := "/oauth/callback/" + uuid
	callbackHost := flow.callbackHost()

	listener, err := flow.options.Listen("tcp", net.JoinHostPort(callbackHost, "0"))
	if err != nil {
		return nil, err
	}
	state := &openRouterCallbackState{outcome: make(chan openRouterOutcome, 1)}
	server := &http.Server{Handler: flow.callbackHandler(ctx, callbackPath, verifier, state)}
	serveDone := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveDone <- err
	}()
	defer func() {
		_ = server.Close()
		<-serveDone
	}()

	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return nil, errors.New("Could not determine the OpenRouter OAuth callback port") //nolint:staticcheck // Upstream capitalization is observable.
	}
	callbackURL := "http://" + net.JoinHostPort(callbackHost, strconv.Itoa(address.Port)) + callbackPath
	authorizeURL := appendOrderedQuery(flow.options.AuthorizeURL,
		"callback_url", callbackURL,
		"code_challenge", challenge,
		"code_challenge_method", "S256",
	)

	interaction.Notify(auth.AuthEvent{
		Type:    auth.EventProgress,
		Message: "Listening for OpenRouter OAuth callback on " + callbackURL,
	})
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventAuthURL, URL: authorizeURL, Instructions: "Complete sign-in in your browser.",
	})

	timer := time.NewTimer(flow.options.LoginTimeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, errors.New(deviceCodeCancelMessage)
	case <-timer.C:
		return nil, errors.New("OpenRouter OAuth login timed out")
	case outcome := <-state.outcome:
		return outcome.credential, outcome.err
	}
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
	if host := os.Getenv("PI_OAUTH_CALLBACK_HOST"); host != "" {
		return host
	}
	return "127.0.0.1"
}

type openRouterOutcome struct {
	credential *auth.Credential
	err        error
}

type openRouterCallbackState struct {
	mu      sync.Mutex
	claimed bool
	settled bool
	outcome chan openRouterOutcome
}

func (state *openRouterCallbackState) finish(credential *auth.Credential, err error) {
	state.mu.Lock()
	if state.settled {
		state.mu.Unlock()
		return
	}
	state.settled = true
	state.mu.Unlock()
	state.outcome <- openRouterOutcome{credential: credential, err: err}
}

func (flow *OpenRouter) callbackHandler(ctx context.Context, callbackPath, verifier string, state *openRouterCallbackState) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != callbackPath {
			sendOpenRouterHTML(writer, http.StatusNotFound, errorPage("OAuth callback route not found."))
			return
		}
		state.mu.Lock()
		if state.claimed || state.settled {
			state.mu.Unlock()
			sendOpenRouterHTML(writer, http.StatusConflict, errorPage("This OAuth callback has already been used."))
			return
		}
		query := request.URL.Query()
		if oauthError := query.Get("error"); oauthError != "" {
			state.mu.Unlock()
			description := oauthError
			if query.Has("error_description") {
				description = query.Get("error_description")
			}
			sendOpenRouterHTML(writer, http.StatusBadRequest, errorPageWithDetails("OpenRouter authorization was denied.", description))
			state.finish(nil, fmt.Errorf("OpenRouter authorization failed: %s", description))
			return
		}
		code := query.Get("code")
		if code == "" {
			state.mu.Unlock()
			sendOpenRouterHTML(writer, http.StatusBadRequest, errorPage("OpenRouter returned no authorization code."))
			return
		}
		state.claimed = true
		state.mu.Unlock()

		credential, err := flow.exchangeAuthorizationCode(ctx, code, verifier)
		if err != nil {
			sendOpenRouterHTML(writer, http.StatusBadGateway, errorPageWithDetails("OpenRouter key exchange failed.", err.Error()))
			state.finish(nil, err)
			return
		}
		sendOpenRouterHTML(writer, http.StatusOK, successPage("Signed in to OpenRouter. You may now close this page."))
		state.finish(credential, nil)
	})
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

func sendOpenRouterHTML(writer http.ResponseWriter, status int, html string) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = io.WriteString(writer, html)
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
