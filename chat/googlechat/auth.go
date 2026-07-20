package googlechat

// auth.go mints the outbound bearer token: an RS256 service-account
// assertion exchanged at the OAuth2 token endpoint via the JWT-bearer grant
// (scope chat.bot), all stdlib. Tokens are cached and refreshed five
// minutes early. The assertion and the token never appear in errors or
// logs.

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const chatBotScope = "https://www.googleapis.com/auth/chat.bot"

const jwtBearerGrant = "urn:ietf:params:oauth:grant-type:jwt-bearer"

const tokenEarlyRefresh = 5 * time.Minute

func (a *Adapter) bearer(ctx context.Context) (string, error) {
	a.tokenMu.Lock()
	defer a.tokenMu.Unlock()
	if a.tokenValue != "" && a.now().Before(a.tokenExpiry) {
		return a.tokenValue, nil
	}
	token, expiresIn, err := a.mintToken(ctx)
	if err != nil {
		return "", err
	}
	a.tokenValue = token
	a.tokenExpiry = a.now().Add(expiresIn - tokenEarlyRefresh)
	return token, nil
}

func (a *Adapter) invalidateToken() {
	a.tokenMu.Lock()
	a.tokenValue = ""
	a.tokenMu.Unlock()
}

func (a *Adapter) mintToken(ctx context.Context) (string, time.Duration, error) {
	assertion, err := a.signAssertion(a.now())
	if err != nil {
		return "", 0, err
	}
	form := url.Values{}
	form.Set("grant_type", jwtBearerGrant)
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("googlechat: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := a.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("googlechat: token exchange: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return "", 0, fmt.Errorf("googlechat: read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The OAuth error body ({"error":"invalid_grant",...}) carries no
		// secrets; the assertion is never included in the error.
		const maxSnippet = 256
		snippet := string(data)
		if len(snippet) > maxSnippet {
			snippet = snippet[:maxSnippet]
		}
		return "", 0, fmt.Errorf("googlechat: token endpoint returned HTTP %d: %s", resp.StatusCode, snippet)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", 0, fmt.Errorf("googlechat: decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", 0, fmt.Errorf("googlechat: token endpoint returned no access_token")
	}
	expiresIn := time.Duration(out.ExpiresIn) * time.Second
	if expiresIn <= tokenEarlyRefresh {
		expiresIn = tokenEarlyRefresh + time.Minute
	}
	return out.AccessToken, expiresIn, nil
}

func (a *Adapter) signAssertion(now time.Time) (string, error) {
	header := map[string]string{"alg": "RS256", "typ": "JWT"}
	if a.keyID != "" {
		header["kid"] = a.keyID
	}
	claims := map[string]any{
		"iss":   a.clientEmail,
		"scope": chatBotScope,
		"aud":   a.tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		return "", fmt.Errorf("googlechat: encode assertion header: %w", err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("googlechat: encode assertion claims: %w", err)
	}
	signingInput := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, a.key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("googlechat: sign assertion: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
