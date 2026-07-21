package oauth

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

const (
	openAICodexClientID            = "app_EMoamEEZ73f0CkXaXp7hrann"
	openAICodexScope               = "openid profile email offline_access"
	openAICodexCallbackPath        = "/auth/callback"
	openAICodexBrowserMethod       = "browser"
	openAICodexDeviceMethod        = "device_code"
	openAICodexDeviceTimeout       = 15 * 60
	openAICodexJWTClaim            = "https://api.openai.com/auth"
	defaultOpenAICodexCallbackPort = 1455
)

type OpenAICodexOptions struct {
	AuthorizeURL          string
	TokenURL              string
	DeviceUserCodeURL     string
	DeviceTokenURL        string
	DeviceVerificationURI string
	RedirectURI           string
	DeviceRedirectURI     string
	CallbackHost          string
	CallbackPort          int
	HTTPClient            *http.Client
	Random                io.Reader
	Now                   func() time.Time
	Listen                func(network, address string) (net.Listener, error)
}

type OpenAICodex struct{ options OpenAICodexOptions }

func NewOpenAICodex(options *OpenAICodexOptions) *OpenAICodex {
	configured := OpenAICodexOptions{}
	if options != nil {
		configured = *options
	}
	const authBaseURL = "https://auth.openai.com"
	if configured.AuthorizeURL == "" {
		configured.AuthorizeURL = authBaseURL + "/oauth/authorize"
	}
	if configured.TokenURL == "" {
		configured.TokenURL = authBaseURL + "/oauth/token"
	}
	if configured.DeviceUserCodeURL == "" {
		configured.DeviceUserCodeURL = authBaseURL + "/api/accounts/deviceauth/usercode"
	}
	if configured.DeviceTokenURL == "" {
		configured.DeviceTokenURL = authBaseURL + "/api/accounts/deviceauth/token"
	}
	if configured.DeviceVerificationURI == "" {
		configured.DeviceVerificationURI = authBaseURL + "/codex/device"
	}
	if configured.DeviceRedirectURI == "" {
		configured.DeviceRedirectURI = authBaseURL + "/deviceauth/callback"
	}
	if configured.CallbackHost == "" {
		configured.CallbackHost = os.Getenv("PI_OAUTH_CALLBACK_HOST")
		if configured.CallbackHost == "" {
			configured.CallbackHost = "127.0.0.1"
		}
	}
	if configured.CallbackPort == 0 {
		configured.CallbackPort = defaultOpenAICodexCallbackPort
	}
	if configured.RedirectURI == "" {
		configured.RedirectURI = fmt.Sprintf("http://localhost:%d%s", configured.CallbackPort, openAICodexCallbackPath)
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = http.DefaultClient
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
	return &OpenAICodex{options: configured}
}

func (*OpenAICodex) Name() string { return "OpenAI (ChatGPT Plus/Pro)" }

func (flow *OpenAICodex) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	method, err := interaction.Prompt(ctx, auth.AuthPrompt{
		Type:    auth.PromptSelect,
		Message: "Select OpenAI Codex login method:",
		Options: []auth.PromptOption{
			{ID: openAICodexBrowserMethod, Label: "Browser login (default)"},
			{ID: openAICodexDeviceMethod, Label: "Device code login (headless)"},
		},
	})
	if err != nil {
		return nil, err
	}
	switch method {
	case openAICodexBrowserMethod:
		return flow.loginBrowser(ctx, interaction)
	case openAICodexDeviceMethod:
		return flow.loginDevice(ctx, interaction)
	default:
		return nil, fmt.Errorf("Unknown OpenAI Codex login method: %s", method) //nolint:staticcheck // Upstream capitalization is observable.
	}
}

func (flow *OpenAICodex) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("OpenAI Codex OAuth refresh requires an OAuth credential")
	}
	body := orderedForm(
		"grant_type", "refresh_token",
		"refresh_token", credential.Refresh,
		"client_id", openAICodexClientID,
	)
	token, err := flow.requestToken(ctx, body, "refresh")
	if err != nil {
		return nil, err
	}
	return flow.credentialFromToken(token)
}

func (*OpenAICodex) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("OpenAI Codex OAuth credential is required")
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

type openAICodexToken struct {
	access  string
	refresh string
	expires int64
}

type openAICodexDeviceToken struct {
	authorizationCode string
	verifier          string
}

func (flow *OpenAICodex) loginDevice(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	body, status, err := flow.request(ctx, http.MethodPost, flow.options.DeviceUserCodeURL, "application/json", []byte(`{"client_id":"`+openAICodexClientID+`"}`))
	if err != nil {
		return nil, cancelledLoginError(ctx, err)
	}
	if status < 200 || status >= 300 {
		if status == http.StatusNotFound {
			return nil, errors.New("OpenAI Codex device code login is not enabled for this server. Use browser login or verify the server URL.") //nolint:staticcheck // Exact upstream error text is observable.
		}
		return nil, fmt.Errorf("OpenAI Codex device code request failed with status %d%s", status, responseBodySuffix(body))
	}
	var response struct {
		DeviceAuthID string          `json:"device_auth_id"`
		UserCode     string          `json:"user_code"`
		Interval     json.RawMessage `json:"interval"`
	}
	if json.Unmarshal(body, &response) != nil {
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", normalizeJSONForError(body)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	interval, ok := parseJSONNumberOrString(response.Interval)
	if response.DeviceAuthID == "" || response.UserCode == "" || !ok || interval < 0 {
		return nil, fmt.Errorf("Invalid OpenAI Codex device code response: %s", normalizeJSONForError(body)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventDeviceCode, UserCode: response.UserCode, VerificationURI: flow.options.DeviceVerificationURI,
		IntervalSeconds: int(interval), ExpiresInSeconds: openAICodexDeviceTimeout,
	})
	expires := float64(openAICodexDeviceTimeout)
	code, err := pollOAuthDeviceCodeFlow(deviceCodePollOptions[openAICodexDeviceToken]{
		intervalSeconds:  &interval,
		expiresInSeconds: &expires,
		ctx:              ctx,
		poll: func() (deviceCodePollResult[openAICodexDeviceToken], error) {
			return flow.pollDevice(ctx, response.DeviceAuthID, response.UserCode)
		},
	})
	if err != nil {
		return nil, err
	}
	return flow.exchangeCode(ctx, code.authorizationCode, code.verifier, flow.options.DeviceRedirectURI)
}

func (flow *OpenAICodex) pollDevice(ctx context.Context, deviceAuthID, userCode string) (deviceCodePollResult[openAICodexDeviceToken], error) {
	body := []byte(`{"device_auth_id":` + strconv.Quote(deviceAuthID) + `,"user_code":` + strconv.Quote(userCode) + `}`)
	responseBody, status, err := flow.request(ctx, http.MethodPost, flow.options.DeviceTokenURL, "application/json", body)
	if err != nil {
		return deviceCodePollResult[openAICodexDeviceToken]{}, cancelledLoginError(ctx, err)
	}
	if status >= 200 && status < 300 {
		var response struct {
			AuthorizationCode string `json:"authorization_code"`
			CodeVerifier      string `json:"code_verifier"`
		}
		if json.Unmarshal(responseBody, &response) != nil || response.AuthorizationCode == "" || response.CodeVerifier == "" {
			return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodeFailed, message: "Invalid OpenAI Codex device auth token response: " + normalizeJSONForError(responseBody)}, nil
		}
		return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodeComplete, value: openAICodexDeviceToken{response.AuthorizationCode, response.CodeVerifier}}, nil
	}
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodePending}, nil
	}
	var failure struct {
		Error json.RawMessage `json:"error"`
	}
	_ = json.Unmarshal(responseBody, &failure)
	errorCode := ""
	if len(failure.Error) > 0 {
		if json.Unmarshal(failure.Error, &errorCode) != nil {
			var nested struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal(failure.Error, &nested)
			errorCode = nested.Code
		}
	}
	switch errorCode {
	case "deviceauth_authorization_pending":
		return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodePending}, nil
	case "slow_down":
		return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodeSlowDown}, nil
	default:
		return deviceCodePollResult[openAICodexDeviceToken]{status: deviceCodeFailed, message: fmt.Sprintf("OpenAI Codex device auth failed with status %d%s", status, responseBodySuffix(responseBody))}, nil
	}
}

func (flow *OpenAICodex) loginBrowser(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	verifier, challenge, err := GeneratePKCE(flow.options.Random)
	if err != nil {
		return nil, err
	}
	stateBytes := make([]byte, 16)
	if _, err := io.ReadFull(flow.options.Random, stateBytes); err != nil {
		return nil, err
	}
	state := hex.EncodeToString(stateBytes)
	authorizeURL := appendOrderedQuery(flow.options.AuthorizeURL,
		"response_type", "code",
		"client_id", openAICodexClientID,
		"redirect_uri", flow.options.RedirectURI,
		"scope", openAICodexScope,
		"code_challenge", challenge,
		"code_challenge_method", "S256",
		"state", state,
		"id_token_add_organizations", "true",
		"codex_cli_simplified_flow", "true",
		"originator", "pi",
	)

	callback := make(chan codexCallbackResult, 1)
	listener, listenErr := flow.options.Listen("tcp", fmt.Sprintf("%s:%d", flow.options.CallbackHost, flow.options.CallbackPort))
	var server *http.Server
	if listenErr == nil {
		server = &http.Server{Handler: openAICodexCallbackHandler(state, callback)}
		go func() { _ = server.Serve(listener) }()
		defer func() { _ = server.Close() }()
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventAuthURL, URL: authorizeURL, Instructions: "A browser window should open. Complete login to finish."})

	manualCtx, cancelManual := context.WithCancel(ctx)
	defer cancelManual()
	manual := make(chan manualResult, 1)
	go func() {
		input, promptErr := interaction.Prompt(manualCtx, auth.AuthPrompt{
			Type: auth.PromptManualCode, Message: "Complete login in your browser, or paste the authorization code / redirect URL here:", Placeholder: flow.options.RedirectURI,
		})
		manual <- manualResult{input: input, err: promptErr}
	}()

	code := ""
	if listenErr != nil {
		result := <-manual
		if result.err != nil {
			return nil, result.err
		}
		code, err = parseOpenAICodexManual(result.input, state)
	} else {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case result := <-callback:
			cancelManual()
			code = result.code
		case result := <-manual:
			if result.err != nil {
				return nil, result.err
			}
			code, err = parseOpenAICodexManual(result.input, state)
		}
	}
	if err != nil {
		return nil, err
	}
	if code == "" {
		return nil, errors.New("Missing authorization code") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return flow.exchangeCode(ctx, code, verifier, flow.options.RedirectURI)
}

func parseOpenAICodexManual(input, expectedState string) (string, error) {
	code, state, err := parseAuthorizationInput(input)
	if err != nil {
		return "", err
	}
	if state != "" && state != expectedState {
		return "", errors.New("State mismatch") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return code, nil
}

type codexCallbackResult struct{ code string }

func openAICodexCallbackHandler(expectedState string, callback chan<- codexCallbackResult) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		if request.URL.Path != openAICodexCallbackPath {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, errorPage("Callback route not found."))
			return
		}
		if request.URL.Query().Get("state") != expectedState {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, errorPage("State mismatch."))
			return
		}
		code := request.URL.Query().Get("code")
		if code == "" {
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(writer, errorPage("Missing authorization code."))
			return
		}
		_, _ = io.WriteString(writer, successPage("OpenAI authentication completed. You can close this window."))
		select {
		case callback <- codexCallbackResult{code: code}:
		default:
		}
	})
}

func (flow *OpenAICodex) exchangeCode(ctx context.Context, code, verifier, redirectURI string) (*auth.Credential, error) {
	body := orderedForm(
		"grant_type", "authorization_code",
		"client_id", openAICodexClientID,
		"code", code,
		"code_verifier", verifier,
		"redirect_uri", redirectURI,
	)
	token, err := flow.requestToken(ctx, body, "exchange")
	if err != nil {
		return nil, err
	}
	return flow.credentialFromToken(token)
}

func (flow *OpenAICodex) requestToken(ctx context.Context, body []byte, operation string) (openAICodexToken, error) {
	responseBody, status, err := flow.request(ctx, http.MethodPost, flow.options.TokenURL, "application/x-www-form-urlencoded", body)
	if err != nil {
		if operation == "refresh" {
			return openAICodexToken{}, fmt.Errorf("OpenAI Codex token refresh error: %s", err)
		}
		return openAICodexToken{}, cancelledLoginError(ctx, err)
	}
	if status < 200 || status >= 300 {
		return openAICodexToken{}, fmt.Errorf("OpenAI Codex token %s failed (%d): %s", operation, status, responseBodyOrStatus(responseBody, status))
	}
	var response struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    *int64 `json:"expires_in"`
	}
	if json.Unmarshal(responseBody, &response) != nil || response.AccessToken == "" || response.RefreshToken == "" || response.ExpiresIn == nil {
		return openAICodexToken{}, fmt.Errorf("OpenAI Codex token %s response missing fields: %s", operation, normalizeJSONForError(responseBody))
	}
	return openAICodexToken{response.AccessToken, response.RefreshToken, flow.options.Now().UnixMilli() + *response.ExpiresIn*1000}, nil
}

func (flow *OpenAICodex) credentialFromToken(token openAICodexToken) (*auth.Credential, error) {
	accountID := OpenAICodexAccountID(token.access)
	if accountID == "" {
		return nil, errors.New("Failed to extract accountId from token") //nolint:staticcheck // Upstream capitalization is observable.
	}
	credential := auth.OAuthCredentialAccessFirst(token.access, token.refresh, token.expires)
	encoded, _ := json.Marshal(accountID)
	credential.SetExtra("accountId", encoded)
	return credential, nil
}

func OpenAICodexAccountID(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		payload, err = base64.StdEncoding.DecodeString(parts[1])
	}
	if err != nil {
		return ""
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	var scoped struct {
		AccountID string `json:"chatgpt_account_id"`
	}
	if json.Unmarshal(claims[openAICodexJWTClaim], &scoped) != nil {
		return ""
	}
	return scoped.AccountID
}

func (flow *OpenAICodex) request(ctx context.Context, method, endpoint, contentType string, body []byte) ([]byte, int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	request.Header.Set("Content-Type", contentType)
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	return contents, response.StatusCode, err
}

func orderedForm(pairs ...string) []byte {
	items := make([]string, 0, len(pairs)/2)
	for index := 0; index < len(pairs); index += 2 {
		items = append(items, url.QueryEscape(pairs[index])+"="+url.QueryEscape(pairs[index+1]))
	}
	return []byte(strings.Join(items, "&"))
}

func appendOrderedQuery(endpoint string, pairs ...string) string {
	separator := "?"
	if strings.Contains(endpoint, "?") {
		separator = "&"
	}
	return endpoint + separator + string(orderedForm(pairs...))
}

func parseJSONNumberOrString(raw json.RawMessage) (float64, bool) {
	var number float64
	if json.Unmarshal(raw, &number) == nil && !mathInvalid(number) {
		return number, true
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return 0, false
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(text), 64)
	return number, err == nil && !mathInvalid(number)
}

func mathInvalid(value float64) bool {
	return math.IsNaN(value) || math.IsInf(value, 0)
}

func cancelledLoginError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.New(deviceCodeCancelMessage)
	}
	return err
}

func responseBodySuffix(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return ": " + string(body)
}

func responseBodyOrStatus(body []byte, status int) string {
	if len(body) > 0 {
		return string(body)
	}
	return http.StatusText(status)
}

func normalizeJSONForError(body []byte) string {
	var value any
	if json.Unmarshal(body, &value) != nil {
		return string(body)
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return string(body)
	}
	return string(normalized)
}
