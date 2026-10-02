package oauth

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

type manualInteraction struct {
	input   string
	method  string
	events  []auth.AuthEvent
	prompts []auth.AuthPrompt
}

func (interaction *manualInteraction) Prompt(_ context.Context, prompt auth.AuthPrompt) (string, error) {
	interaction.prompts = append(interaction.prompts, prompt)
	if prompt.Type == auth.PromptSelect {
		return cmp.Or(interaction.method, "browser"), nil
	}
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
		Listen: func(_, address string) (net.Listener, error) {
			if strings.HasPrefix(address, "[::1]") {
				return nil, errors.New("no IPv6 here")
			}
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

func (interaction *callbackInteraction) Prompt(ctx context.Context, prompt auth.AuthPrompt) (string, error) {
	if prompt.Type == auth.PromptSelect {
		return "browser", nil
	}
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
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || string(body) != successPage("Signed in to Anthropic.") {
		t.Fatalf("callback body = %q, error = %v", body, readErr)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("callback status = %d", response.StatusCode)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func TestCallbackServerRejectsMismatchedStateAndEndsOnProviderError(t *testing.T) {
	server, err := startCallbackServer(callbackOptions[string]{
		provider: "Anthropic", host: "127.0.0.1", port: 0, path: callbackPath, state: "expected",
		complete: func(query url.Values) (string, error) { return query.Get("code"), nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.close()
	get := func(query string) (int, string) {
		response, err := http.Get(server.redirectURI + "?" + query) //nolint:gosec // local OAuth callback under test
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = response.Body.Close() }()
		body, _ := io.ReadAll(response.Body)
		return response.StatusCode, string(body)
	}
	if status, body := get("code=code&state=wrong"); status != http.StatusBadRequest || !strings.Contains(body, "State mismatch.") {
		t.Fatalf("mismatched state = %d %q", status, body)
	}
	if status, _ := get("state=expected&error=access_denied&error_description=User+said+no"); status != http.StatusBadRequest {
		t.Fatalf("provider error status = %d", status)
	}
	_, _, _, err = waitForCallbackOrManualInput(context.Background(), &callbackInteraction{urlReady: make(chan string, 1)}, server, auth.AuthPrompt{})
	if err == nil || err.Error() != "Anthropic authorization failed: User said no" {
		t.Fatalf("wait err = %v", err)
	}
}

func TestAnthropicCopyCodeLoginUsesAnthropicsCodePage(t *testing.T) {
	var requestBody string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requestBody = string(body)
		_, _ = io.WriteString(writer, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)
	}))
	defer tokenServer.Close()
	flow := NewAnthropic(&AnthropicOptions{
		TokenURL: tokenServer.URL, Random: bytes.NewReader(make([]byte, 32)),
		Listen: func(string, string) (net.Listener, error) { t.Fatal("copy code login listened"); return nil, nil },
	})
	interaction := &manualInteraction{method: "copy_code", input: "pasted-code#AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}
	if _, err := flow.Login(context.Background(), interaction); err != nil {
		t.Fatal(err)
	}
	authorize, _ := url.Parse(interaction.events[0].URL)
	if authorize.Query().Get("redirect_uri") != anthropicCopyCodeRedirectURI || !strings.Contains(requestBody, `"code":"pasted-code"`) ||
		!strings.Contains(requestBody, `"redirect_uri":"`+anthropicCopyCodeRedirectURI+`"`) {
		t.Fatalf("authorize = %s, token body = %s", authorize, requestBody)
	}
}

func TestAnthropicBusyCallbackPortFallsBackToPaste(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"access_token":"access","refresh_token":"refresh","expires_in":3600}`)
	}))
	defer tokenServer.Close()
	flow := NewAnthropic(&AnthropicOptions{
		TokenURL: tokenServer.URL, Random: bytes.NewReader(make([]byte, 32)),
		Listen: func(string, string) (net.Listener, error) { return nil, syscall.EADDRINUSE },
	})
	credential, err := flow.Login(context.Background(), &manualInteraction{input: "http://localhost:53692/callback?code=pasted&state=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"})
	if err != nil || credential.Access != "access" {
		t.Fatalf("credential = %#v, err = %v", credential, err)
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

// LOG-m7: formatOAuthErrorDetails ports upstream formatErrorDetails
// (anthropic.ts:82-97): name/message first, then code, errno, cause, and stack.
func TestLOGm7FormatOAuthErrorDetailsIncludesDiagnosticFields(t *testing.T) {
	errno := syscall.ECONNREFUSED
	if got, want := formatOAuthErrorDetails(errno), "Error: "+errno.Error()+"; errno="+strconv.Itoa(int(errno)); got != want {
		t.Fatalf("bare errno details = %q, want %q", got, want)
	}
	wrapped := fmt.Errorf("dial tcp 127.0.0.1:1: %w", errno)
	want := "Error: dial tcp 127.0.0.1:1: " + errno.Error() +
		"; cause=Error: " + errno.Error() + "; errno=" + strconv.Itoa(int(errno))
	if got := formatOAuthErrorDetails(wrapped); got != want {
		t.Fatalf("wrapped details = %q, want %q", got, want)
	}
	if got := formatOAuthErrorDetails(errors.New("plain")); got != "Error: plain" {
		t.Fatalf("plain details = %q", got)
	}
	diagnostic := oauthDiagnosticTestError{
		name: "TypeError", message: "fetch failed", code: "UND_ERR_CONNECT_TIMEOUT",
		errno: "ETIMEDOUT", cause: errors.New("socket closed"), stack: "TypeError: fetch failed\n    at postJson",
	}
	wantDiagnostic := "TypeError: fetch failed; code=UND_ERR_CONNECT_TIMEOUT; errno=ETIMEDOUT; " +
		"cause=Error: socket closed; stack=TypeError: fetch failed\n    at postJson"
	if got := formatOAuthErrorDetails(diagnostic); got != wantDiagnostic {
		t.Fatalf("typed diagnostic details = %q, want %q", got, wantDiagnostic)
	}
}

type oauthDiagnosticTestError struct {
	name, message, code string
	errno               any
	cause               error
	stack               string
}

func (err oauthDiagnosticTestError) Error() string { return err.message }
func (err oauthDiagnosticTestError) Name() string  { return err.name }
func (err oauthDiagnosticTestError) Code() string  { return err.code }
func (err oauthDiagnosticTestError) Errno() any    { return err.errno }
func (err oauthDiagnosticTestError) Unwrap() error { return err.cause }
func (err oauthDiagnosticTestError) Stack() string { return err.stack }

func TestAnthropicLoginRefusesWhenAnotherProgramHoldsIPv6Loopback(t *testing.T) {
	squatter, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("no IPv6 loopback here")
	}
	defer func() { _ = squatter.Close() }()
	port := squatter.Addr().(*net.TCPAddr).Port
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skip("port taken on IPv4")
	}
	flow := NewAnthropic(&AnthropicOptions{
		CallbackPort: port,
		Random:       bytes.NewReader(make([]byte, 32)),
		Listen: func(_, address string) (net.Listener, error) {
			if address == "[::1]:0" {
				return net.Listen("tcp", address)
			}
			if strings.HasPrefix(address, "[::1]") {
				return nil, errors.New("address already in use")
			}
			return listener, nil
		},
	})
	if _, err := flow.Login(context.Background(), &manualInteraction{input: "code"}); err == nil || !strings.Contains(err.Error(), "another program is listening") {
		t.Fatalf("err = %v", err)
	}
}
