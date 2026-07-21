package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

type manualInteraction struct {
	input  string
	events []auth.AuthEvent
}

func (interaction *manualInteraction) Prompt(context.Context, auth.AuthPrompt) (string, error) {
	return interaction.input, nil
}

func (interaction *manualInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

func TestAnthropicLoginManualCode(t *testing.T) {
	var requestBody string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requestBody = string(body)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"access_token":"sk-ant-oat-access","refresh_token":"refresh","expires_in":3600}`)
	}))
	defer tokenServer.Close()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	used := false
	flow := NewAnthropic(&AnthropicOptions{
		AuthorizeURL: "https://example.test/authorize",
		TokenURL:     tokenServer.URL,
		CallbackHost: "127.0.0.1",
		CallbackPort: port,
		RedirectURI:  "http://localhost:" + listener.Addr().(*net.TCPAddr).String()[strings.LastIndex(listener.Addr().String(), ":")+1:] + callbackPath,
		Random:       bytes.NewReader(make([]byte, 32)),
		Now:          func() time.Time { return time.UnixMilli(1_700_000_000_000) },
		Listen: func(_, _ string) (net.Listener, error) {
			if used {
				t.Fatal("listener reused")
			}
			used = true
			return listener, nil
		},
	})
	interaction := &manualInteraction{input: "manual-code"}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "sk-ant-oat-access" || credential.Refresh != "refresh" || credential.Expires != 1_700_003_300_000 {
		t.Fatalf("credential = %#v", credential)
	}
	if len(interaction.events) != 2 || interaction.events[0].Type != auth.EventAuthURL || interaction.events[1].Type != auth.EventProgress {
		t.Fatalf("events = %#v", interaction.events)
	}
	authorize, err := url.Parse(interaction.events[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if query.Get("state") != "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" || query.Get("code_challenge") != "DwBzhbb51LfusnSGBa_hqYSgo7-j8BTQnip4TOnlzRo" {
		t.Fatalf("authorize query = %s", authorize.RawQuery)
	}
	wantBody := `{"grant_type":"authorization_code","client_id":"9d1c250a-e61b-44d9-88ed-5944d1962f5e","code":"manual-code","state":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","redirect_uri":"` + flow.options.RedirectURI + `","code_verifier":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
	if requestBody != wantBody {
		t.Fatalf("token body = %s, want %s", requestBody, wantBody)
	}
}

type callbackInteraction struct {
	urlReady chan string
}

func (interaction *callbackInteraction) Prompt(ctx context.Context, _ auth.AuthPrompt) (string, error) {
	<-ctx.Done()
	return "", ctx.Err()
}

func (interaction *callbackInteraction) Notify(event auth.AuthEvent) {
	if event.Type == auth.EventAuthURL {
		interaction.urlReady <- event.URL
	}
}

func TestAnthropicLoginCallback(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"access_token":"access","refresh_token":"refresh","expires_in":600}`)
	}))
	defer tokenServer.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	flow := NewAnthropic(&AnthropicOptions{
		AuthorizeURL: "https://example.test/authorize", TokenURL: tokenServer.URL,
		CallbackHost: "127.0.0.1", CallbackPort: port,
		RedirectURI: "http://localhost:" + listener.Addr().(*net.TCPAddr).String()[strings.LastIndex(listener.Addr().String(), ":")+1:] + callbackPath,
		Random:      bytes.NewReader(make([]byte, 32)),
		Listen:      func(_, _ string) (net.Listener, error) { return listener, nil },
	})
	interaction := &callbackInteraction{urlReady: make(chan string, 1)}
	result := make(chan error, 1)
	go func() {
		_, loginErr := flow.Login(context.Background(), interaction)
		result <- loginErr
	}()
	authorizeURL := <-interaction.urlReady
	parsed, _ := url.Parse(authorizeURL)
	callbackURL := flow.options.RedirectURI + "?code=callback-code&state=" + url.QueryEscape(parsed.Query().Get("state"))
	response, err := http.Get(callbackURL) //nolint:gosec // local OAuth callback under test
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestAnthropicCallbackRejectsMismatchedState(t *testing.T) {
	wait := make(chan callbackResult, 1)
	request := httptest.NewRequest(http.MethodGet, callbackPath+"?code=code&state=wrong", nil)
	response := httptest.NewRecorder()
	callbackHandler("expected", wait).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "State mismatch.") {
		t.Fatalf("callback response = %d %q", response.Code, response.Body.String())
	}
	select {
	case result := <-wait:
		t.Fatalf("mismatched callback resolved login: %#v", result)
	default:
	}
}

func TestParseAuthorizationInput(t *testing.T) {
	tests := []struct {
		input     string
		wantCode  string
		wantState string
	}{
		{input: "https://localhost/callback?code=url-code&state=url-state", wantCode: "url-code", wantState: "url-state"},
		{input: "hash-code#hash-state", wantCode: "hash-code", wantState: "hash-state"},
		{input: "code=query-code&state=query-state", wantCode: "query-code", wantState: "query-state"},
		{input: "manual-code", wantCode: "manual-code"},
	}
	for _, test := range tests {
		code, state, err := parseAuthorizationInput(test.input)
		if err != nil || code != test.wantCode || state != test.wantState {
			t.Fatalf("parse %q = %q, %q, %v", test.input, code, state, err)
		}
	}
}

func TestAnthropicRefreshUsesRotatedToken(t *testing.T) {
	var mu sync.Mutex
	var body map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() { _ = request.Body.Close() }()
		mu.Lock()
		_ = json.NewDecoder(request.Body).Decode(&body)
		mu.Unlock()
		_, _ = io.WriteString(writer, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":900}`)
	}))
	defer server.Close()
	flow := NewAnthropic(&AnthropicOptions{TokenURL: server.URL, Now: func() time.Time { return time.UnixMilli(1_000_000) }})
	current := auth.OAuthCredential("old-refresh", "old-access", 0)
	current.Extra = map[string]json.RawMessage{"providerExtra": json.RawMessage(`"old"`)}
	credential, err := flow.Refresh(context.Background(), current)
	if err != nil {
		t.Fatal(err)
	}
	if body["grant_type"] != "refresh_token" || body["refresh_token"] != "old-refresh" || body["client_id"] != anthropicClientID {
		t.Fatalf("refresh body = %#v", body)
	}
	if credential.Access != "new-access" || credential.Refresh != "new-refresh" || credential.Expires != 1_600_000 {
		t.Fatalf("credential = %#v", credential)
	}
	if credential.Extra != nil {
		t.Fatalf("Anthropic refresh retained fields upstream drops: %#v", credential.Extra)
	}
}

func TestAnthropicTokenFailuresKeepUpstreamWrapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, "denied")
	}))
	defer server.Close()
	flow := NewAnthropic(&AnthropicOptions{TokenURL: server.URL, RedirectURI: "http://localhost:53692/callback"})

	for _, test := range []struct {
		label string
		want  string
	}{
		{
			label: "Token exchange",
			want:  "Token exchange request failed. url=" + server.URL + "; redirect_uri=http://localhost:53692/callback; response_type=authorization_code; details=Error: HTTP request failed. status=401; url=" + server.URL + "; body=denied",
		},
		{
			label: "Anthropic token refresh",
			want:  "Anthropic token refresh request failed. url=" + server.URL + "; details=Error: HTTP request failed. status=401; url=" + server.URL + "; body=denied",
		},
	} {
		_, err := flow.exchange(context.Background(), []byte(`{}`), test.label)
		if err == nil || err.Error() != test.want {
			t.Errorf("%s error = %q, want %q", test.label, err, test.want)
		}
	}
}
