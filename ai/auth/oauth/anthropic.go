package oauth

import (
	"bytes"
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
	"os"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/internal/jsonwire"
)

const (
	defaultAuthorizeURL = "https://claude.ai/oauth/authorize"
	defaultTokenURL     = "https://platform.claude.com/v1/oauth/token"
	defaultCallbackPort = 53692
	callbackPath        = "/callback"
	anthropicScopes     = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
)

var anthropicClientID = mustDecode("OWQxYzI1MGEtZTYxYi00NGQ5LTg4ZWQtNTk0NGQxOTYyZjVl")

type AnthropicOptions struct {
	AuthorizeURL string
	TokenURL     string
	CallbackHost string
	CallbackPort int
	RedirectURI  string
	HTTPClient   *http.Client
	Random       io.Reader
	Now          func() time.Time
	Listen       func(network, address string) (net.Listener, error)
}

type Anthropic struct {
	options AnthropicOptions
}

func NewAnthropic(options *AnthropicOptions) *Anthropic {
	configured := AnthropicOptions{}
	if options != nil {
		configured = *options
	}
	if configured.AuthorizeURL == "" {
		configured.AuthorizeURL = defaultAuthorizeURL
	}
	if configured.TokenURL == "" {
		configured.TokenURL = defaultTokenURL
	}
	if configured.CallbackHost == "" {
		configured.CallbackHost = os.Getenv("PI_OAUTH_CALLBACK_HOST")
		if configured.CallbackHost == "" {
			configured.CallbackHost = "127.0.0.1"
		}
	}
	if configured.CallbackPort == 0 {
		configured.CallbackPort = defaultCallbackPort
	}
	if configured.RedirectURI == "" {
		configured.RedirectURI = fmt.Sprintf("http://localhost:%d%s", configured.CallbackPort, callbackPath)
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if configured.Random == nil {
		configured.Random = rand.Reader
	}
	if configured.Now == nil {
		configured.Now = time.Now
	}
	if configured.Listen == nil {
		configured.Listen = net.Listen
	}
	return &Anthropic{options: configured}
}

func (*Anthropic) Name() string { return "Anthropic (Claude Pro/Max)" }

func (flow *Anthropic) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	verifier, challenge, err := GeneratePKCE(flow.options.Random)
	if err != nil {
		return nil, err
	}
	listener, err := flow.options.Listen("tcp", fmt.Sprintf("%s:%d", flow.options.CallbackHost, flow.options.CallbackPort))
	if err != nil {
		return nil, err
	}
	defer func() { _ = listener.Close() }()

	wait := make(chan callbackResult, 1)
	server := &http.Server{Handler: callbackHandler(verifier, wait)}
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

	authorizeURL, err := flow.authorizeURL(verifier, challenge)
	if err != nil {
		return nil, err
	}
	interaction.Notify(auth.AuthEvent{
		Type:         auth.EventAuthURL,
		URL:          authorizeURL,
		Instructions: "Complete login in your browser. If the browser is on another machine, paste the final redirect URL here.",
	})

	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()
	manual := make(chan manualResult, 1)
	go func() {
		input, promptErr := interaction.Prompt(manualCtx, auth.AuthPrompt{
			Type:        auth.PromptManualCode,
			Message:     "Complete login in your browser, or paste the authorization code / redirect URL here:",
			Placeholder: flow.options.RedirectURI,
		})
		manual <- manualResult{input: input, err: promptErr}
	}()

	var code, state string
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-wait:
		cancelManual()
		code, state = result.code, result.state
	case result := <-manual:
		if result.err != nil {
			return nil, result.err
		}
		code, state, err = parseAuthorizationInput(result.input)
		if err != nil {
			return nil, err
		}
		if state != "" && state != verifier {
			return nil, errors.New("OAuth state mismatch")
		}
		if state == "" {
			state = verifier
		}
	}
	if code == "" {
		return nil, errors.New("Missing authorization code") //nolint:staticcheck // Upstream error capitalization is observable.
	}
	if state == "" {
		return nil, errors.New("Missing OAuth state") //nolint:staticcheck // Upstream error capitalization is observable.
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Exchanging authorization code for tokens..."})
	body := orderedJSON(
		"grant_type", "authorization_code",
		"client_id", anthropicClientID,
		"code", code,
		"state", state,
		"redirect_uri", flow.options.RedirectURI,
		"code_verifier", verifier,
	)
	return flow.exchange(ctx, body, "Token exchange")
}

func (flow *Anthropic) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("Anthropic OAuth refresh requires an OAuth credential")
	}
	body := orderedJSON(
		"grant_type", "refresh_token",
		"client_id", anthropicClientID,
		"refresh_token", credential.Refresh,
	)
	return flow.exchange(ctx, body, "Anthropic token refresh")
}

func (*Anthropic) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("Anthropic OAuth credential is required")
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

func (flow *Anthropic) authorizeURL(verifier, challenge string) (string, error) {
	parsed, err := url.Parse(flow.options.AuthorizeURL)
	if err != nil {
		return "", err
	}
	pairs := []string{
		"code", "true",
		"client_id", anthropicClientID,
		"response_type", "code",
		"redirect_uri", flow.options.RedirectURI,
		"scope", anthropicScopes,
		"code_challenge", challenge,
		"code_challenge_method", "S256",
		"state", verifier,
	}
	query := make([]string, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		query = append(query, url.QueryEscape(pairs[index])+"="+url.QueryEscape(pairs[index+1]))
	}
	parsed.RawQuery = strings.Join(query, "&")
	return parsed.String(), nil
}

func (flow *Anthropic) exchange(ctx context.Context, body []byte, label string) (*auth.Credential, error) {
	responseBody, err := flow.postJSON(ctx, body)
	if err != nil {
		if label == "Token exchange" {
			return nil, fmt.Errorf( //nolint:staticcheck // Upstream error capitalization is observable.
				"Token exchange request failed. url=%s; redirect_uri=%s; response_type=authorization_code; details=%s",
				flow.options.TokenURL,
				flow.options.RedirectURI,
				formatOAuthErrorDetails(err),
			)
		}
		return nil, fmt.Errorf("%s request failed. url=%s; details=%s", label, flow.options.TokenURL, formatOAuthErrorDetails(err))
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(responseBody, &token); err != nil {
		return nil, fmt.Errorf("%s returned invalid JSON. url=%s; body=%s; details=%s", label, flow.options.TokenURL, responseBody, formatOAuthErrorDetails(err))
	}
	expires := flow.options.Now().UnixMilli() + token.ExpiresIn*1000 - int64(5*time.Minute/time.Millisecond)
	return auth.OAuthCredential(token.RefreshToken, token.AccessToken, expires), nil
}

func (flow *Anthropic) postJSON(ctx context.Context, body []byte) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.options.TokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP request failed. status=%d; url=%s; body=%s", response.StatusCode, flow.options.TokenURL, responseBody)
	}
	return responseBody, nil
}

func formatOAuthErrorDetails(err error) string { return "Error: " + err.Error() }

type callbackResult struct {
	code  string
	state string
}

type manualResult struct {
	input string
	err   error
}

func callbackHandler(expectedState string, wait chan<- callbackResult) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path != callbackPath {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, errorPage("Callback route not found."))
			return
		}
		query := request.URL.Query()
		if oauthError := query.Get("error"); oauthError != "" {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, errorPageWithDetails("Anthropic authentication did not complete.", "Error: "+oauthError))
			return
		}
		code, state := query.Get("code"), query.Get("state")
		if code == "" || state == "" {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, errorPage("Missing code or state parameter."))
			return
		}
		if state != expectedState {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, errorPage("State mismatch."))
			return
		}
		_, _ = io.WriteString(writer, successPage("Anthropic authentication completed. You can close this window."))
		select {
		case wait <- callbackResult{code: code, state: state}:
		default:
		}
	})
}

func parseAuthorizationInput(input string) (code, state string, err error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", "", nil
	}
	if parsed, parseErr := url.Parse(value); parseErr == nil && parsed.IsAbs() {
		return parsed.Query().Get("code"), parsed.Query().Get("state"), nil
	}
	if strings.Contains(value, "#") {
		parts := strings.SplitN(value, "#", 2)
		return parts[0], parts[1], nil
	}
	if strings.Contains(value, "code=") {
		query, parseErr := url.ParseQuery(value)
		if parseErr != nil {
			return "", "", parseErr
		}
		return query.Get("code"), query.Get("state"), nil
	}
	return value, "", nil
}

func orderedJSON(pairs ...string) []byte {
	var output bytes.Buffer
	output.WriteByte('{')
	for index := 0; index < len(pairs); index += 2 {
		if index > 0 {
			output.WriteByte(',')
		}
		key, _ := jsonwire.Marshal(pairs[index])
		value, _ := jsonwire.Marshal(pairs[index+1])
		output.Write(key)
		output.WriteByte(':')
		output.Write(value)
	}
	output.WriteByte('}')
	return output.Bytes()
}

func mustDecode(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return string(decoded)
}
