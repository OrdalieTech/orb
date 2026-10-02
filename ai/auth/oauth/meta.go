package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

// Meta splits identity from API access: the RFC 8628 device grant yields an
// identity token that inference rejects, so it is exchanged for a Model API key
// at the Muse Code key-mint endpoint. The identity token is stored as refresh
// and the minted key (valid about a day) as access, so the ordinary refresh
// path re-mints the key. The identity token itself is not renewable: a 401 or
// 403 from mint means signing in again.
const (
	metaClientID                 = "1031625952748946" // Muse Code CLI client id
	defaultMetaDeviceAuthURL     = "https://auth.meta.com/oidc/device/authorization/"
	defaultMetaDeviceTokenURL    = "https://auth.meta.com/oidc/device/token/"
	defaultMetaAPIKeyMintURL     = "https://api.meta.ai/muse-code/key"
	metaAPIKeyLifetime           = 24 * time.Hour
	metaRequestTimeout           = 30 * time.Second
	metaDeviceCodeGrantType      = "urn:ietf:params:oauth:grant-type:device_code"
	metaSessionExpiredLoginHint  = "Run `/login meta` to sign in again."
	metaDeviceExpiredLoginPrompt = "Meta device authorization expired. Please restart login."
)

type MetaOptions struct {
	DeviceAuthorizationURL string
	DeviceTokenURL         string
	APIKeyMintURL          string
	HTTPClient             *http.Client
	Now                    func() time.Time
	Sleep                  func(context.Context, time.Duration) error
}

type Meta struct{ options MetaOptions }

func NewMeta(options *MetaOptions) *Meta {
	configured := MetaOptions{}
	if options != nil {
		configured = *options
	}
	if configured.DeviceAuthorizationURL == "" {
		configured.DeviceAuthorizationURL = defaultMetaDeviceAuthURL
	}
	if configured.DeviceTokenURL == "" {
		configured.DeviceTokenURL = defaultMetaDeviceTokenURL
	}
	if configured.APIKeyMintURL == "" {
		configured.APIKeyMintURL = defaultMetaAPIKeyMintURL
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = defaultHTTPClient
	}
	if configured.Now == nil {
		configured.Now = time.Now
	}
	return &Meta{options: configured}
}

func (*Meta) Name() string { return "Meta (Muse subscription)" }

func (*Meta) LoginLabel() string { return "Sign in with Meta" }

func (flow *Meta) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	credential, err := flow.login(ctx, interaction)
	if err != nil && ctx.Err() != nil {
		return nil, errors.New(deviceCodeCancelMessage)
	}
	return credential, err
}

func (flow *Meta) login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	status, body, err := flow.post(ctx, flow.options.DeviceAuthorizationURL, "application/x-www-form-urlencoded", orderedForm("client_id", metaClientID), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("Meta device authorization failed with status %d%s", status, metaErrorDetail(body)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	deviceCode, _ := body["device_code"].(string)
	userCode, _ := body["user_code"].(string)
	verificationURI := metaTrustedURL(body["verification_uri_complete"])
	if verificationURI == "" {
		verificationURI = metaTrustedURL(body["verification_uri"])
	}
	if deviceCode == "" || userCode == "" || verificationURI == "" {
		encoded, _ := json.Marshal(body)
		return nil, fmt.Errorf("Invalid Meta device authorization response: %s", encoded) //nolint:staticcheck // Upstream capitalization is observable.
	}
	interval, expires := metaPositive(body["interval"]), metaPositive(body["expires_in"])
	event := auth.AuthEvent{Type: auth.EventDeviceCode, UserCode: userCode, VerificationURI: verificationURI}
	if interval != nil {
		event.IntervalSeconds = int(*interval)
	}
	if expires != nil {
		event.ExpiresInSeconds = int(*expires)
	}
	interaction.Notify(event)
	identity, err := pollOAuthDeviceCodeFlow(deviceCodePollOptions[string]{
		intervalSeconds: interval, expiresInSeconds: expires, waitBeforeFirst: true,
		ctx: ctx, sleep: flow.options.Sleep,
		poll: func() (deviceCodePollResult[string], error) { return flow.pollIdentity(ctx, deviceCode) },
	})
	if err != nil {
		return nil, err
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Enabling Meta Model API access..."})
	return flow.mint(ctx, identity)
}

func (flow *Meta) pollIdentity(ctx context.Context, deviceCode string) (deviceCodePollResult[string], error) {
	status, body, err := flow.post(ctx, flow.options.DeviceTokenURL, "application/x-www-form-urlencoded", orderedForm(
		"grant_type", metaDeviceCodeGrantType, "device_code", deviceCode, "client_id", metaClientID,
	), nil)
	if err != nil {
		return deviceCodePollResult[string]{}, err
	}
	if token, _ := body["access_token"].(string); status >= 200 && status <= 299 && token != "" {
		return deviceCodePollResult[string]{status: deviceCodeComplete, value: token}, nil
	}
	switch body["error"] {
	case "authorization_pending":
		return deviceCodePollResult[string]{status: deviceCodePending}, nil
	case "slow_down":
		return deviceCodePollResult[string]{status: deviceCodeSlowDown, intervalSeconds: metaPositive(body["interval"])}, nil
	case "access_denied":
		return deviceCodePollResult[string]{status: deviceCodeFailed, message: "Meta login was denied."}, nil
	case "expired_token":
		return deviceCodePollResult[string]{status: deviceCodeFailed, message: metaDeviceExpiredLoginPrompt}, nil
	default:
		return deviceCodePollResult[string]{status: deviceCodeFailed, message: fmt.Sprintf("Meta device token request failed with status %d%s", status, metaErrorDetail(body))}, nil
	}
}

// mint exchanges an identity token for a Model API key.
func (flow *Meta) mint(ctx context.Context, identity string) (*auth.Credential, error) {
	status, body, err := flow.post(ctx, flow.options.APIKeyMintURL, "application/json", []byte("{}"), map[string]string{
		"Authorization": "Bearer " + identity, "x-api-version": "1.0.0",
	})
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("Meta session expired (status %d). %s%s", status, metaSessionExpiredLoginHint, metaErrorDetail(body)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	if status < 200 || status > 299 {
		return nil, fmt.Errorf("Meta API key mint failed with status %d%s", status, metaErrorDetail(body)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	key, _ := body["api_key"].(string)
	if key == "" {
		suffix := ""
		if action := metaTrustedURL(body["action_url"]); action != "" {
			suffix = " Complete setup at " + action
		}
		return nil, errors.New("Meta did not issue an API key." + suffix) //nolint:staticcheck // Upstream capitalization is observable.
	}
	expires := flow.options.Now().Add(metaAPIKeyLifetime).UnixMilli()
	return &auth.Credential{Type: auth.CredentialOAuth, Refresh: identity, Access: key, Expires: expires}, nil
}

func (flow *Meta) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("Meta OAuth refresh requires an OAuth credential") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return flow.mint(ctx, credential.Refresh)
}

func (*Meta) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("Meta OAuth credential is required") //nolint:staticcheck // Upstream capitalization is observable.
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

func (flow *Meta) post(ctx context.Context, endpoint, contentType string, payload []byte, headers map[string]string) (int, map[string]any, error) {
	requestContext, cancel := context.WithTimeout(ctx, metaRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", contentType)
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return 0, nil, cancelledLoginError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, err
	}
	body := map[string]any{}
	_ = json.Unmarshal(contents, &body)
	return response.StatusCode, body, nil
}

func metaErrorDetail(body map[string]any) string {
	for _, key := range []string{"error_description", "detail", "message", "error"} {
		if value, ok := body[key].(string); ok && strings.TrimSpace(value) != "" {
			return ": " + strings.TrimSpace(value)
		}
	}
	return ""
}

// metaTrustedURL keeps only http(s) URLs, which are opened in a browser.
func metaTrustedURL(value any) string {
	raw, ok := value.(string)
	if !ok || raw == "" {
		return ""
	}
	trusted, err := trustedVerificationURL(raw, false)
	if err != nil {
		return ""
	}
	return trusted
}

func metaPositive(value any) *float64 {
	number, ok := value.(float64)
	if !ok || number <= 0 || mathInvalid(number) {
		return nil
	}
	return &number
}
