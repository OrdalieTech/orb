package oauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/pi-go/ai/auth"
	aimodels "github.com/OrdalieTech/pi-go/ai/models"
)

const (
	githubCopilotUserAgent  = "GitHubCopilotChat/0.35.0"
	githubCopilotAPIVersion = "2026-06-01"
)

var (
	githubCopilotClientID = mustDecodeBase64("SXYxLmI1MDdhMDhjODdlY2ZlOTg=")
	copilotProxyEndpoint  = regexp.MustCompile(`proxy-ep=([^;]+)`)
)

type GitHubCopilotOptions struct {
	DeviceCodeURL   string
	AccessTokenURL  string
	CopilotTokenURL string
	CopilotBaseURL  string
	HTTPClient      *http.Client
	KnownModelIDs   []string
	Sleep           func(context.Context, time.Duration) error
}

type GitHubCopilot struct{ options GitHubCopilotOptions }

func NewGitHubCopilot(options *GitHubCopilotOptions) *GitHubCopilot {
	configured := GitHubCopilotOptions{}
	if options != nil {
		configured = *options
	}
	if configured.HTTPClient == nil {
		configured.HTTPClient = http.DefaultClient
	}
	if configured.KnownModelIDs == nil {
		configured.KnownModelIDs = builtInCopilotModelIDs()
	} else {
		configured.KnownModelIDs = append([]string(nil), configured.KnownModelIDs...)
	}
	return &GitHubCopilot{options: configured}
}

func (*GitHubCopilot) Name() string { return "GitHub Copilot" }

func (flow *GitHubCopilot) Login(ctx context.Context, interaction auth.AuthInteraction) (*auth.Credential, error) {
	input, err := interaction.Prompt(ctx, auth.AuthPrompt{
		Type: auth.PromptText, Message: "GitHub Enterprise URL/domain (blank for github.com)", Placeholder: "company.ghe.com",
	})
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, errors.New(deviceCodeCancelMessage)
	}
	trimmed := strings.TrimSpace(input)
	enterpriseDomain := normalizeGitHubDomain(input)
	if trimmed != "" && enterpriseDomain == "" {
		return nil, errors.New("Invalid GitHub Enterprise URL/domain") //nolint:staticcheck // Upstream capitalization is observable.
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	device, err := flow.startDeviceFlow(ctx, domain)
	if err != nil {
		return nil, err
	}
	interaction.Notify(auth.AuthEvent{
		Type: auth.EventDeviceCode, UserCode: device.userCode, VerificationURI: device.verificationURI,
		IntervalSeconds: githubDeviceInterval(device), ExpiresInSeconds: int(device.expiresIn),
	})
	githubToken, err := flow.pollAccessToken(ctx, domain, device)
	if err != nil {
		return nil, err
	}
	credential, err := flow.exchangeCopilotToken(ctx, githubToken, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	interaction.Notify(auth.AuthEvent{Type: auth.EventProgress, Message: "Enabling models..."})
	flow.enableAllModels(ctx, credential.Access, enterpriseDomain)
	available, err := flow.fetchAvailableModels(ctx, credential.Access, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	setCredentialJSON(credential, "availableModelIds", available)
	return credential, nil
}

func (flow *GitHubCopilot) Refresh(ctx context.Context, credential *auth.Credential) (*auth.Credential, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, errors.New("GitHub Copilot OAuth refresh requires an OAuth credential")
	}
	enterpriseDomain := copilotEnterpriseDomain(credential)
	refreshed, err := flow.exchangeCopilotToken(ctx, credential.Refresh, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	available, err := flow.fetchAvailableModels(ctx, refreshed.Access, enterpriseDomain)
	if err != nil {
		return nil, err
	}
	setCredentialJSON(refreshed, "availableModelIds", available)
	return refreshed, nil
}

func (*GitHubCopilot) ToAuth(credential *auth.Credential) (auth.ModelAuth, error) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return auth.ModelAuth{}, errors.New("GitHub Copilot OAuth credential is required")
	}
	key := credential.Access
	baseURL := GitHubCopilotBaseURL(credential.Access, copilotEnterpriseDomain(credential))
	return auth.ModelAuth{APIKey: &key, BaseURL: &baseURL}, nil
}

type githubDeviceCode struct {
	deviceCode      string
	userCode        string
	verificationURI string
	interval        *float64
	expiresIn       float64
}

func (flow *GitHubCopilot) startDeviceFlow(ctx context.Context, domain string) (githubDeviceCode, error) {
	response, err := flow.fetchJSON(ctx, http.MethodPost, flow.deviceCodeURL(domain), orderedForm(
		"client_id", githubCopilotClientID,
		"scope", "read:user",
	), true, nil)
	if err != nil {
		return githubDeviceCode{}, err
	}
	deviceCode, deviceOK := response["device_code"].(string)
	userCode, userOK := response["user_code"].(string)
	verificationURI, uriOK := response["verification_uri"].(string)
	expiresIn, expiresOK := response["expires_in"].(float64)
	var interval *float64
	if raw, exists := response["interval"]; exists {
		value, intervalOK := raw.(float64)
		if !intervalOK {
			return githubDeviceCode{}, errors.New("Invalid device code response fields") //nolint:staticcheck // Upstream capitalization is observable.
		}
		interval = &value
	}
	if !deviceOK || !userOK || !uriOK || !expiresOK {
		return githubDeviceCode{}, errors.New("Invalid device code response fields") //nolint:staticcheck // Upstream capitalization is observable.
	}
	trusted, err := trustedVerificationURL(verificationURI, false)
	if err != nil {
		return githubDeviceCode{}, errors.New("Untrusted verification_uri in device code response") //nolint:staticcheck // Upstream capitalization is observable.
	}
	return githubDeviceCode{deviceCode, userCode, trusted, interval, expiresIn}, nil
}

func (flow *GitHubCopilot) pollAccessToken(ctx context.Context, domain string, device githubDeviceCode) (string, error) {
	expires := device.expiresIn
	return pollOAuthDeviceCodeFlow(deviceCodePollOptions[string]{
		intervalSeconds:  device.interval,
		expiresInSeconds: &expires,
		waitBeforeFirst:  true,
		ctx:              ctx,
		sleep:            flow.options.Sleep,
		poll: func() (deviceCodePollResult[string], error) {
			response, err := flow.fetchJSON(ctx, http.MethodPost, flow.accessTokenURL(domain), orderedForm(
				"client_id", githubCopilotClientID,
				"device_code", device.deviceCode,
				"grant_type", "urn:ietf:params:oauth:grant-type:device_code",
			), true, nil)
			if err != nil {
				return deviceCodePollResult[string]{}, err
			}
			if access, ok := response["access_token"].(string); ok {
				return deviceCodePollResult[string]{status: deviceCodeComplete, value: access}, nil
			}
			errorCode, ok := response["error"].(string)
			if !ok {
				return deviceCodePollResult[string]{status: deviceCodeFailed, message: "Invalid device token response"}, nil
			}
			switch errorCode {
			case "authorization_pending":
				return deviceCodePollResult[string]{status: deviceCodePending}, nil
			case "slow_down":
				var serverInterval *float64
				if value, ok := response["interval"].(float64); ok {
					serverInterval = &value
				}
				return deviceCodePollResult[string]{status: deviceCodeSlowDown, intervalSeconds: serverInterval}, nil
			default:
				description, _ := response["error_description"].(string)
				if description != "" {
					description = ": " + description
				}
				return deviceCodePollResult[string]{status: deviceCodeFailed, message: "Device flow failed: " + errorCode + description}, nil
			}
		},
	})
}

func githubDeviceInterval(device githubDeviceCode) int {
	if device.interval == nil {
		return 0
	}
	return int(*device.interval)
}

func (flow *GitHubCopilot) exchangeCopilotToken(ctx context.Context, refreshToken, enterpriseDomain string) (*auth.Credential, error) {
	headers := githubCopilotStaticHeaders()
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+refreshToken)
	response, err := flow.fetchJSON(ctx, http.MethodGet, flow.copilotTokenURL(enterpriseDomain), nil, false, headers)
	if err != nil {
		return nil, err
	}
	token, tokenOK := response["token"].(string)
	expiresAt, expiresOK := response["expires_at"].(float64)
	if !tokenOK || !expiresOK {
		return nil, errors.New("Invalid Copilot token response fields") //nolint:staticcheck // Upstream capitalization is observable.
	}
	credential := auth.OAuthCredential(refreshToken, token, int64(expiresAt*1000)-int64(5*time.Minute/time.Millisecond))
	if enterpriseDomain != "" {
		setCredentialJSON(credential, "enterpriseUrl", enterpriseDomain)
	}
	return credential, nil
}

func (flow *GitHubCopilot) fetchAvailableModels(ctx context.Context, token, enterpriseDomain string) ([]string, error) {
	modelCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	headers := githubCopilotStaticHeaders()
	headers.Set("Accept", "application/json")
	headers.Set("Authorization", "Bearer "+token)
	headers.Set("X-GitHub-Api-Version", githubCopilotAPIVersion)
	response, err := flow.fetchJSON(modelCtx, http.MethodGet, flow.copilotBaseURL(token, enterpriseDomain)+"/models", nil, false, headers)
	if err != nil {
		return nil, err
	}
	raw, ok := response["data"].([]any)
	if !ok {
		return nil, errors.New("Invalid Copilot models response") //nolint:staticcheck // Upstream capitalization is observable.
	}
	result := make([]string, 0)
	for _, value := range raw {
		item, ok := value.(map[string]any)
		if !ok || !selectableCopilotModel(item) {
			continue
		}
		if id, ok := item["id"].(string); ok {
			result = append(result, id)
		}
	}
	return result, nil
}

func selectableCopilotModel(item map[string]any) bool {
	enabled, _ := item["model_picker_enabled"].(bool)
	policy, _ := item["policy"].(map[string]any)
	capabilities, _ := item["capabilities"].(map[string]any)
	supports, _ := capabilities["supports"].(map[string]any)
	if policy["state"] == "disabled" || supports["tool_calls"] == false {
		return false
	}
	return enabled
}

func (flow *GitHubCopilot) enableAllModels(ctx context.Context, token, enterpriseDomain string) {
	var wait sync.WaitGroup
	for _, modelID := range flow.options.KnownModelIDs {
		wait.Add(1)
		go func() {
			defer wait.Done()
			headers := githubCopilotStaticHeaders()
			headers.Set("Content-Type", "application/json")
			headers.Set("Authorization", "Bearer "+token)
			headers.Set("openai-intent", "chat-policy")
			headers.Set("x-interaction-type", "chat-policy")
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, flow.copilotBaseURL(token, enterpriseDomain)+"/models/"+url.PathEscape(modelID)+"/policy", strings.NewReader(`{"state":"enabled"}`))
			if err != nil {
				return
			}
			request.Header = headers
			response, err := flow.options.HTTPClient.Do(request)
			if err == nil {
				_ = response.Body.Close()
			}
		}()
	}
	wait.Wait()
}

func (flow *GitHubCopilot) fetchJSON(ctx context.Context, method, endpoint string, body []byte, form bool, headers http.Header) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if headers != nil {
		request.Header = headers.Clone()
	}
	if form {
		request.Header.Set("Accept", "application/json")
		request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		request.Header.Set("User-Agent", githubCopilotUserAgent)
	}
	response, err := flow.options.HTTPClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	contents, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("%s: %s", response.Status, contents)
	}
	var result map[string]any
	if json.Unmarshal(contents, &result) != nil || result == nil {
		return nil, errors.New("Invalid JSON response") //nolint:staticcheck // Upstream fetch.json rejection remains observable via the native message.
	}
	return result, nil
}

func githubCopilotStaticHeaders() http.Header {
	return http.Header{
		"User-Agent":             []string{githubCopilotUserAgent},
		"Editor-Version":         []string{"vscode/1.107.0"},
		"Editor-Plugin-Version":  []string{"copilot-chat/0.35.0"},
		"Copilot-Integration-Id": []string{"vscode-chat"},
	}
}

func normalizeGitHubDomain(input string) string {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return ""
	}
	if !strings.Contains(trimmed, "://") {
		trimmed = "https://" + trimmed
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

func GitHubCopilotBaseURL(token, enterpriseDomain string) string {
	if match := copilotProxyEndpoint.FindStringSubmatch(token); len(match) == 2 {
		host := match[1]
		if strings.HasPrefix(host, "proxy.") {
			host = "api." + strings.TrimPrefix(host, "proxy.")
		}
		return "https://" + host
	}
	if enterpriseDomain != "" {
		return "https://copilot-api." + enterpriseDomain
	}
	return "https://api.individual.githubcopilot.com"
}

func (flow *GitHubCopilot) deviceCodeURL(domain string) string {
	if flow.options.DeviceCodeURL != "" {
		return flow.options.DeviceCodeURL
	}
	return "https://" + domain + "/login/device/code"
}

func (flow *GitHubCopilot) accessTokenURL(domain string) string {
	if flow.options.AccessTokenURL != "" {
		return flow.options.AccessTokenURL
	}
	return "https://" + domain + "/login/oauth/access_token"
}

func (flow *GitHubCopilot) copilotTokenURL(enterpriseDomain string) string {
	if flow.options.CopilotTokenURL != "" {
		return flow.options.CopilotTokenURL
	}
	domain := enterpriseDomain
	if domain == "" {
		domain = "github.com"
	}
	return "https://api." + domain + "/copilot_internal/v2/token"
}

func (flow *GitHubCopilot) copilotBaseURL(token, enterpriseDomain string) string {
	if flow.options.CopilotBaseURL != "" {
		return strings.TrimRight(flow.options.CopilotBaseURL, "/")
	}
	return GitHubCopilotBaseURL(token, enterpriseDomain)
}

func copilotEnterpriseDomain(credential *auth.Credential) string {
	if credential == nil {
		return ""
	}
	var value string
	if json.Unmarshal(credential.Extra["enterpriseUrl"], &value) != nil {
		return ""
	}
	return normalizeGitHubDomain(value)
}

func CopilotAvailableModelIDs(credential *auth.Credential) ([]string, bool) {
	if credential == nil || credential.Type != auth.CredentialOAuth {
		return nil, false
	}
	raw := bytes.TrimSpace(credential.Extra["availableModelIds"])
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false
	}
	var ids []string
	if json.Unmarshal(raw, &ids) != nil {
		return nil, false
	}
	return ids, true
}

func trustedVerificationURL(raw string, requireHTTPS bool) (string, error) {
	parsed, err := url.Parse(normalizeWHATWGURLInput(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && (requireHTTPS || parsed.Scheme != "http")) {
		return "", errors.New("untrusted URL")
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), nil
}

func normalizeWHATWGURLInput(raw string) string {
	raw = strings.TrimFunc(raw, func(value rune) bool { return value <= ' ' })
	var normalized strings.Builder
	for index := 0; index < len(raw); index++ {
		value := raw[index]
		switch value {
		case '\t', '\n', '\r':
			continue
		}
		if value < 0x20 || value == 0x7f {
			const hex = "0123456789ABCDEF"
			normalized.WriteByte('%')
			normalized.WriteByte(hex[value>>4])
			normalized.WriteByte(hex[value&0x0f])
			continue
		}
		normalized.WriteByte(value)
	}
	return normalized.String()
}

func setCredentialJSON(credential *auth.Credential, name string, value any) {
	encoded, _ := json.Marshal(value)
	credential.SetExtra(name, encoded)
}

func builtInCopilotModelIDs() []string {
	catalog, err := aimodels.Builtin()
	if err != nil {
		return nil
	}
	models := catalog.Models("github-copilot")
	result := make([]string, len(models))
	for index, model := range models {
		result[index] = model.ID
	}
	return result
}

func mustDecodeBase64(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		panic(err)
	}
	return string(decoded)
}
