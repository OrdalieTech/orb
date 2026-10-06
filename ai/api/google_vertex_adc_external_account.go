package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/internal/lazyregexp"
)

const (
	googleVertexExternalAccountGrantType          = "urn:ietf:params:oauth:grant-type:token-exchange"
	googleVertexExternalAccountRequestedTokenType = "urn:ietf:params:oauth:token-type:access_token"
	googleVertexExternalAccountAuthVersion        = "10.6.2"
)

var googleVertexExternalAccountWorkforceAudiencePattern = lazyregexp.New(`//iam\.googleapis\.com/locations/[^/]+/workforcePools/[^/]+/providers/.+`)

type googleVertexExternalAccountConfig struct {
	Type                           string                                      `json:"type"`
	Audience                       string                                      `json:"audience"`
	SubjectTokenType               string                                      `json:"subject_token_type"`
	TokenURL                       *string                                     `json:"token_url"`
	UniverseDomain                 *string                                     `json:"universe_domain"`
	ClientID                       string                                      `json:"client_id"`
	ClientSecret                   string                                      `json:"client_secret"`
	WorkforcePoolUserProject       string                                      `json:"workforce_pool_user_project"`
	ServiceAccountImpersonationURL string                                      `json:"service_account_impersonation_url"`
	ServiceAccountImpersonation    googleVertexExternalAccountImpersonation    `json:"service_account_impersonation"`
	CredentialSource               googleVertexExternalAccountCredentialSource `json:"credential_source"`
}

type googleVertexExternalAccountImpersonation struct {
	TokenLifetimeSeconds int64 `json:"token_lifetime_seconds"`
}

type googleVertexExternalAccountCredentialSource struct {
	File                        string                                        `json:"file"`
	URL                         string                                        `json:"url"`
	Headers                     map[string]string                             `json:"headers"`
	Format                      googleVertexExternalAccountSubjectFormat      `json:"format"`
	Certificate                 *googleVertexExternalAccountCertificateSource `json:"certificate"`
	Executable                  *googleVertexExternalAccountExecutableSource  `json:"executable"`
	EnvironmentID               string                                        `json:"environment_id"`
	RegionURL                   string                                        `json:"region_url"`
	RegionalCredVerificationURL string                                        `json:"regional_cred_verification_url"`
	IMDSv2SessionTokenURL       string                                        `json:"imdsv2_session_token_url"`
}

type googleVertexExternalAccountSubjectFormat struct {
	Type                  string `json:"type"`
	SubjectTokenFieldName string `json:"subject_token_field_name"`
}

type googleVertexExternalAccountCertificateSource struct {
	UseDefaultCertificateConfig bool   `json:"use_default_certificate_config"`
	CertificateConfigLocation   string `json:"certificate_config_location"`
	TrustChainPath              string `json:"trust_chain_path"`
}

type googleVertexExternalAccountExecutableSource struct {
	Command       string `json:"command"`
	TimeoutMillis *int64 `json:"timeout_millis"`
	OutputFile    string `json:"output_file"`
}

type googleVertexExternalAccountIAMResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireTime  string `json:"expireTime"`
}

func (adc *googleVertexADC) externalAccountToken(ctx context.Context, raw json.RawMessage) (googleVertexTokenResponse, error) {
	var config googleVertexExternalAccountConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("decode external_account ADC: %w", err)
	}
	if config.Type != "" && config.Type != "external_account" {
		return googleVertexTokenResponse{}, fmt.Errorf("expected external_account credential type, got %q", config.Type)
	}
	if config.WorkforcePoolUserProject != "" && !googleVertexExternalAccountWorkforceAudience(config.Audience) {
		//nolint:staticcheck // The capitalization and punctuation are observable google-auth-library behavior.
		return googleVertexTokenResponse{}, errors.New("workforcePoolUserProject should not be set for non-workforce pool credentials.")
	}

	subjectToken, sourceType, err := adc.externalAccountSubjectToken(ctx, &config)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	stsToken, err := adc.exchangeExternalAccountSTS(ctx, &config, subjectToken, sourceType)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	if config.ServiceAccountImpersonationURL == "" {
		return stsToken, nil
	}
	return adc.impersonateExternalAccountServiceAccount(ctx, &config, stsToken.AccessToken)
}

func googleVertexExternalAccountWorkforceAudience(audience string) bool {
	return googleVertexExternalAccountWorkforceAudiencePattern().MatchString(audience)
}

func (adc *googleVertexADC) externalAccountSubjectToken(ctx context.Context, config *googleVertexExternalAccountConfig) (string, string, error) {
	source := &config.CredentialSource
	switch {
	case source.EnvironmentID != "":
		token, err := adc.externalAccountAWSSubjectToken(ctx, config)
		return token, "aws", err
	case source.Executable != nil:
		token, err := adc.externalAccountExecutableSubjectToken(ctx, config)
		return token, "executable", err
	default:
		return adc.externalAccountIdentitySubjectToken(ctx, config)
	}
}

func (config *googleVertexExternalAccountConfig) externalAccountTokenURL() string {
	if config.TokenURL != nil {
		return *config.TokenURL
	}
	domain := "googleapis.com"
	if config.UniverseDomain != nil {
		domain = *config.UniverseDomain
	}
	return "https://sts." + domain + "/v1/token"
}

func (adc *googleVertexADC) exchangeExternalAccountSTS(
	ctx context.Context,
	config *googleVertexExternalAccountConfig,
	subjectToken string,
	sourceType string,
) (googleVertexTokenResponse, error) {
	values := [][2]string{
		{"grant_type", googleVertexExternalAccountGrantType},
		{"audience", config.Audience},
		{"scope", googleVertexCloudScope},
		{"requested_token_type", googleVertexExternalAccountRequestedTokenType},
		{"subject_token", subjectToken},
		{"subject_token_type", config.SubjectTokenType},
	}
	if config.ClientID == "" && config.WorkforcePoolUserProject != "" {
		options, err := ai.Marshal(struct {
			UserProject string `json:"userProject"`
		}{UserProject: config.WorkforcePoolUserProject})
		if err != nil {
			return googleVertexTokenResponse{}, err
		}
		values = append(values, [2]string{"options", string(options)})
	}
	header, body := googleVertexForm(values...)
	header.Set("X-Goog-Api-Client", googleVertexExternalAccountMetrics(sourceType, config.ServiceAccountImpersonationURL != "", config.ServiceAccountImpersonation.TokenLifetimeSeconds != 0))
	if config.ClientID != "" {
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(config.ClientID+":"+config.ClientSecret)))
	}
	var token googleVertexTokenResponse
	err := adc.postJSON(ctx, config.externalAccountTokenURL(), body, header, &token)
	return token, err
}

func googleVertexExternalAccountMetrics(sourceType string, impersonation, configuredLifetime bool) string {
	version := strings.TrimPrefix(runtime.Version(), "go")
	return "gl-go/" + version + " auth/" + googleVertexExternalAccountAuthVersion +
		" google-byoid-sdk source/" + sourceType +
		" sa-impersonation/" + strconv.FormatBool(impersonation) +
		" config-lifetime/" + strconv.FormatBool(configuredLifetime)
}

func (adc *googleVertexADC) impersonateExternalAccountServiceAccount(
	ctx context.Context,
	config *googleVertexExternalAccountConfig,
	stsAccessToken string,
) (googleVertexTokenResponse, error) {
	lifetime := config.ServiceAccountImpersonation.TokenLifetimeSeconds
	if lifetime == 0 {
		lifetime = 3600
	}
	body, err := ai.Marshal(struct {
		Scope    []string `json:"scope"`
		Lifetime string   `json:"lifetime"`
	}{Scope: []string{googleVertexCloudScope}, Lifetime: strconv.FormatInt(lifetime, 10) + "s"})
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	var token googleVertexExternalAccountIAMResponse
	header := http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + stsAccessToken}}
	if err := adc.postJSON(ctx, config.ServiceAccountImpersonationURL, body, header, &token); err != nil {
		return googleVertexTokenResponse{}, err
	}
	expires, err := time.Parse(time.RFC3339Nano, token.ExpireTime)
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("parse service account impersonation expireTime: %w", err)
	}
	expiresIn := int64(expires.Sub(adc.now()).Seconds())
	return googleVertexTokenResponse{AccessToken: token.AccessToken, ExpiresIn: expiresIn, TokenType: "Bearer"}, nil
}
