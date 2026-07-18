package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const googleVertexExternalAuthorizedUserTokenURL = "https://sts.{universeDomain}/v1/oauthtoken"

type googleVertexExternalAuthorizedUserCredential struct {
	ClientID       string  `json:"client_id"`
	ClientSecret   string  `json:"client_secret"`
	RefreshToken   string  `json:"refresh_token"`
	TokenURL       *string `json:"token_url"`
	UniverseDomain *string `json:"universe_domain"`
}

type googleVertexExternalAuthorizedUserTokenResponse struct {
	AccessToken  string  `json:"access_token"`
	ExpiresIn    int64   `json:"expires_in"`
	TokenType    string  `json:"token_type"`
	RefreshToken *string `json:"refresh_token"`
}

func (adc *googleVertexADC) externalAuthorizedUserToken(ctx context.Context, raw json.RawMessage) (googleVertexTokenResponse, error) {
	var credential googleVertexExternalAuthorizedUserCredential
	if err := json.Unmarshal(raw, &credential); err != nil {
		return googleVertexTokenResponse{}, fmt.Errorf("decode external_account_authorized_user ADC: %w", err)
	}
	if adc.credential != nil && adc.credential.Type == "external_account_authorized_user" {
		credential.RefreshToken = adc.credential.RefreshToken
	}

	universeDomain := "googleapis.com"
	if credential.UniverseDomain != nil {
		universeDomain = *credential.UniverseDomain
	}
	endpoint := strings.Replace(googleVertexExternalAuthorizedUserTokenURL, "{universeDomain}", universeDomain, 1)
	if credential.TokenURL != nil {
		endpoint = *credential.TokenURL
	}

	body := "grant_type=refresh_token&refresh_token=" + googleVertexURLSearchParamsEscape(credential.RefreshToken)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return googleVertexTokenResponse{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=UTF-8")
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential.ClientID+":"+credential.ClientSecret)))

	response, err := adc.do(ctx, request)
	if err != nil {
		return googleVertexTokenResponse{}, googleVertexExternalAuthorizedUserOAuthError(err)
	}
	defer func() { _ = response.Body.Close() }()

	var token googleVertexExternalAuthorizedUserTokenResponse
	if err := json.NewDecoder(response.Body).Decode(&token); err != nil {
		return googleVertexTokenResponse{}, err
	}
	if token.RefreshToken != nil && adc.credential != nil && adc.credential.Type == "external_account_authorized_user" {
		adc.credential.RefreshToken = *token.RefreshToken
	}
	return googleVertexTokenResponse{
		AccessToken: token.AccessToken,
		ExpiresIn:   token.ExpiresIn,
		TokenType:   token.TokenType,
	}, nil
}

func googleVertexExternalAuthorizedUserOAuthError(err error) error {
	const prefix = "Google authentication request failed: "
	message := err.Error()
	if !strings.HasPrefix(message, prefix) {
		return err
	}
	failure := strings.TrimPrefix(message, prefix)
	separator := strings.Index(failure, ": ")
	if separator < 0 {
		return err
	}

	var response map[string]json.RawMessage
	if json.Unmarshal([]byte(failure[separator+2:]), &response) != nil {
		return errors.New("Error code undefined") //nolint:staticcheck // Exact upstream text.
	}
	oauthMessage := "Error code " + googleVertexExternalAuthorizedUserErrorField(response, "error")
	if _, ok := response["error_description"]; ok {
		oauthMessage += ": " + googleVertexExternalAuthorizedUserErrorField(response, "error_description")
	}
	if _, ok := response["error_uri"]; ok {
		oauthMessage += " - " + googleVertexExternalAuthorizedUserErrorField(response, "error_uri")
	}
	return errors.New(oauthMessage)
}

func googleVertexExternalAuthorizedUserErrorField(response map[string]json.RawMessage, name string) string {
	value, ok := response[name]
	if !ok {
		return "undefined"
	}
	if string(value) == "null" {
		return "null"
	}
	var text string
	if json.Unmarshal(value, &text) == nil {
		return text
	}
	return string(value)
}

// URLSearchParams uses the application/x-www-form-urlencoded percent-encode set,
// which differs from net/url.QueryEscape for '*' and '~'.
func googleVertexURLSearchParamsEscape(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var encoded strings.Builder
	encoded.Grow(len(value))
	for _, character := range []byte(value) {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '*', character == '-', character == '.', character == '_':
			encoded.WriteByte(character)
		case character == ' ':
			encoded.WriteByte('+')
		default:
			encoded.WriteByte('%')
			encoded.WriteByte(hexadecimal[character>>4])
			encoded.WriteByte(hexadecimal[character&0x0f])
		}
	}
	return encoded.String()
}
