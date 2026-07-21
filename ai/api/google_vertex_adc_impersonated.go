package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"

	"github.com/OrdalieTech/pigo/ai"
)

const googleVertexDefaultUniverseDomain = "googleapis.com"

var googleVertexImpersonatedPrincipalPattern = regexp.MustCompile(`([^/]+):(generateAccessToken|generateIdToken)$`)

type googleVertexImpersonatedADCFile struct {
	SourceCredentials              json.RawMessage `json:"source_credentials"`
	ServiceAccountImpersonationURL string          `json:"service_account_impersonation_url"`
	Delegates                      []string        `json:"delegates"`
	Lifetime                       *int64          `json:"lifetime"`
	Endpoint                       *string         `json:"endpoint"`
	UniverseDomain                 string          `json:"universe_domain"`
}

type googleVertexImpersonatedRequest struct {
	Delegates []string `json:"delegates"`
	Scope     []string `json:"scope"`
	Lifetime  string   `json:"lifetime"`
}

type googleVertexImpersonatedResponse struct {
	AccessToken string `json:"accessToken"`
	ExpireTime  string `json:"expireTime"`
}

func (adc *googleVertexADC) impersonatedServiceAccountToken(ctx context.Context, raw json.RawMessage) (googleVertexTokenResponse, error) {
	var credential googleVertexImpersonatedADCFile
	if err := json.Unmarshal(raw, &credential); err != nil {
		return googleVertexTokenResponse{}, err
	}
	if len(bytes.TrimSpace(credential.SourceCredentials)) == 0 || bytes.Equal(bytes.TrimSpace(credential.SourceCredentials), []byte("null")) {
		return googleVertexTokenResponse{}, errors.New("The incoming JSON object does not contain a source_credentials field") //nolint:staticcheck // Exact upstream text.
	}
	if credential.ServiceAccountImpersonationURL == "" {
		return googleVertexTokenResponse{}, errors.New("The incoming JSON object does not contain a service_account_impersonation_url field") //nolint:staticcheck // Exact upstream text.
	}
	sourceCredential, err := decodeGoogleVertexADCFile(credential.SourceCredentials)
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}
	if googleVertexJavaScriptStringLength(credential.ServiceAccountImpersonationURL) > 256 {
		return googleVertexTokenResponse{}, fmt.Errorf("Target principal is too long: %s", credential.ServiceAccountImpersonationURL) //nolint:staticcheck // Exact upstream prefix.
	}
	match := googleVertexImpersonatedPrincipalPattern.FindStringSubmatch(credential.ServiceAccountImpersonationURL)
	if len(match) == 0 {
		return googleVertexTokenResponse{}, fmt.Errorf("Cannot extract target principal from %s", credential.ServiceAccountImpersonationURL) //nolint:staticcheck // Exact upstream prefix.
	}

	endpoint, err := googleVertexImpersonatedEndpoint(credential, sourceCredential.Raw)
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	sourceToken, err := adc.credentialToken(ctx, sourceCredential)
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}
	if sourceToken.AccessToken == "" {
		return googleVertexTokenResponse{}, errors.New("unable to impersonate: source credentials returned an empty access token")
	}

	delegates := credential.Delegates
	if delegates == nil {
		delegates = []string{}
	}
	lifetime := int64(3600)
	if credential.Lifetime != nil {
		lifetime = *credential.Lifetime
	}
	// @google/genai supplies this scope to GoogleAuth, so it takes precedence over scopes in the credential JSON.
	body, err := ai.Marshal(googleVertexImpersonatedRequest{
		Delegates: delegates,
		Scope:     []string{googleVertexCloudScope},
		Lifetime:  fmt.Sprintf("%ds", lifetime),
	})
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}

	tokenURL := endpoint + "/v1/projects/-/serviceAccounts/" + match[1] + ":generateAccessToken"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, bytes.NewReader(body))
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+sourceToken.AccessToken)
	request.Header.Set("Content-Type", "application/json")
	if sourceCredential.QuotaProjectID != "" {
		request.Header.Set("X-Goog-User-Project", sourceCredential.QuotaProjectID)
	}
	response, err := adc.do(ctx, request)
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}
	defer func() { _ = response.Body.Close() }()

	var token googleVertexImpersonatedResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: %w", err)
	}
	expires, err := time.Parse(time.RFC3339Nano, token.ExpireTime)
	if err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("unable to impersonate: parse expireTime: %w", err)
	}
	return googleVertexTokenResponse{
		AccessToken: token.AccessToken,
		ExpiresIn:   int64(expires.Sub(adc.now()) / time.Second),
	}, nil
}

func googleVertexImpersonatedEndpoint(credential googleVertexImpersonatedADCFile, sourceRaw json.RawMessage) (string, error) {
	sourceUniverse, err := googleVertexCredentialUniverseDomain(sourceRaw)
	if err != nil {
		return "", fmt.Errorf("unable to impersonate: %w", err)
	}
	universe := credential.UniverseDomain
	if universe == "" {
		universe = sourceUniverse
	} else if universe != sourceUniverse {
		//nolint:staticcheck // The text is part of google-auth-library's observable validation behavior.
		return "", fmt.Errorf(
			"Universe domain %s in source credentials does not match %s universe domain set for impersonated credentials.",
			sourceUniverse,
			universe,
		)
	}
	if credential.Endpoint != nil {
		return *credential.Endpoint, nil
	}
	return "https://iamcredentials." + universe, nil
}

func googleVertexCredentialUniverseDomain(raw json.RawMessage) (string, error) {
	var credential struct {
		Type              string          `json:"type"`
		UniverseDomain    string          `json:"universe_domain"`
		SourceCredentials json.RawMessage `json:"source_credentials"`
	}
	if err := json.Unmarshal(raw, &credential); err != nil {
		return "", err
	}
	if credential.UniverseDomain != "" {
		return credential.UniverseDomain, nil
	}
	if credential.Type == "impersonated_service_account" && len(bytes.TrimSpace(credential.SourceCredentials)) > 0 {
		return googleVertexCredentialUniverseDomain(credential.SourceCredentials)
	}
	return googleVertexDefaultUniverseDomain, nil
}

func googleVertexJavaScriptStringLength(value string) int {
	length := 0
	for _, char := range value {
		length++
		if char > 0xffff {
			length++
		}
	}
	return length
}
