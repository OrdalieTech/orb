package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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

func TestGoogleVertexADCImpersonatedServiceAccountOptionsAndNestedSource(t *testing.T) {
	fixedNow := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	var urls []string
	var bodies []string
	client := googleVertexADCTestClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		urls = append(urls, request.URL.String())
		switch len(urls) {
		case 1:
			_, _ = io.WriteString(writer, `{"access_token":"base-token","expires_in":3600}`)
		case 2:
			body, _ := io.ReadAll(request.Body)
			bodies = append(bodies, string(body))
			if got := request.Header.Get("Authorization"); got != "Bearer base-token" {
				t.Errorf("inner Authorization = %q", got)
			}
			_, _ = io.WriteString(writer, `{"accessToken":"inner-token","expireTime":"2026-01-02T04:04:05Z"}`)
		case 3:
			body, _ := io.ReadAll(request.Body)
			bodies = append(bodies, string(body))
			if got := request.Header.Get("Authorization"); got != "Bearer inner-token" {
				t.Errorf("outer Authorization = %q", got)
			}
			_, _ = io.WriteString(writer, `{"accessToken":"outer-token","expireTime":"2026-01-02T03:19:05Z"}`)
		default:
			t.Fatalf("unexpected request %d", len(urls))
		}
	}))
	adc := newGoogleVertexADC(nil)
	adc.client = client
	adc.now = func() time.Time { return fixedNow }
	adc.sleep = func(context.Context, time.Duration) error { return nil }

	raw := json.RawMessage(`{
		"type":"impersonated_service_account",
		"service_account_impersonation_url":"anything/outer@example.test:generateIdToken",
		"delegates":["delegate-a@example.test","delegate-b@example.test"],
		"lifetime":900,
		"source_credentials":{
			"type":"impersonated_service_account",
			"service_account_impersonation_url":"anything/inner@example.test:generateAccessToken",
			"source_credentials":{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}
		}
	}`)
	token, err := adc.impersonatedServiceAccountToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if token.AccessToken != "outer-token" || token.ExpiresIn != 900 {
		t.Errorf("token = %#v", token)
	}
	wantURLs := []string{
		googleVertexTokenURL,
		"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/inner@example.test:generateAccessToken",
		"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/outer@example.test:generateAccessToken",
	}
	if strings.Join(urls, "\n") != strings.Join(wantURLs, "\n") {
		t.Errorf("URLs = %#v, want %#v", urls, wantURLs)
	}
	if len(bodies) != 2 {
		t.Fatalf("IAM bodies = %#v", bodies)
	}
	if want := `{"delegates":[],"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"3600s"}`; bodies[0] != want {
		t.Errorf("inner body = %s, want %s", bodies[0], want)
	}
	if want := `{"delegates":["delegate-a@example.test","delegate-b@example.test"],"scope":["https://www.googleapis.com/auth/cloud-platform"],"lifetime":"900s"}`; bodies[1] != want {
		t.Errorf("outer body = %s, want %s", bodies[1], want)
	}
}

func TestGoogleVertexADCImpersonatedServiceAccountValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		message string
	}{
		{
			name:    "missing source credentials",
			raw:     `{"type":"impersonated_service_account","service_account_impersonation_url":"x/y@example.test:generateAccessToken"}`,
			message: "source_credentials field",
		},
		{
			name:    "null source credentials",
			raw:     `{"type":"impersonated_service_account","source_credentials":null,"service_account_impersonation_url":"x/y@example.test:generateAccessToken"}`,
			message: "source_credentials field",
		},
		{
			name:    "missing URL",
			raw:     `{"type":"impersonated_service_account","source_credentials":{}}`,
			message: "service_account_impersonation_url field",
		},
		{
			name:    "invalid target",
			raw:     `{"type":"impersonated_service_account","source_credentials":{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"},"service_account_impersonation_url":"https://example.test/no-target"}`,
			message: "Cannot extract target principal",
		},
		{
			name:    "JavaScript length limit",
			raw:     `{"type":"impersonated_service_account","source_credentials":{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"},"service_account_impersonation_url":"` + strings.Repeat("😀", 129) + `:generateAccessToken"}`,
			message: "Target principal is too long",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adc := newGoogleVertexADC(nil)
			_, err := adc.impersonatedServiceAccountToken(context.Background(), json.RawMessage(test.raw))
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want containing %q", err, test.message)
			}
		})
	}
}

func TestGoogleVertexADCImpersonatedServiceAccountExpiryAndError(t *testing.T) {
	fixedNow := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	for _, test := range []struct {
		name       string
		response   string
		wantExpiry int64
		wantError  string
	}{
		{name: "RFC3339 offset", response: `{"accessToken":"token","expireTime":"2026-01-02T05:04:05+01:00"}`, wantExpiry: 3600},
		{name: "invalid timestamp", response: `{"accessToken":"token","expireTime":"not-a-date"}`, wantError: "unable to impersonate: parse expireTime"},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := googleVertexADCTestClient(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.String() == googleVertexTokenURL {
					_, _ = io.WriteString(writer, `{"access_token":"source-token","expires_in":3600}`)
					return
				}
				_, _ = io.WriteString(writer, test.response)
			}))
			adc := newGoogleVertexADC(nil)
			adc.client = client
			adc.now = func() time.Time { return fixedNow }
			adc.sleep = func(context.Context, time.Duration) error { return nil }
			raw := json.RawMessage(`{
				"type":"impersonated_service_account",
				"service_account_impersonation_url":"x/target@example.test:generateAccessToken",
				"source_credentials":{"type":"authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}
			}`)
			token, err := adc.impersonatedServiceAccountToken(context.Background(), raw)
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("error = %v, want containing %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if token.ExpiresIn != test.wantExpiry {
				t.Errorf("expires_in = %d, want %d", token.ExpiresIn, test.wantExpiry)
			}
		})
	}
}
