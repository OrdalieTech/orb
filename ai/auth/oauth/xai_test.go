package oauth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

type xAIInteraction struct{ events []auth.AuthEvent }

func (*xAIInteraction) Prompt(context.Context, auth.AuthPrompt) (string, error) { return "", nil }
func (interaction *xAIInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

func TestXAIDeviceLoginAndRefresh(t *testing.T) {
	requests := make(map[string][]string)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		requests[request.URL.Path] = append(requests[request.URL.Path], string(body))
		switch request.URL.Path {
		case "/device":
			_, _ = io.WriteString(writer, `{"device_code":"device-xai","user_code":"XAI-CODE","verification_uri":"https://auth.x.ai/device","verification_uri_complete":"https://auth.x.ai/device?user_code=XAI-CODE","interval":0,"expires_in":600}`)
		case "/token":
			if strings.HasPrefix(string(body), "grant_type=refresh_token") {
				_, _ = io.WriteString(writer, `{"access_token":"access-refreshed"}`)
			} else {
				_, _ = io.WriteString(writer, `{"access_token":"access-device","refresh_token":"refresh-device","expires_in":900}`)
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	flow := NewXAI(&XAIOptions{
		DeviceCodeURL: server.URL + "/device", TokenURL: server.URL + "/token",
		Now:   func() time.Time { return time.UnixMilli(1_700_000_000_000) },
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	interaction := &xAIInteraction{}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := credential.MarshalJSON()
	want := `{"type":"oauth","access":"access-device","refresh":"refresh-device","expires":1700000600000}`
	if string(encoded) != want {
		t.Fatalf("credential = %s, want %s", encoded, want)
	}
	if len(interaction.events) != 1 || interaction.events[0].VerificationURI != "https://auth.x.ai/device?user_code=XAI-CODE" || interaction.events[0].UserCode != "XAI-CODE" {
		t.Fatalf("events = %#v", interaction.events)
	}
	wantDevice := "client_id=b1a00492-073a-47ea-816f-4c329264a828&scope=openid+profile+email+offline_access+grok-cli%3Aaccess+api%3Aaccess&referrer=pi"
	if requests["/device"][0] != wantDevice {
		t.Fatalf("device form = %q, want %q", requests["/device"][0], wantDevice)
	}
	wantPoll := "grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code&client_id=b1a00492-073a-47ea-816f-4c329264a828&device_code=device-xai"
	if requests["/token"][0] != wantPoll {
		t.Fatalf("poll form = %q, want %q", requests["/token"][0], wantPoll)
	}

	refreshed, err := flow.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Access != "access-refreshed" || refreshed.Refresh != "refresh-device" || refreshed.Expires != 1_700_003_300_000 {
		t.Fatalf("refreshed = %#v", refreshed)
	}
	modelAuth, err := flow.ToAuth(refreshed)
	if err != nil || modelAuth.APIKey == nil || *modelAuth.APIKey != "access-refreshed" {
		t.Fatalf("model auth = %#v, %v", modelAuth, err)
	}
}

func TestXAIRejectsUntrustedVerificationURI(t *testing.T) {
	_, err := parseXAIDeviceCode(map[string]any{
		"device_code": "device", "user_code": "code", "verification_uri": "http://auth.x.ai/device", "expires_in": float64(60),
	})
	if err == nil || err.Error() != "Untrusted verification URI in xAI OAuth response" {
		t.Fatalf("error = %v", err)
	}
}

func TestXAINonObjectJSONIsAnEmptyOAuthObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()
	flow := NewXAI(&XAIOptions{DeviceCodeURL: server.URL})
	_, err := flow.Login(context.Background(), &xAIInteraction{})
	if err == nil || err.Error() != "Invalid xAI OAuth response field: device_code" {
		t.Fatalf("error = %v", err)
	}
}

func TestXAIOAuthMetadataAndDefaultClient(t *testing.T) {
	flow := NewXAI(nil)
	if flow.Name() != "xAI (Grok/X subscription)" || flow.LoginLabel() != "Sign in with SuperGrok or X Premium" {
		t.Fatalf("xAI labels = %q / %q", flow.Name(), flow.LoginLabel())
	}
	if flow.options.HTTPClient != http.DefaultClient {
		t.Fatal("default xAI OAuth client unexpectedly imposes a blanket timeout")
	}
}
