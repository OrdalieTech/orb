package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

type openRouterInteraction struct {
	onAuthURL func(authorizeURL string)
	events    []auth.AuthEvent
}

func (*openRouterInteraction) Prompt(context.Context, auth.AuthPrompt) (string, error) {
	return "", nil
}

func (interaction *openRouterInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
	if event.Type == auth.EventAuthURL && interaction.onAuthURL != nil {
		interaction.onAuthURL(event.URL)
	}
}

func openRouterCallbackURL(t *testing.T, authorizeURL string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	callback, err := url.Parse(parsed.Query().Get("callback_url"))
	if err != nil {
		t.Fatal(err)
	}
	return callback
}

type openRouterExchangeCapture struct {
	body    []byte
	headers http.Header
}

func TestOpenRouterLoginExchangesCodeForPermanentKey(t *testing.T) {
	captured := make(chan openRouterExchangeCapture, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		captured <- openRouterExchangeCapture{body: body, headers: request.Header.Clone()}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"key":"sk-or-test"}`)
	}))
	defer server.Close()

	flow := NewOpenRouter(&OpenRouterOptions{TokenURL: server.URL})
	var authorizeURL string
	callbackDone := make(chan *http.Response, 1)
	interaction := &openRouterInteraction{onAuthURL: func(value string) {
		authorizeURL = value
		callback := openRouterCallbackURL(t, value)
		query := callback.Query()
		query.Set("code", "authorization-code")
		callback.RawQuery = query.Encode()
		go func() {
			response, err := http.Get(callback.String())
			if err != nil {
				t.Error(err)
				callbackDone <- nil
				return
			}
			_ = response.Body.Close()
			callbackDone <- response
		}()
	}}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := credential.MarshalJSON()
	want := `{"type":"oauth","access":"sk-or-test","refresh":"","expires":9007199254740991}`
	if string(encoded) != want {
		t.Fatalf("credential = %s, want %s", encoded, want)
	}
	if response := <-callbackDone; response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("callback response = %#v", response)
	}
	if len(interaction.events) != 2 || interaction.events[0].Type != auth.EventProgress ||
		interaction.events[1].Instructions != "Complete sign-in in your browser." {
		t.Fatalf("events = %#v", interaction.events)
	}

	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme+"://"+parsed.Host != "https://openrouter.ai" || parsed.Path != "/auth" {
		t.Fatalf("authorize URL = %s", authorizeURL)
	}
	if parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("authorize query = %s", parsed.RawQuery)
	}
	callback := openRouterCallbackURL(t, authorizeURL)
	if callback.Hostname() != "127.0.0.1" || !regexp.MustCompile(`^/oauth/callback/[0-9a-f-]+$`).MatchString(callback.Path) {
		t.Fatalf("callback URL = %s", callback)
	}

	exchange := <-captured
	var exchanged struct {
		Code                string `json:"code"`
		CodeVerifier        string `json:"code_verifier"`
		CodeChallengeMethod string `json:"code_challenge_method"`
	}
	if err := json.Unmarshal(exchange.body, &exchanged); err != nil {
		t.Fatal(err)
	}
	if exchanged.Code != "authorization-code" || exchanged.CodeChallengeMethod != "S256" || exchanged.CodeVerifier == "" {
		t.Fatalf("exchange body = %s", exchange.body)
	}
	if exchange.headers.Get("Content-Type") != "application/json" || exchange.headers.Get("Accept") != "application/json" {
		t.Fatalf("exchange headers = %#v", exchange.headers)
	}
	digest := sha256.Sum256([]byte(exchanged.CodeVerifier))
	if parsed.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(digest[:]) {
		t.Fatalf("code_challenge does not match the exchanged verifier: %s", parsed.RawQuery)
	}
}

func TestOpenRouterLoginReportsExchangeFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(writer, `{"error":{"message":"invalid code"}}`)
	}))
	defer server.Close()

	flow := NewOpenRouter(&OpenRouterOptions{TokenURL: server.URL})
	callbackDone := make(chan *http.Response, 1)
	_, err := flow.Login(context.Background(), &openRouterInteraction{onAuthURL: func(value string) {
		callback := openRouterCallbackURL(t, value)
		query := callback.Query()
		query.Set("code", "bad-code")
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr != nil {
				callbackDone <- nil
				return
			}
			_ = response.Body.Close()
			callbackDone <- response
		}()
	}})
	if err == nil || err.Error() != "OpenRouter OAuth key exchange failed (HTTP 403): invalid code" {
		t.Fatalf("error = %v", err)
	}
	if response := <-callbackDone; response == nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("callback response = %#v", response)
	}
}

func TestOpenRouterAllowsOnlyOneTokenExchange(t *testing.T) {
	release := make(chan struct{})
	var exchanges atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		exchanges.Add(1)
		<-release
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"key":"sk-or-test"}`)
	}))
	defer server.Close()

	flow := NewOpenRouter(&OpenRouterOptions{TokenURL: server.URL})
	firstDone := make(chan *http.Response, 1)
	secondDone := make(chan *http.Response, 1)
	credential, err := flow.Login(context.Background(), &openRouterInteraction{onAuthURL: func(value string) {
		callback := openRouterCallbackURL(t, value)
		query := callback.Query()
		query.Set("code", "authorization-code")
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr != nil {
				firstDone <- nil
				return
			}
			_ = response.Body.Close()
			firstDone <- response
		}()
		go func() {
			// Wait for the first callback to claim the exchange, then observe
			// the one-shot conflict and release the pending exchange.
			for start := time.Now(); exchanges.Load() == 0 && time.Since(start) < 5*time.Second; {
				time.Sleep(time.Millisecond)
			}
			response, getErr := http.Get(callback.String())
			if getErr != nil {
				secondDone <- nil
			} else {
				_ = response.Body.Close()
				secondDone <- response
			}
			close(release)
		}()
	}})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "sk-or-test" {
		t.Fatalf("credential = %#v", credential)
	}
	if response := <-secondDone; response == nil || response.StatusCode != http.StatusConflict {
		t.Fatalf("second callback = %#v", response)
	}
	if response := <-firstDone; response == nil || response.StatusCode != http.StatusOK {
		t.Fatalf("first callback = %#v", response)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges = %d, want 1", exchanges.Load())
	}
}

func TestOpenRouterRejectsSuccessWithoutKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"user_id":"user-1"}`)
	}))
	defer server.Close()

	flow := NewOpenRouter(&OpenRouterOptions{TokenURL: server.URL})
	callbackDone := make(chan *http.Response, 1)
	_, err := flow.Login(context.Background(), &openRouterInteraction{onAuthURL: func(value string) {
		callback := openRouterCallbackURL(t, value)
		query := callback.Query()
		query.Set("code", "code-without-key")
		callback.RawQuery = query.Encode()
		go func() {
			response, getErr := http.Get(callback.String())
			if getErr != nil {
				callbackDone <- nil
				return
			}
			_ = response.Body.Close()
			callbackDone <- response
		}()
	}})
	if err == nil || err.Error() != `OpenRouter OAuth response carries no "key"` {
		t.Fatalf("error = %v", err)
	}
	if response := <-callbackDone; response == nil || response.StatusCode != http.StatusBadGateway {
		t.Fatalf("callback response = %#v", response)
	}
}

func TestOpenRouterClosesCallbackOnCancelledLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var callback *url.URL
	_, err := NewOpenRouter(nil).Login(ctx, &openRouterInteraction{onAuthURL: func(value string) {
		callback = openRouterCallbackURL(t, value)
		cancel()
	}})
	if err == nil || err.Error() != "Login cancelled" {
		t.Fatalf("error = %v", err)
	}
	if callback == nil {
		t.Fatal("login did not expose a callback URL")
	}
	if _, err := http.Get(callback.String()); err == nil {
		t.Fatal("callback server still reachable after cancellation")
	}
}

func TestOpenRouterRejectsAlreadyCancelledLogin(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewOpenRouter(nil).Login(ctx, &openRouterInteraction{onAuthURL: func(string) {
		t.Error("cancelled login must not emit events")
	}})
	if err == nil || err.Error() != "Login cancelled" {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenRouterUsesConfiguredCallbackHost(t *testing.T) {
	t.Setenv("PI_OAUTH_CALLBACK_HOST", "localhost")
	ctx, cancel := context.WithCancel(context.Background())
	var callback *url.URL
	_, err := NewOpenRouter(nil).Login(ctx, &openRouterInteraction{onAuthURL: func(value string) {
		callback = openRouterCallbackURL(t, value)
		cancel()
	}})
	if err == nil || err.Error() != "Login cancelled" {
		t.Fatalf("error = %v", err)
	}
	if callback == nil || callback.Hostname() != "localhost" {
		t.Fatalf("callback URL = %v", callback)
	}
}

func TestOpenRouterMetadataRefreshAndToAuth(t *testing.T) {
	flow := NewOpenRouter(nil)
	if flow.Name() != "OpenRouter OAuth" || flow.LoginLabel() != "Sign in with OpenRouter" {
		t.Fatalf("OpenRouter labels = %q / %q", flow.Name(), flow.LoginLabel())
	}
	credential := auth.OAuthCredentialAccessFirst("token", "", 9007199254740991)
	refreshed, err := flow.Refresh(context.Background(), credential)
	if err != nil || refreshed != credential {
		t.Fatalf("refresh = %#v, %v, want the permanent credential unchanged", refreshed, err)
	}
	modelAuth, err := flow.ToAuth(credential)
	if err != nil || modelAuth.APIKey == nil || *modelAuth.APIKey != "token" {
		t.Fatalf("model auth = %#v, %v", modelAuth, err)
	}
}
