package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

// KimiCoding ports upstream kimiCodingOAuth (ai/src/auth/oauth/kimi-coding.ts):
// an RFC 8628 device authorization grant against https://auth.kimi.com with
// JSON responses. The access token authenticates requests to
// https://api.kimi.com/coding as an `Authorization: Bearer` header.
const (
	kimiCodingClientID              = "17e5f671-d194-4dfb-9706-5516cb48c098"
	kimiCodingDefaultOAuthHost      = "https://auth.kimi.com"
	kimiCodingDeviceTimeoutSeconds  = 15 * 60
	kimiCodingDefaultPollSeconds    = 5
	kimiCodingRequestTimeout        = 30 * time.Second
	kimiCodingRefreshMaxRetries     = 3
	kimiCodingRefreshAbortedMessage = "Kimi Code token refresh aborted"
)

type KimiCodingOptions struct {
	OAuthHost  string
	HTTPClient *http.Client
	Now        func() time.Time
	Sleep      func(context.Context, time.Duration) error
}

type KimiCoding struct{ options KimiCodingOptions }

func NewKimiCoding(options *KimiCodingOptions) *KimiCoding {
	configured := KimiCodingOptions{}
	if options != nil {
		configured = *options
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = http.DefaultClient
	}
	if configured.Now == nil {
		configured.Now = time.Now
	}
	if configured.Sleep == nil {
		configured.Sleep = sleepDeviceCode
	}
	return &KimiCoding{options: configured}
}

func (*KimiCoding) Name() string { return "Kimi Code (subscription)" }

func (*KimiCoding) LoginLabel() string { return "Sign in with Kimi Code" }

// oauthHost mirrors upstream getOauthHost(): KIMI_CODE_OAUTH_HOST /
// KIMI_OAUTH_HOST override the default, with trailing slashes stripped.
func (flow *KimiCoding) oauthHost() string {
	override := flow.options.OAuthHost
	if override == "" {
		override = os.Getenv("KIMI_CODE_OAUTH_HOST")
	}
	if override == "" {
		override = os.Getenv("KIMI_OAUTH_HOST")
	}
	if override == "" {
		override = kimiCodingDefaultOAuthHost
	}
	return strings.TrimRight(override, "/")
}

func (flow *KimiCoding) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	oauthHost := flow.oauthHost()
	device, err := flow.startDeviceAuthorization(ctx, oauthHost)
	if err != nil {
		return nil, err
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventDeviceCode, UserCode: device.userCode, VerificationURI: device.verificationURIComplete,
		IntervalSeconds: int(device.intervalSeconds), ExpiresInSeconds: int(device.expiresInSeconds),
	})
	token, err := flow.pollForToken(ctx, oauthHost, device)
	if err != nil {
		return nil, err
	}
	return auth.OAuthCredentialAccessFirst(token.access, token.refresh, token.expires), nil
}

func (flow *KimiCoding) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("Kimi Code OAuth refresh requires an OAuth credential")
	}
	token, err := flow.refreshToken(ctx, flow.oauthHost(), credential.Refresh)
	if err != nil {
		return nil, err
	}
	return auth.OAuthCredentialAccessFirst(token.access, token.refresh, token.expires), nil
}

func (*KimiCoding) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("Kimi Code OAuth credential is required")
	}
	bearer := "Bearer " + credential.Access
	return auth.ModelAuth{Headers: map[string]*string{"Authorization": &bearer}}, nil
}

type kimiDeviceAuthorization struct {
	deviceCode              string
	userCode                string
	verificationURI         string
	verificationURIComplete string
	intervalSeconds         float64
	expiresInSeconds        float64
}

type kimiTokenResponse struct {
	access  string
	refresh string
	expires int64
}

func (flow *KimiCoding) startDeviceAuthorization(ctx context.Context, oauthHost string) (kimiDeviceAuthorization, error) {
	status, contents, err := flow.postForm(ctx, oauthHost+"/api/oauth/device_authorization", orderedForm(
		"client_id", kimiCodingClientID,
	))
	if err != nil {
		return kimiDeviceAuthorization{}, err
	}
	if status < 200 || status >= 300 {
		return kimiDeviceAuthorization{}, fmt.Errorf("Kimi Code device authorization failed with status %d%s", status, responseBodySuffix(contents)) //nolint:staticcheck // Exact upstream error text is observable.
	}
	body, raw := kimiReadJSON(contents)
	deviceCode, deviceOK := body["device_code"].(string)
	userCode, userOK := body["user_code"].(string)
	verificationURI, uriOK := body["verification_uri"].(string)
	verificationURIComplete, completeOK := body["verification_uri_complete"].(string)
	if !deviceOK || !userOK || !uriOK || !completeOK ||
		!kimiTrustedHTTPURL(verificationURIComplete) || !kimiTrustedHTTPURL(verificationURI) {
		return kimiDeviceAuthorization{}, fmt.Errorf("Invalid Kimi Code device authorization response: %s", kimiStringify(raw)) //nolint:staticcheck // Upstream capitalization is observable.
	}
	intervalSeconds := float64(kimiCodingDefaultPollSeconds)
	if interval, ok := body["interval"].(float64); ok && interval > 0 && !mathInvalid(interval) {
		intervalSeconds = interval
	}
	expiresInSeconds := float64(kimiCodingDeviceTimeoutSeconds)
	if expiresIn, ok := body["expires_in"].(float64); ok && expiresIn > 0 && !mathInvalid(expiresIn) {
		expiresInSeconds = expiresIn
	}
	return kimiDeviceAuthorization{
		deviceCode:              deviceCode,
		userCode:                userCode,
		verificationURI:         verificationURI,
		verificationURIComplete: verificationURIComplete,
		intervalSeconds:         intervalSeconds,
		expiresInSeconds:        expiresInSeconds,
	}, nil
}

func (flow *KimiCoding) pollForToken(ctx context.Context, oauthHost string, device kimiDeviceAuthorization) (kimiTokenResponse, error) {
	interval, expires := device.intervalSeconds, device.expiresInSeconds
	return pollOAuthDeviceCodeFlow(deviceCodePollOptions[kimiTokenResponse]{
		intervalSeconds:  &interval,
		expiresInSeconds: &expires,
		waitBeforeFirst:  true,
		ctx:              ctx,
		now:              flow.options.Now,
		sleep:            flow.options.Sleep,
		poll: func() (deviceCodePollResult[kimiTokenResponse], error) {
			status, contents, err := flow.postForm(ctx, oauthHost+"/api/oauth/token", orderedForm(
				"client_id", kimiCodingClientID,
				"device_code", device.deviceCode,
				"grant_type", "urn:ietf:params:oauth:grant-type:device_code",
			))
			if err != nil {
				return deviceCodePollResult[kimiTokenResponse]{}, err
			}
			if status >= 500 {
				return deviceCodePollResult[kimiTokenResponse]{
					status:  deviceCodeFailed,
					message: fmt.Sprintf("Kimi Code device token request failed with status %d%s", status, responseBodySuffix(contents)),
				}, nil
			}
			body, raw := kimiReadJSON(contents)
			if status >= 200 && status < 300 {
				if _, ok := body["access_token"].(string); ok {
					token, parseErr := flow.parseTokenResponse(body, raw, "poll")
					if parseErr != nil {
						return deviceCodePollResult[kimiTokenResponse]{status: deviceCodeFailed, message: parseErr.Error()}, nil
					}
					return deviceCodePollResult[kimiTokenResponse]{status: deviceCodeComplete, value: token}, nil
				}
			}
			errorCode, errorIsString := body["error"].(string)
			description := ""
			if value, ok := body["error_description"].(string); ok {
				description = ": " + value
			}
			switch errorCode {
			case "authorization_pending":
				return deviceCodePollResult[kimiTokenResponse]{status: deviceCodePending}, nil
			case "slow_down":
				var serverInterval *float64
				if value, ok := body["interval"].(float64); ok && value > 0 {
					serverInterval = &value
				}
				return deviceCodePollResult[kimiTokenResponse]{status: deviceCodeSlowDown, intervalSeconds: serverInterval}, nil
			case "expired_token":
				return deviceCodePollResult[kimiTokenResponse]{status: deviceCodeFailed, message: "Kimi Code device authorization expired. Please restart login."}, nil
			case "access_denied":
				return deviceCodePollResult[kimiTokenResponse]{status: deviceCodeFailed, message: "Kimi Code login was denied."}, nil
			}
			suffix := ""
			if errorIsString {
				suffix = ": " + errorCode + description
			}
			return deviceCodePollResult[kimiTokenResponse]{
				status:  deviceCodeFailed,
				message: fmt.Sprintf("Kimi Code device token request failed (status %d)%s", status, suffix),
			}, nil
		},
	})
}

func kimiRetryableRefreshFailure(status int) bool {
	return status == http.StatusTooManyRequests || status >= 500
}

func (flow *KimiCoding) refreshToken(ctx context.Context, oauthHost, refreshTokenValue string) (kimiTokenResponse, error) {
	var lastError error
	for attempt := 0; attempt <= kimiCodingRefreshMaxRetries; attempt++ {
		if attempt > 0 {
			_ = flow.options.Sleep(ctx, time.Duration(1000*(1<<(attempt-1)))*time.Millisecond)
		}
		if ctx.Err() != nil {
			return kimiTokenResponse{}, errors.New(kimiCodingRefreshAbortedMessage)
		}

		status, contents, err := flow.postForm(ctx, oauthHost+"/api/oauth/token", orderedForm(
			"client_id", kimiCodingClientID,
			"grant_type", "refresh_token",
			"refresh_token", refreshTokenValue,
		))
		if err != nil {
			lastError = err
			continue
		}

		body, raw := kimiReadJSON(contents)
		if status >= 200 && status < 300 {
			return flow.parseTokenResponse(body, raw, "refresh")
		}

		// Unauthorized: the stored credential is dead; the resolver clears it
		// and prompts re-login.
		if status == http.StatusUnauthorized || status == http.StatusForbidden || body["error"] == "invalid_grant" {
			description := ""
			if value, ok := body["error_description"].(string); ok {
				description = ": " + value
			}
			return kimiTokenResponse{}, fmt.Errorf("Kimi Code token refresh unauthorized (status %d)%s", status, description) //nolint:staticcheck // Exact upstream error text is observable.
		}

		if kimiRetryableRefreshFailure(status) && attempt < kimiCodingRefreshMaxRetries {
			lastError = fmt.Errorf("Kimi Code token refresh failed with status %d", status) //nolint:staticcheck // Exact upstream error text is observable.
			continue
		}

		return kimiTokenResponse{}, fmt.Errorf("Kimi Code token refresh failed with status %d: %s", status, kimiStringify(raw)) //nolint:staticcheck // Exact upstream error text is observable.
	}

	if lastError != nil {
		return kimiTokenResponse{}, lastError
	}
	return kimiTokenResponse{}, errors.New("Kimi Code token refresh failed") //nolint:staticcheck // Exact upstream error text is observable.
}

func (flow *KimiCoding) parseTokenResponse(body map[string]any, raw any, operation string) (kimiTokenResponse, error) {
	access, accessOK := body["access_token"].(string)
	refresh, refreshOK := body["refresh_token"].(string)
	expiresIn, expiresOK := body["expires_in"].(float64)
	if !accessOK || access == "" || !refreshOK || refresh == "" || !expiresOK || expiresIn <= 0 || mathInvalid(expiresIn) {
		return kimiTokenResponse{}, fmt.Errorf("Kimi Code token %s response missing fields: %s", operation, kimiStringify(raw)) //nolint:staticcheck // Exact upstream error text is observable.
	}
	return kimiTokenResponse{
		access:  access,
		refresh: refresh,
		expires: flow.options.Now().UnixMilli() + int64(expiresIn*1000),
	}, nil
}

func (flow *KimiCoding) postForm(ctx context.Context, endpoint string, form []byte) (int, []byte, error) {
	requestCtx, cancel := context.WithTimeout(ctx, kimiCodingRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(form))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("Accept", "application/json")
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return 0, nil, cancelledLoginError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return 0, nil, cancelledLoginError(ctx, err)
	}
	return response.StatusCode, contents, nil
}

// kimiReadJSON mirrors upstream readJson(): any JSON object (including arrays)
// parses; other values behave as null. The second return value preserves the
// parsed document for JSON.stringify-style error text.
func kimiReadJSON(contents []byte) (map[string]any, any) {
	var parsed any
	if json.Unmarshal(contents, &parsed) != nil {
		return map[string]any{}, nil
	}
	switch typed := parsed.(type) {
	case map[string]any:
		return typed, typed
	case []any:
		return map[string]any{}, typed
	default:
		return map[string]any{}, nil
	}
}

func kimiStringify(raw any) string {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

// kimiTrustedHTTPURL mirrors upstream trustedHttpUrl(): the verification URI
// is opened in the user's browser; only http(s) URLs are trusted.
func kimiTrustedHTTPURL(value string) bool {
	if value == "" {
		return false
	}
	_, err := trustedVerificationURL(value, false)
	return err == nil
}
