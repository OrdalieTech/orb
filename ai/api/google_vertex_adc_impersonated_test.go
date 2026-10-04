package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestGoogleVertexADCImpersonatedServiceAccountDefaults(t *testing.T) {
	fixedNow := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	var iamBody string
	client := googleVertexADCTestClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.String() {
		case googleVertexTokenURL:
			_, _ = io.WriteString(writer, `{"access_token":"source-token","expires_in":3600,"token_type":"MAC"}`)
		case "https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/target@example.test:generateAccessToken":
			if got := request.Header.Get("Authorization"); got != "Bearer source-token" {
				t.Errorf("Authorization = %q", got)
			}
			if got := request.Header.Get("X-Goog-User-Project"); got != "source-quota" {
				t.Errorf("X-Goog-User-Project = %q", got)
			}
			if got := request.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q", got)
			}
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			iamBody = string(body)
			_, _ = io.WriteString(writer, `{"accessToken":"impersonated-token","expireTime":"2026-01-02T04:04:05Z"}`)
		default:
			t.Errorf("unexpected request URL %q", request.URL.String())
			http.NotFound(writer, request)
		}
	}))
	adc := newGoogleVertexADC(nil)
	adc.client = client
	adc.now = func() time.Time { return fixedNow }
	adc.sleep = func(context.Context, time.Duration) error { return nil }

	raw := json.RawMessage(`{
		"type":"impersonated_service_account",
		"service_account_impersonation_url":"https://ignored.example.test/v1/projects/-/serviceAccounts/target@example.test:generateAccessToken",
		"source_credentials":{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","quota_project_id":"source-quota"},
		"scopes":["https://example.test/ignored"]
	}`)
	token, err := adc.impersonatedServiceAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "impersonated-token" || token.ExpiresIn != 3600 || token.TokenType != "" {
		t.Errorf("token = %#v", token)
	}
	wantBody := `{"delegates":[],"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"3600s"}`
	if iamBody != wantBody {
		t.Errorf("IAM body = %s, want %s", iamBody, wantBody)
	}
}
