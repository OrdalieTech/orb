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
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
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
		configured.CallbackHost = callbackHost()
	}
	if configured.CallbackPort == 0 {
		configured.CallbackPort = defaultOpenAICodexCallbackPort
	}
	if configured.RedirectURI == "" {
		configured.RedirectURI = fmt.Sprintf("http://localhost:%d%s", configured.CallbackPort, openAICodexCallbackPath)
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
	body, status, err := flow.post(ctx, flow.options.DeviceUserCodeURL, "application/json", []byte(`{"client_id":"`+openAICodexClientID+`"}`))
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
	responseBody, status, err := flow.post(ctx, flow.options.DeviceTokenURL, "application/json", body)
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

	// A busy callback port leaves only the pasted redirect URL.
	server, _ := startCallbackServer(callbackOptions[string]{
		provider: "OpenAI", listen: flow.options.Listen, host: flow.options.CallbackHost, port: flow.options.CallbackPort,
		path: openAICodexCallbackPath, state: state, complete: func(query url.Values) (string, error) { return query.Get("code"), nil },
	})
	if server != nil {
		defer server.close()
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventAuthURL, URL: authorizeURL, Instructions: "A browser window should open. Complete login to finish."})
	code, input, fromCallback, err := waitForCallbackOrManualInput(ctx, interaction, server, auth.AuthPrompt{
		Message: "Complete login in your browser, or paste the authorization code / redirect URL here:", Placeholder: flow.options.RedirectURI,
	})
	if err != nil {
		return nil, err
	}
	if !fromCallback {
		if code, err = parseOpenAICodexManual(input, state); err != nil {
			return nil, err
		}
	}
	if code == "" {
		return nil, errMissingAuthorizationCode
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
	responseBody, status, err := flow.post(ctx, flow.options.TokenURL, formContentType, body)
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

const formContentType = "application/x-www-form-urlencoded"

// send makes one request with header and reads the whole response body.
func send(ctx context.Context, client *http.Client, method, endpoint string, body []byte, header http.Header) (*http.Response, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, err
	}
	request.Header = header
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	return response, contents, err
}

// acceptJSON is the header of a request that sends contentType and wants JSON.
func acceptJSON(contentType string) http.Header {
	return http.Header{"Accept": {"application/json"}, "Content-Type": {contentType}}
}

func (flow *OpenAICodex) post(ctx context.Context, endpoint, contentType string, body []byte) ([]byte, int, error) {
	response, contents, err := send(ctx, flow.options.HTTPClient, http.MethodPost, endpoint, body, http.Header{"Content-Type": {contentType}})
	if err != nil {
		return nil, 0, err
	}
	return contents, response.StatusCode, nil
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
