package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
)

type googleVertexExternalAccountRoundTripFunc func(*http.Request) (*http.Response, error)

func (function googleVertexExternalAccountRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func googleVertexExternalAccountTestResponse(request *http.Request, body string) *http.Response {
	return &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}
}

func googleVertexExternalAccountRaw(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGoogleVertexExternalAccountFileSTSWorkforceAndURLSearchParams(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subject-token")
	if err := os.WriteFile(path, []byte("subject ~* token"), 0o600); err != nil {
		t.Fatal(err)
	}
	const audience = "//iam.googleapis.com/locations/global/workforcePools/pool/providers/provider"
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":                        "external_account",
		"audience":                    audience,
		"subject_token_type":          "urn:ietf:params:oauth:token-type:jwt",
		"token_url":                   "https://sts.example.test/v1/token",
		"scopes":                      []string{"credential-json-scope-is-ignored"},
		"workforce_pool_user_project": "billing-project",
		"credential_source":           map[string]any{"file": path},
	})

	var requestBody string
	adc := newGoogleVertexADC(nil)
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestBytes, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		requestBody = string(requestBytes)
		if request.URL.String() != "https://sts.example.test/v1/token" {
			t.Errorf("STS URL = %q", request.URL.String())
		}
		if got := request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Errorf("STS Content-Type = %q", got)
		}
		if got := request.Header.Get("X-Goog-Api-Client"); !strings.Contains(got, "auth/10.6.2 google-byoid-sdk source/file sa-impersonation/false config-lifetime/false") {
			t.Errorf("metrics header = %q", got)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"file-access","expires_in":3600,"token_type":"Bearer"}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "file-access" || token.ExpiresIn != 3600 || token.TokenType != "Bearer" {
		t.Fatalf("token = %#v", token)
	}
	wantBody := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Atoken-exchange" +
		"&audience=%2F%2Fiam.googleapis.com%2Flocations%2Fglobal%2FworkforcePools%2Fpool%2Fproviders%2Fprovider" +
		"&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcloud-platform" +
		"&requested_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Aaccess_token" +
		"&subject_token=subject+%7E*+token" +
		"&subject_token_type=urn%3Aietf%3Aparams%3Aoauth%3Atoken-type%3Ajwt" +
		"&options=%7B%22userProject%22%3A%22billing-project%22%7D"
	if requestBody != wantBody {
		t.Errorf("STS body:\n got %s\nwant %s", requestBody, wantBody)
	}
}

func TestGoogleVertexExternalAccountAWSEnvironmentSigV4(t *testing.T) {
	fixedNow := time.Date(2023, time.November, 14, 22, 13, 20, 0, time.UTC)
	raw := googleVertexExternalAccountRaw(t, map[string]any{
		"type":               "external_account",
		"audience":           "//iam.googleapis.com/projects/123/locations/global/workloadIdentityPools/pool/providers/aws?<target>&value=>",
		"subject_token_type": "urn:ietf:params:aws:token-type:aws4_request",
		"token_url":          "https://sts.google.example.test/v1/token",
		"credential_source": map[string]any{
			"environment_id":                 "aws1",
			"regional_cred_verification_url": "https://sts.{region}.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15",
		},
	})
	options := &ai.StreamOptions{Env: ai.ProviderEnv{
		"AWS_REGION":            "us-east-2",
		"AWS_ACCESS_KEY_ID":     "AKIDEXAMPLE",
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY",
		"AWS_SESSION_TOKEN":     "session-token",
	}}
	adc := newGoogleVertexADC(options)
	adc.now = func() time.Time { return fixedNow }
	adc.client = &http.Client{Transport: googleVertexExternalAccountRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := request.ParseForm(); err != nil {
			t.Fatal(err)
		}
		encoded := request.PostForm.Get("subject_token")
		serialized, err := url.QueryUnescape(encoded)
		if err != nil {
			t.Fatal(err)
		}
		var signed googleVertexExternalAccountAWSSignedRequest
		if err := json.Unmarshal([]byte(serialized), &signed); err != nil {
			t.Fatalf("decode AWS subject token %q: %v", serialized, err)
		}
		if strings.Contains(serialized, `\u0026`) || strings.Contains(serialized, `\u003c`) || strings.Contains(serialized, `\u003e`) ||
			!strings.Contains(serialized, "&Version=2011-06-15") || !strings.Contains(serialized, "aws?<target>&value=>") {
			t.Errorf("AWS signed request did not preserve JSON.stringify escaping: %s", serialized)
		}
		if signed.URL != "https://sts.us-east-2.amazonaws.com?Action=GetCallerIdentity&Version=2011-06-15" || signed.Method != http.MethodPost {
			t.Errorf("signed request = %#v", signed)
		}
		headers := make(map[string]string)
		for _, header := range signed.Headers {
			headers[header.Key] = header.Value
		}
		wantAuthorization := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20231114/us-east-2/sts/aws4_request, SignedHeaders=host;x-amz-date;x-amz-security-token, Signature=e775b1ee5067879e927d0c6b2ca9c92b866730b92492a46472a751f4ff54f3c7"
		if headers["authorization"] != wantAuthorization {
			t.Errorf("AWS authorization:\n got %s\nwant %s", headers["authorization"], wantAuthorization)
		}
		if headers["x-amz-date"] != "20231114T221320Z" || headers["x-amz-security-token"] != "session-token" {
			t.Errorf("AWS headers = %#v", headers)
		}
		if headers["x-goog-cloud-target-resource"] == "" {
			t.Errorf("target resource header missing: %#v", headers)
		}
		return googleVertexExternalAccountTestResponse(request, `{"access_token":"aws-access","expires_in":3600}`), nil
	})}
	token, err := adc.externalAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "aws-access" {
		t.Errorf("access token = %q", token.AccessToken)
	}
}
