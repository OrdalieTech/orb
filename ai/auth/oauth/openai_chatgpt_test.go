package oauth

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/auth"
)

type chatGPTInteraction struct {
	openRouterInteraction
	deviceID string
}

func (interaction *chatGPTInteraction) DeviceID() (string, error) { return interaction.deviceID, nil }

func TestOpenAIChatGPTLoginAndRefresh(t *testing.T) {
	var forms []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_ = request.ParseForm()
		forms = append(forms, request.PostForm)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"access_token": "access", "refresh_token": "refresh", "id_token": "id", "expires_in": 3600,
			"scope": "openid " + openAIChatGPTDirectScope,
		})
	}))
	defer server.Close()
	now := time.UnixMilli(1_000_000)
	flow := NewOpenAIChatGPT(&OpenAIChatGPTOptions{
		TokenURL: server.URL, Now: func() time.Time { return now },
		// The callback port is taken, so the pasted redirect URL completes login.
		Listen: func(string, string) (net.Listener, error) { return nil, errors.New("port in use") },
	})
	interaction := &chatGPTInteraction{deviceID: "0F8FAD5B-D9CB-469F-A165-70867728950E"}
	var authorize *url.URL
	interaction.onAuthURL = func(raw string) { authorize, _ = url.Parse(raw) }
	interaction.prompt = func(context.Context, auth.AuthPrompt) (string, error) {
		state := authorize.Query().Get("state")
		return "http://127.0.0.1:1455/auth/callback?code=code&state=" + state + "&client_id=issued", nil
	}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	if got := authorize.Query().Get("ext_agent_host_id"); got != "urn:uuid:0f8fad5b-d9cb-469f-a165-70867728950e" {
		t.Fatalf("agent host id = %q", got)
	}
	if credential.Access != "access" || credential.Expires != now.Add(time.Hour-openAIChatGPTExpiryMargin).UnixMilli() ||
		!strings.Contains(string(credential.Extra["clientId"]), "issued") {
		t.Fatalf("credential = %#v", credential)
	}
	if forms[0].Get("client_id") != "issued" || forms[0].Get("resource") != openAIChatGPTResource {
		t.Fatalf("exchange form = %v", forms[0])
	}
	if _, err := flow.Refresh(context.Background(), credential); err != nil || forms[1].Get("grant_type") != "refresh_token" || forms[1].Get("client_id") != "issued" {
		t.Fatalf("refresh form = %v, %v", forms[1], err)
	}
	interaction.deviceID = ""
	if _, err := flow.Login(context.Background(), interaction); err == nil || !strings.Contains(err.Error(), "device ID") {
		t.Fatalf("login without device id = %v", err)
	}
}
