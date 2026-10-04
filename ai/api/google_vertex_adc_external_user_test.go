package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
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
