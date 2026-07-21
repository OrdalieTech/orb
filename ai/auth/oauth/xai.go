package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

const (
	xAIClientID             = "b1a00492-073a-47ea-816f-4c329264a828"
	xAIScope                = "openid profile email offline_access grok-cli:access api:access"
	xAIDefaultLifetime      = 3600
	xAIRefreshSkew          = 5 * time.Minute
	defaultXAIDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	defaultXAITokenURL      = "https://auth.x.ai/oauth2/token"
)

type XAIOptions struct {
	DeviceCodeURL string
	TokenURL      string
	HTTPClient    *http.Client
	Now           func() time.Time
	Sleep         func(context.Context, time.Duration) error
}

type XAI struct{ options XAIOptions }

func NewXAI(options *XAIOptions) *XAI {
	configured := XAIOptions{}
	if options != nil {
		configured = *options
	}
	if configured.DeviceCodeURL == "" {
		configured.DeviceCodeURL = defaultXAIDeviceCodeURL
	}
	if configured.TokenURL == "" {
		configured.TokenURL = defaultXAITokenURL
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = http.DefaultClient
	}
	if configured.Now == nil {
		configured.Now = time.Now
	}
	return &XAI{options: configured}
}

func (*XAI) Name() string { return "xAI (Grok/X subscription)" }

func (*XAI) LoginLabel() string { return "Sign in with SuperGrok or X Premium" }

func (flow *XAI) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	response, err := flow.postForm(ctx, flow.options.DeviceCodeURL, orderedForm(
		"client_id", xAIClientID,
		"scope", xAIScope,
		"referrer", "pi",
	))
	if err != nil {
		return nil, err
	}
	if !response.ok {
		return nil, xAIRequestFailure("device authorization", response)
	}
	device, err := parseXAIDeviceCode(response.body)
	if err != nil {
		return nil, err
	}
	verificationURI := device.verificationURI
	if device.verificationURIComplete != "" {
		verificationURI = device.verificationURIComplete
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventDeviceCode, UserCode: device.userCode, VerificationURI: verificationURI,
		IntervalSeconds: int(device.interval), ExpiresInSeconds: int(device.expiresIn),
	})
	interval, expires := device.interval, device.expiresIn
	var intervalPointer *float64
	if interval > 0 {
		intervalPointer = &interval
	}
	return pollOAuthDeviceCodeFlow(deviceCodePollOptions[*auth.Credential]{
		intervalSeconds:  intervalPointer,
		expiresInSeconds: &expires,
		waitBeforeFirst:  true,
		ctx:              ctx,
		sleep:            flow.options.Sleep,
		poll: func() (deviceCodePollResult[*auth.Credential], error) {
			return flow.pollToken(ctx, device.deviceCode)
		},
	})
}

func (flow *XAI) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("xAI OAuth refresh requires an OAuth credential")
	}
	response, err := flow.postForm(ctx, flow.options.TokenURL, orderedForm(
		"grant_type", "refresh_token",
		"client_id", xAIClientID,
		"refresh_token", credential.Refresh,
	))
	if err != nil {
		return nil, err
	}
	if !response.ok {
		return nil, xAIRequestFailure("token refresh", response)
	}
	return flow.xaiCredential(response.body, credential.Refresh)
}

func (*XAI) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("xAI OAuth credential is required")
	}
	key := credential.Access
	return auth.ModelAuth{APIKey: &key}, nil
}

type xAIHTTPResponse struct {
	ok     bool
	status int
	body   map[string]any
}

type xAIDeviceCode struct {
	deviceCode              string
	userCode                string
	verificationURI         string
	verificationURIComplete string
	interval                float64
	expiresIn               float64
}

func parseXAIDeviceCode(body map[string]any) (xAIDeviceCode, error) {
	deviceCode, err := requiredXAIString(body, "device_code")
	if err != nil {
		return xAIDeviceCode{}, err
	}
	userCode, err := requiredXAIString(body, "user_code")
	if err != nil {
		return xAIDeviceCode{}, err
	}
	rawVerification, err := requiredXAIString(body, "verification_uri")
	if err != nil {
		return xAIDeviceCode{}, err
	}
	verificationURI, err := trustedVerificationURL(rawVerification, true)
	if err != nil {
		return xAIDeviceCode{}, errors.New("Untrusted verification URI in xAI OAuth response") //nolint:staticcheck // Upstream capitalization is observable.
	}
	verificationComplete := ""
	if raw, ok := body["verification_uri_complete"].(string); ok && raw != "" {
		verificationComplete, err = trustedVerificationURL(raw, true)
		if err != nil {
			return xAIDeviceCode{}, errors.New("Untrusted verification URI in xAI OAuth response") //nolint:staticcheck // Upstream capitalization is observable.
		}
	}
	expiresIn, err := positiveXAINumber(body, "expires_in")
	if err != nil {
		return xAIDeviceCode{}, err
	}
	interval := float64(0)
	if value, ok := body["interval"].(float64); ok && value > 0 && !mathInvalid(value) {
		interval = value
	}
	return xAIDeviceCode{deviceCode, userCode, verificationURI, verificationComplete, interval, expiresIn}, nil
}

func (flow *XAI) pollToken(ctx context.Context, deviceCode string) (deviceCodePollResult[*auth.Credential], error) {
	response, err := flow.postForm(ctx, flow.options.TokenURL, orderedForm(
		"grant_type", "urn:ietf:params:oauth:grant-type:device_code",
		"client_id", xAIClientID,
		"device_code", deviceCode,
	))
	if err != nil {
		return deviceCodePollResult[*auth.Credential]{}, err
	}
	if response.ok {
		credential, err := flow.xaiCredential(response.body, "")
		if err != nil {
			return deviceCodePollResult[*auth.Credential]{}, err
		}
		return deviceCodePollResult[*auth.Credential]{status: deviceCodeComplete, value: credential}, nil
	}
	errorCode, _ := response.body["error"].(string)
	switch errorCode {
	case "authorization_pending":
		return deviceCodePollResult[*auth.Credential]{status: deviceCodePending}, nil
	case "slow_down":
		var interval *float64
		if value, ok := response.body["interval"].(float64); ok {
			interval = &value
		}
		return deviceCodePollResult[*auth.Credential]{status: deviceCodeSlowDown, intervalSeconds: interval}, nil
	case "access_denied", "authorization_denied":
		return deviceCodePollResult[*auth.Credential]{status: deviceCodeFailed, message: "xAI device authorization was denied"}, nil
	case "expired_token":
		return deviceCodePollResult[*auth.Credential]{status: deviceCodeFailed, message: "xAI device code expired"}, nil
	default:
		return deviceCodePollResult[*auth.Credential]{status: deviceCodeFailed, message: xAIRequestFailure("device token polling", response).Error()}, nil
	}
}

func (flow *XAI) xaiCredential(body map[string]any, previousRefresh string) (*auth.Credential, error) {
	access, err := requiredXAIString(body, "access_token")
	if err != nil {
		return nil, err
	}
	refresh := previousRefresh
	if _, exists := body["refresh_token"]; exists || refresh == "" {
		refresh, err = requiredXAIString(body, "refresh_token")
		if err != nil {
			return nil, err
		}
	}
	expiresIn := float64(xAIDefaultLifetime)
	if _, exists := body["expires_in"]; exists {
		expiresIn, err = positiveXAINumber(body, "expires_in")
		if err != nil {
			return nil, err
		}
	}
	expires := flow.options.Now().UnixMilli() + int64(expiresIn*1000) - int64(xAIRefreshSkew/time.Millisecond)
	return auth.OAuthCredentialAccessFirst(access, refresh, expires), nil
}

func (flow *XAI) postForm(ctx context.Context, endpoint string, form []byte) (xAIHTTPResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(form))
	if err != nil {
		return xAIHTTPResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return xAIHTTPResponse{}, cancelledLoginError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return xAIHTTPResponse{}, err
	}
	var decoded any
	if json.Unmarshal(contents, &decoded) != nil {
		if ctx.Err() != nil {
			return xAIHTTPResponse{}, errors.New(deviceCodeCancelMessage)
		}
		return xAIHTTPResponse{}, fmt.Errorf("xAI OAuth returned invalid JSON (HTTP %d)", response.StatusCode)
	}
	body, ok := decoded.(map[string]any)
	if !ok {
		body = map[string]any{}
	}
	return xAIHTTPResponse{ok: response.StatusCode >= 200 && response.StatusCode < 300, status: response.StatusCode, body: body}, nil
}

func requiredXAIString(body map[string]any, field string) (string, error) {
	value, ok := body[field].(string)
	if !ok || value == "" {
		return "", fmt.Errorf("Invalid xAI OAuth response field: %s", field) //nolint:staticcheck // Upstream capitalization is observable.
	}
	return value, nil
}

func positiveXAINumber(body map[string]any, field string) (float64, error) {
	value, ok := body[field].(float64)
	if !ok || value <= 0 || mathInvalid(value) {
		return 0, fmt.Errorf("Invalid xAI OAuth response field: %s", field) //nolint:staticcheck // Upstream capitalization is observable.
	}
	return value, nil
}

func xAIRequestFailure(action string, response xAIHTTPResponse) error {
	errorCode, _ := response.body["error"].(string)
	description, _ := response.body["error_description"].(string)
	detail := errorCode
	if detail != "" && description != "" {
		detail += ": "
	}
	detail += description
	if detail != "" {
		detail = ": " + detail
	}
	return fmt.Errorf("xAI OAuth %s failed (HTTP %d)%s", action, response.status, detail)
}
