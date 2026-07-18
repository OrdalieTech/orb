package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

type googleVertexExternalUserRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip googleVertexExternalUserRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func googleVertexExternalUserResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status) + " " + http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestGoogleVertexExternalAuthorizedUserRefreshMatchesUpstream(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"external_account_authorized_user",
		"client_id":"client:id",
		"client_secret":"s ecret✓",
		"refresh_token":"a~!*'() b+/?",
		"token_url":"https://sts.example.test/custom"
	}`)
	adc := newGoogleVertexADC(nil)
	adc.credential = &googleVertexADCFile{
		Type:         "external_account_authorized_user",
		RefreshToken: "a~!*'() b+/?",
		Raw:          raw,
	}

	var bodies []string
	adc.client = &http.Client{Transport: googleVertexExternalUserRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", request.Method)
		}
		if got := request.URL.String(); got != "https://sts.example.test/custom" {
			t.Errorf("URL = %q", got)
		}
		if got := request.Header.Get("Authorization"); got != "Basic Y2xpZW50OmlkOnMgZWNyZXTinJM=" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("Content-Type"); got != "application/x-www-form-urlencoded;charset=UTF-8" {
			t.Errorf("Content-Type = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, string(body))
		if len(bodies) == 1 {
			return googleVertexExternalUserResponse(http.StatusOK, `{"access_token":"first","expires_in":600,"token_type":"Bearer","refresh_token":"rotated"}`), nil
		}
		return googleVertexExternalUserResponse(http.StatusOK, `{"access_token":"second","expires_in":300,"token_type":"Bearer"}`), nil
	})}

	first, err := adc.externalAuthorizedUserToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if first != (googleVertexTokenResponse{AccessToken: "first", ExpiresIn: 600, TokenType: "Bearer"}) {
		t.Fatalf("first token = %#v", first)
	}
	second, err := adc.externalAuthorizedUserToken(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if second != (googleVertexTokenResponse{AccessToken: "second", ExpiresIn: 300, TokenType: "Bearer"}) {
		t.Fatalf("second token = %#v", second)
	}

	wantBodies := []string{
		"grant_type=refresh_token&refresh_token=a%7E%21*%27%28%29+b%2B%2F%3F",
		"grant_type=refresh_token&refresh_token=rotated",
	}
	if len(bodies) != len(wantBodies) {
		t.Fatalf("request bodies = %#v", bodies)
	}
	for index := range wantBodies {
		if bodies[index] != wantBodies[index] {
			t.Errorf("request body %d = %q, want %q", index, bodies[index], wantBodies[index])
		}
	}
}

func TestGoogleVertexExternalAuthorizedUserEndpointDefaults(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "missing token URL and universe",
			raw:  `{"type":"external_account_authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh"}`,
			want: "https://sts.googleapis.com/v1/oauthtoken",
		},
		{
			name: "null token URL and universe",
			raw:  `{"type":"external_account_authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","token_url":null,"universe_domain":null}`,
			want: "https://sts.googleapis.com/v1/oauthtoken",
		},
		{
			name: "custom universe",
			raw:  `{"type":"external_account_authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","universe_domain":"example.test"}`,
			want: "https://sts.example.test/v1/oauthtoken",
		},
		{
			name: "custom token URL overrides universe",
			raw:  `{"type":"external_account_authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","universe_domain":"example.test","token_url":"https://tokens.example.test/refresh"}`,
			want: "https://tokens.example.test/refresh",
		},
		{
			name: "empty universe is not nullish",
			raw:  `{"type":"external_account_authorized_user","client_id":"id","client_secret":"secret","refresh_token":"refresh","universe_domain":""}`,
			want: "https://sts./v1/oauthtoken",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests int
			adc := newGoogleVertexADC(nil)
			adc.client = &http.Client{Transport: googleVertexExternalUserRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requests++
				if got := request.URL.String(); got != test.want {
					t.Errorf("URL = %q, want %q", got, test.want)
				}
				if got := request.Header.Get("Authorization"); got != "Basic aWQ6c2VjcmV0" {
					t.Errorf("Authorization = %q", got)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatal(err)
				}
				if got := string(body); got != "grant_type=refresh_token&refresh_token=refresh" {
					t.Errorf("body = %q", got)
				}
				return googleVertexExternalUserResponse(http.StatusOK, `{"access_token":"token","expires_in":3600}`), nil
			})}

			if _, err := adc.externalAuthorizedUserToken(context.Background(), json.RawMessage(test.raw)); err != nil {
				t.Fatal(err)
			}
			if requests != 1 {
				t.Fatalf("requests = %d, want 1", requests)
			}
		})
	}
}

func TestGoogleVertexExternalAuthorizedUserUsesUpstreamExpiryThreshold(t *testing.T) {
	raw := json.RawMessage(`{"type":"external_account_authorized_user","refresh_token":"refresh"}`)
	now := time.Unix(1_700_000_000, 0).UTC()
	requests := 0
	adc := newGoogleVertexADC(nil)
	adc.sourceLoaded = true
	adc.credential = &googleVertexADCFile{
		Type:         "external_account_authorized_user",
		RefreshToken: "refresh",
		Raw:          raw,
	}
	adc.now = func() time.Time { return now }
	adc.client = &http.Client{Transport: googleVertexExternalUserRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return googleVertexExternalUserResponse(http.StatusOK, `{"access_token":"token-`+strconv.Itoa(requests)+`","expires_in":600}`), nil
	})}

	first, err := adc.headers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(4 * time.Minute)
	second, err := adc.headers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 || first.accessToken != "token-1" || second.accessToken != "token-1" {
		t.Fatalf("before threshold: requests=%d first=%q second=%q", requests, first.accessToken, second.accessToken)
	}

	now = now.Add(time.Minute)
	third, err := adc.headers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || third.accessToken != "token-2" {
		t.Fatalf("at threshold: requests=%d token=%q", requests, third.accessToken)
	}
}

func TestGoogleVertexExternalAuthorizedUserErrors(t *testing.T) {
	t.Run("credential JSON", func(t *testing.T) {
		adc := newGoogleVertexADC(nil)
		_, err := adc.externalAuthorizedUserToken(context.Background(), json.RawMessage(`{"token_url":`))
		if err == nil || !strings.Contains(err.Error(), "decode external_account_authorized_user ADC") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("OAuth response", func(t *testing.T) {
		requests := 0
		adc := newGoogleVertexADC(nil)
		adc.client = &http.Client{Transport: googleVertexExternalUserRoundTripFunc(func(*http.Request) (*http.Response, error) {
			requests++
			return googleVertexExternalUserResponse(http.StatusBadRequest, `{"error":"invalid_grant","error_description":"expired","error_uri":"https://errors.example.test/invalid-grant"}`), nil
		})}
		_, err := adc.externalAuthorizedUserToken(context.Background(), json.RawMessage(`{"token_url":"https://sts.example.test/token"}`))
		want := "Error code invalid_grant: expired - https://errors.example.test/invalid-grant"
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
		if requests != 1 {
			t.Fatalf("requests = %d, want 1", requests)
		}
	})

	t.Run("malformed token response", func(t *testing.T) {
		adc := newGoogleVertexADC(nil)
		adc.client = &http.Client{Transport: googleVertexExternalUserRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return googleVertexExternalUserResponse(http.StatusOK, `{`), nil
		})}
		_, err := adc.externalAuthorizedUserToken(context.Background(), json.RawMessage(`{"token_url":"https://sts.example.test/token"}`))
		if err == nil {
			t.Fatal("expected malformed token response error")
		}
	})
}
