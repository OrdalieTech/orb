package oauth

import (
	"bytes"
	"context"
	"encoding/base64"
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

	"github.com/OrdalieTech/pi-go/ai/auth"
)

type codexInteraction struct {
	method string
	manual string
	events []auth.AuthEvent
	mu     sync.Mutex
}

func (interaction *codexInteraction) Prompt(_ context.Context, prompt auth.AuthPrompt) (string, error) {
	if prompt.Type == auth.PromptSelect {
		return interaction.method, nil
	}
	return interaction.manual, nil
}

func (interaction *codexInteraction) Notify(event auth.AuthEvent) {
	interaction.mu.Lock()
	interaction.events = append(interaction.events, event)
	interaction.mu.Unlock()
}

func TestOpenAICodexDeviceCodeLoginAndRefresh(t *testing.T) {
	access := codexTestToken(t, "account-123")
	requests := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests[request.URL.Path] = string(body)
		switch request.URL.Path {
		case "/usercode":
			_, _ = io.WriteString(writer, `{"device_auth_id":"device-1","user_code":"ABCD-EFGH","interval":"0"}`)
		case "/device-token":
			_, _ = io.WriteString(writer, `{"authorization_code":"authorization-1","code_verifier":"server-verifier"}`)
		case "/oauth-token":
			_, _ = io.WriteString(writer, `{"access_token":`+quoted(access)+`,"refresh_token":"refresh-1","expires_in":3600}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	flow := NewOpenAICodex(&OpenAICodexOptions{
		DeviceUserCodeURL: server.URL + "/usercode", DeviceTokenURL: server.URL + "/device-token",
		DeviceVerificationURI: "https://auth.example/codex/device", DeviceRedirectURI: "https://auth.example/device/callback",
		TokenURL: server.URL + "/oauth-token", Now: func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	})
	interaction := &codexInteraction{method: openAICodexDeviceMethod}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := credential.MarshalJSON()
	wantCredential := `{"type":"oauth","access":` + quoted(access) + `,"refresh":"refresh-1","expires":1700003600000,"accountId":"account-123"}`
	if string(encoded) != wantCredential {
		t.Fatalf("credential = %s, want %s", encoded, wantCredential)
	}
	if requests["/usercode"] != `{"client_id":"app_EMoamEEZ73f0CkXaXp7hrann"}` || requests["/device-token"] != `{"device_auth_id":"device-1","user_code":"ABCD-EFGH"}` {
		t.Fatalf("device requests = %#v", requests)
	}
	wantExchange := "grant_type=authorization_code&client_id=app_EMoamEEZ73f0CkXaXp7hrann&code=authorization-1&code_verifier=server-verifier&redirect_uri=https%3A%2F%2Fauth.example%2Fdevice%2Fcallback"
	if requests["/oauth-token"] != wantExchange {
		t.Fatalf("exchange body = %q, want %q", requests["/oauth-token"], wantExchange)
	}
	if len(interaction.events) != 1 || interaction.events[0].Type != auth.EventDeviceCode || interaction.events[0].UserCode != "ABCD-EFGH" || interaction.events[0].ExpiresInSeconds != 900 {
		t.Fatalf("events = %#v", interaction.events)
	}

	refreshed, err := flow.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != access || refreshed.Refresh != "refresh-1" {
		t.Fatalf("refreshed = %#v", refreshed)
	}
	wantRefresh := "grant_type=refresh_token&refresh_token=refresh-1&client_id=app_EMoamEEZ73f0CkXaXp7hrann"
	if requests["/oauth-token"] != wantRefresh {
		t.Fatalf("refresh body = %q, want %q", requests["/oauth-token"], wantRefresh)
	}
	modelAuth, err := flow.ToAuth(refreshed)
	if err != nil || modelAuth.APIKey == nil || *modelAuth.APIKey != access {
		t.Fatalf("model auth = %#v, %v", modelAuth, err)
	}
}

func TestOpenAICodexBrowserManualLogin(t *testing.T) {
	access := codexTestToken(t, "browser-account")
	var tokenBody string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		tokenBody = string(body)
		_, _ = io.WriteString(writer, `{"access_token":`+quoted(access)+`,"refresh_token":"browser-refresh","expires_in":60}`)
	}))
	defer server.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	flow := NewOpenAICodex(&OpenAICodexOptions{
		AuthorizeURL: "https://auth.example/authorize", TokenURL: server.URL,
		CallbackHost: "127.0.0.1", CallbackPort: port,
		RedirectURI: "http://localhost:" + stringPort(port) + openAICodexCallbackPath,
		Random:      bytes.NewReader(make([]byte, 48)),
		Listen:      func(_, _ string) (net.Listener, error) { return listener, nil },
	})
	interaction := &codexInteraction{method: openAICodexBrowserMethod, manual: "manual-code"}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != access || credential.Refresh != "browser-refresh" {
		t.Fatalf("credential = %#v", credential)
	}
	if len(interaction.events) != 1 || interaction.events[0].Type != auth.EventAuthURL {
		t.Fatalf("events = %#v", interaction.events)
	}
	authorize, _ := url.Parse(interaction.events[0].URL)
	query := authorize.Query()
	if query.Get("client_id") != openAICodexClientID || query.Get("state") != strings.Repeat("0", 32) || query.Get("originator") != "pi" || query.Get("code_challenge") != "DwBzhbb51LfusnSGBa_hqYSgo7-j8BTQnip4TOnlzRo" {
		t.Fatalf("authorize query = %s", authorize.RawQuery)
	}
	wantBody := "grant_type=authorization_code&client_id=app_EMoamEEZ73f0CkXaXp7hrann&code=manual-code&code_verifier=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&redirect_uri=" + url.QueryEscape(flow.options.RedirectURI)
	if tokenBody != wantBody {
		t.Fatalf("token body = %q, want %q", tokenBody, wantBody)
	}
}

func TestOpenAICodexRejectsTokenWithoutAccount(t *testing.T) {
	flow := NewOpenAICodex(nil)
	_, err := flow.credentialFromToken(openAICodexToken{access: "not-a-jwt", refresh: "refresh", expires: 1})
	if err == nil || err.Error() != "Failed to extract accountId from token" {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenAICodexDefaultClientHasNoBlanketTimeout(t *testing.T) {
	if flow := NewOpenAICodex(nil); flow.options.HTTPClient != http.DefaultClient {
		t.Fatal("default OpenAI Codex OAuth client unexpectedly imposes a blanket timeout")
	}
}

func codexTestToken(t *testing.T, accountID string) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{openAICodexJWTClaim: map[string]string{"chatgpt_account_id": accountID}})
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func quoted(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func stringPort(port int) string { return strings.TrimPrefix((&net.TCPAddr{Port: port}).String(), ":") }
