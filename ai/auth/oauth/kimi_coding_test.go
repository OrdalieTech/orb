package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

type kimiInteraction struct{ events []auth.AuthEvent }

func (*kimiInteraction) Prompt(context.Context, auth.AuthPrompt) (string, error) {
	return "", nil
}

func (interaction *kimiInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

type kimiRequestLog struct {
	mu       sync.Mutex
	requests map[string][]string
}

func (log *kimiRequestLog) record(path, body string) {
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.requests == nil {
		log.requests = make(map[string][]string)
	}
	log.requests[path] = append(log.requests[path], body)
}

func (log *kimiRequestLog) get(path string) []string {
	log.mu.Lock()
	defer log.mu.Unlock()
	return append([]string(nil), log.requests[path]...)
}

const kimiDeviceAuthorizationBody = `{"user_code":"ABCD-1234","device_code":"device-code-123","verification_uri":"https://www.kimi.com/code","verification_uri_complete":"https://www.kimi.com/code?user_code=ABCD-1234","interval":5,"expires_in":600}`

func TestKimiCodingDeviceLogin(t *testing.T) {
	log := &kimiRequestLog{}
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		log.record(request.URL.Path, string(body))
		if request.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("headers = %#v", request.Header)
		}
		switch request.URL.Path {
		case "/api/oauth/device_authorization":
			_, _ = io.WriteString(writer, kimiDeviceAuthorizationBody)
		case "/api/oauth/token":
			polls++
			if polls == 1 {
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(writer, `{"error":"authorization_pending"}`)
				return
			}
			_, _ = io.WriteString(writer, `{"access_token":"access-token","refresh_token":"refresh-token","expires_in":3600}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	var sleeps []time.Duration
	flow := NewKimiCoding(&KimiCodingOptions{
		OAuthHost: server.URL,
		Now:       func() time.Time { return time.UnixMilli(1_700_000_000_000) },
		Sleep: func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		},
	})
	interaction := &kimiInteraction{}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := credential.MarshalJSON()
	want := `{"type":"oauth","access":"access-token","refresh":"refresh-token","expires":1700003600000}`
	if string(encoded) != want {
		t.Fatalf("credential = %s, want %s", encoded, want)
	}
	if len(interaction.events) != 1 {
		t.Fatalf("events = %#v", interaction.events)
	}
	event := interaction.events[0]
	if event.Type != auth.EventDeviceCode || event.UserCode != "ABCD-1234" ||
		event.VerificationURI != "https://www.kimi.com/code?user_code=ABCD-1234" ||
		event.IntervalSeconds != 5 || event.ExpiresInSeconds != 600 {
		t.Fatalf("device code event = %#v", event)
	}
	if device := log.get("/api/oauth/device_authorization"); len(device) != 1 || device[0] != "client_id=17e5f671-d194-4dfb-9706-5516cb48c098" {
		t.Fatalf("device authorization form = %#v", device)
	}
	wantPoll := "client_id=17e5f671-d194-4dfb-9706-5516cb48c098&device_code=device-code-123&grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code"
	if tokens := log.get("/api/oauth/token"); len(tokens) != 2 || tokens[0] != wantPoll || tokens[1] != wantPoll {
		t.Fatalf("token polls = %#v", tokens)
	}
	// waitBeforeFirstPoll: one interval-length sleep before each of the two polls.
	if len(sleeps) != 2 || sleeps[0] != 5*time.Second || sleeps[1] != 5*time.Second {
		t.Fatalf("sleeps = %v", sleeps)
	}
}

func kimiFailingLogin(t *testing.T, pollBody string, pollStatus int) error {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/oauth/device_authorization":
			_, _ = io.WriteString(writer, kimiDeviceAuthorizationBody)
		case "/api/oauth/token":
			writer.WriteHeader(pollStatus)
			_, _ = io.WriteString(writer, pollBody)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	flow := NewKimiCoding(&KimiCodingOptions{
		OAuthHost: server.URL,
		Sleep:     func(context.Context, time.Duration) error { return nil },
	})
	_, err := flow.Login(context.Background(), &kimiInteraction{})
	return err
}

func TestKimiCodingLoginFailures(t *testing.T) {
	if err := kimiFailingLogin(t, `{"error":"expired_token"}`, http.StatusBadRequest); err == nil ||
		err.Error() != "Kimi Code device authorization expired. Please restart login." {
		t.Fatalf("expired error = %v", err)
	}
	if err := kimiFailingLogin(t, `{"error":"access_denied"}`, http.StatusBadRequest); err == nil ||
		err.Error() != "Kimi Code login was denied." {
		t.Fatalf("denied error = %v", err)
	}
	if err := kimiFailingLogin(t, `{"error":"boom","error_description":"details"}`, http.StatusBadRequest); err == nil ||
		err.Error() != "Kimi Code device token request failed (status 400): boom: details" {
		t.Fatalf("generic error = %v", err)
	}
	if err := kimiFailingLogin(t, `upstream broke`, http.StatusBadGateway); err == nil ||
		err.Error() != "Kimi Code device token request failed with status 502: upstream broke" {
		t.Fatalf("5xx error = %v", err)
	}
}

func TestKimiCodingHonorsOAuthHostOverride(t *testing.T) {
	log := &kimiRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		log.record(request.URL.Path, "")
		switch request.URL.Path {
		case "/api/oauth/device_authorization":
			_, _ = io.WriteString(writer, `{"user_code":"ABCD-1234","device_code":"device-code-123","verification_uri":"https://www.kimi.com/code","verification_uri_complete":"https://www.kimi.com/code?user_code=ABCD-1234","interval":1,"expires_in":600}`)
		case "/api/oauth/token":
			_, _ = io.WriteString(writer, `{"access_token":"a","refresh_token":"r","expires_in":60}`)
		}
	}))
	defer server.Close()

	t.Setenv("KIMI_CODE_OAUTH_HOST", server.URL+"/")
	flow := NewKimiCoding(&KimiCodingOptions{Sleep: func(context.Context, time.Duration) error { return nil }})
	credential, err := flow.Login(context.Background(), &kimiInteraction{})
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "a" || credential.Refresh != "r" {
		t.Fatalf("credential = %#v", credential)
	}
	if len(log.get("/api/oauth/device_authorization")) != 1 || len(log.get("/api/oauth/token")) != 1 {
		t.Fatalf("requests = %#v", log.requests)
	}
}

func TestKimiCodingRefreshAndToAuth(t *testing.T) {
	log := &kimiRequestLog{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		log.record(request.URL.Path, string(body))
		if request.URL.Path != "/api/oauth/token" {
			writer.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = io.WriteString(writer, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
	}))
	defer server.Close()

	flow := NewKimiCoding(&KimiCodingOptions{
		OAuthHost: server.URL,
		Now:       func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	})
	credential, err := flow.Refresh(context.Background(), auth.OAuthCredentialAccessFirst("old-access", "old-refresh", 0))
	if err != nil {
		t.Fatal(err)
	}
	if credential.Access != "new-access" || credential.Refresh != "new-refresh" || credential.Expires != 1_700_003_600_000 {
		t.Fatalf("refreshed = %#v", credential)
	}
	wantForm := "client_id=17e5f671-d194-4dfb-9706-5516cb48c098&grant_type=refresh_token&refresh_token=old-refresh"
	if forms := log.get("/api/oauth/token"); len(forms) != 1 || forms[0] != wantForm {
		t.Fatalf("refresh form = %#v", forms)
	}

	modelAuth, err := flow.ToAuth(credential)
	if err != nil || modelAuth.APIKey != nil || modelAuth.Headers == nil ||
		modelAuth.Headers["Authorization"] == nil || *modelAuth.Headers["Authorization"] != "Bearer new-access" {
		t.Fatalf("model auth = %#v, %v", modelAuth, err)
	}
}

func TestKimiCodingRefreshRetriesAndUnauthorized(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":"temporarily_unavailable"}`)
			return
		}
		_, _ = io.WriteString(writer, `{"access_token":"a","refresh_token":"r","expires_in":60}`)
	}))
	defer server.Close()

	var sleeps []time.Duration
	flow := NewKimiCoding(&KimiCodingOptions{
		OAuthHost: server.URL,
		Sleep: func(_ context.Context, duration time.Duration) error {
			sleeps = append(sleeps, duration)
			return nil
		},
	})
	credential, err := flow.Refresh(context.Background(), auth.OAuthCredentialAccessFirst("old", "old", 0))
	if err != nil || credential.Access != "a" {
		t.Fatalf("refresh = %#v, %v", credential, err)
	}
	if calls != 2 || len(sleeps) != 1 || sleeps[0] != time.Second {
		t.Fatalf("calls = %d, sleeps = %v", calls, sleeps)
	}

	denied := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(writer, `{"error":"invalid_grant"}`)
	}))
	defer denied.Close()
	deniedFlow := NewKimiCoding(&KimiCodingOptions{OAuthHost: denied.URL})
	_, err = deniedFlow.Refresh(context.Background(), auth.OAuthCredentialAccessFirst("old", "old", 0))
	if err == nil || !strings.Contains(err.Error(), "unauthorized") ||
		err.Error() != "Kimi Code token refresh unauthorized (status 400)" {
		t.Fatalf("invalid_grant error = %v", err)
	}
}

func TestKimiCodingMetadata(t *testing.T) {
	flow := NewKimiCoding(nil)
	if flow.Name() != "Kimi Code (subscription)" || flow.LoginLabel() != "Sign in with Kimi Code" {
		t.Fatalf("Kimi labels = %q / %q", flow.Name(), flow.LoginLabel())
	}
	if flow.oauthHost() != "https://auth.kimi.com" {
		t.Fatalf("default host = %q", flow.oauthHost())
	}
}
