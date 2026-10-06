package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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

	header, body := googleVertexForm([2]string{"grant_type", "refresh_token"}, [2]string{"refresh_token", credential.RefreshToken})
	header.Set("Accept", "application/json")
	header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credential.ClientID+":"+credential.ClientSecret)))
	var token googleVertexExternalAuthorizedUserTokenResponse
	if err := adc.postJSON(ctx, endpoint, body, header, &token); err != nil {
		// Only the retry policy's failures carry the prefix this rewrites.
		return googleVertexTokenResponse{}, googleVertexExternalAuthorizedUserOAuthError(err)
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
