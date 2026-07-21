package oauth

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/ai/auth"
)

type copilotInteraction struct {
	input  string
	events []auth.AuthEvent
}

func (interaction *copilotInteraction) Prompt(context.Context, auth.AuthPrompt) (string, error) {
	return interaction.input, nil
}

func (interaction *copilotInteraction) Notify(event auth.AuthEvent) {
	interaction.events = append(interaction.events, event)
}

func TestGitHubCopilotDeviceLoginRefreshAndHeaders(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string][]string)
	record := func(request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		mu.Lock()
		requests[request.URL.Path] = append(requests[request.URL.Path], string(body)+"|"+request.Header.Get("Authorization"))
		mu.Unlock()
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		record(request)
		switch request.URL.Path {
		case "/device":
			_, _ = io.WriteString(writer, `{"device_code":"device","user_code":"ABCD-EFGH","verification_uri":"https://github.example/device","interval":0,"expires_in":900}`)
		case "/access":
			_, _ = io.WriteString(writer, `{"access_token":"github-access"}`)
		case "/copilot-token":
			_, _ = io.WriteString(writer, `{"token":"tid=1;proxy-ep=proxy.enterprise.githubcopilot.example;exp=1800000000","expires_at":1800000000}`)
		case "/api/models/fixture-model/policy":
			_, _ = io.WriteString(writer, `{}`)
		case "/api/models":
			_, _ = io.WriteString(writer, `{"data":[{"id":"gpt-4.1","model_picker_enabled":true,"policy":{"state":"enabled"},"capabilities":{"supports":{"tool_calls":true}}},{"id":"disabled","model_picker_enabled":true,"policy":{"state":"disabled"}},{"id":"no-tools","model_picker_enabled":true,"capabilities":{"supports":{"tool_calls":false}}},{"id":"hidden","model_picker_enabled":false}]}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	flow := NewGitHubCopilot(&GitHubCopilotOptions{
		DeviceCodeURL: server.URL + "/device", AccessTokenURL: server.URL + "/access",
		CopilotTokenURL: server.URL + "/copilot-token", CopilotBaseURL: server.URL + "/api",
		KnownModelIDs: []string{"fixture-model"},
		Sleep:         func(context.Context, time.Duration) error { return nil },
	})
	interaction := &copilotInteraction{input: "https://company.ghe.test/enterprise"}
	credential, err := flow.Login(context.Background(), interaction)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := credential.MarshalJSON()
	want := `{"type":"oauth","refresh":"github-access","access":"tid=1;proxy-ep=proxy.enterprise.githubcopilot.example;exp=1800000000","expires":1799999700000,"enterpriseUrl":"company.ghe.test","availableModelIds":["gpt-4.1"]}`
	if string(encoded) != want {
		t.Fatalf("credential = %s, want %s", encoded, want)
	}
	if len(interaction.events) != 2 || interaction.events[0].Type != auth.EventDeviceCode || interaction.events[0].VerificationURI != "https://github.example/device" || interaction.events[1].Message != "Enabling models..." {
		t.Fatalf("events = %#v", interaction.events)
	}
	if requests["/device"][0] != "client_id=Iv1.b507a08c87ecfe98&scope=read%3Auser|" {
		t.Fatalf("device request = %v", requests["/device"])
	}
	if requests["/access"][0] != "client_id=Iv1.b507a08c87ecfe98&device_code=device&grant_type=urn%3Aietf%3Aparams%3Aoauth%3Agrant-type%3Adevice_code|" {
		t.Fatalf("access request = %v", requests["/access"])
	}
	if got := requests["/copilot-token"][0]; !strings.HasSuffix(got, "|Bearer github-access") {
		t.Fatalf("Copilot token request = %q", got)
	}
	if got := requests["/api/models/fixture-model/policy"][0]; !strings.HasSuffix(got, "|Bearer tid=1;proxy-ep=proxy.enterprise.githubcopilot.example;exp=1800000000") {
		t.Fatalf("policy request = %q", got)
	}

	modelAuth, err := flow.ToAuth(credential)
	if err != nil || modelAuth.APIKey == nil || modelAuth.BaseURL == nil || *modelAuth.BaseURL != "https://api.enterprise.githubcopilot.example" {
		t.Fatalf("model auth = %#v, %v", modelAuth, err)
	}
	available, ok := CopilotAvailableModelIDs(credential)
	if !ok || len(available) != 1 || available[0] != "gpt-4.1" {
		t.Fatalf("available = %v, %v", available, ok)
	}
	refreshed, err := flow.Refresh(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if domain := copilotEnterpriseDomain(refreshed); domain != "company.ghe.test" {
		t.Fatalf("enterprise domain = %q", domain)
	}
}

func TestGitHubCopilotBaseURLAndTrust(t *testing.T) {
	if got := GitHubCopilotBaseURL("tid=x;proxy-ep=proxy.individual.githubcopilot.com;exp=1", ""); got != "https://api.individual.githubcopilot.com" {
		t.Fatalf("base URL = %q", got)
	}
	if got := GitHubCopilotBaseURL("opaque", "company.ghe.com"); got != "https://copilot-api.company.ghe.com" {
		t.Fatalf("enterprise base URL = %q", got)
	}
	if _, err := trustedVerificationURL("file:///tmp/program", false); err == nil {
		t.Fatal("trusted file verification URI")
	}
}

func TestGitHubCopilotOmittedIntervalUsesPollerDefault(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, `{"device_code":"device","user_code":"code","verification_uri":"https://github.example/device","expires_in":900}`)
	}))
	defer server.Close()
	flow := NewGitHubCopilot(&GitHubCopilotOptions{DeviceCodeURL: server.URL})
	device, err := flow.startDeviceFlow(context.Background(), "github.com")
	if err != nil {
		t.Fatal(err)
	}
	if device.interval != nil || githubDeviceInterval(device) != 0 {
		t.Fatalf("omitted interval = %v", device.interval)
	}
}

func TestGitHubCopilotCurrentCompatibilityEdges(t *testing.T) {
	flow := NewGitHubCopilot(&GitHubCopilotOptions{KnownModelIDs: []string{}})
	if flow.options.HTTPClient != http.DefaultClient {
		t.Fatal("default OAuth client unexpectedly imposes a blanket timeout")
	}
	if got := GitHubCopilotBaseURL("prefix-proxy-ep=proxy.business.githubcopilot.com;suffix", ""); got != "https://api.business.githubcopilot.com" {
		t.Fatalf("base URL from embedded proxy-ep = %q", got)
	}

	nullCredential := auth.OAuthCredential("refresh", "access", 1)
	nullCredential.SetExtra("availableModelIds", json.RawMessage("null"))
	if ids, filtered := CopilotAvailableModelIDs(nullCredential); filtered || ids != nil {
		t.Fatalf("null availableModelIds = %v, filtered=%v", ids, filtered)
	}
	emptyCredential := auth.OAuthCredential("refresh", "access", 1)
	emptyCredential.SetExtra("availableModelIds", json.RawMessage("[]"))
	if ids, filtered := CopilotAvailableModelIDs(emptyCredential); !filtered || len(ids) != 0 {
		t.Fatalf("empty availableModelIds = %v, filtered=%v", ids, filtered)
	}

	got, err := trustedVerificationURL("https://github.com/login/\x1b]8;;evil", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://github.com/login/%1B]8;;evil" {
		t.Fatalf("normalized verification URI = %q", got)
	}
}

func TestGitHubCopilotDefersDefaultModelCatalogUntilNeeded(t *testing.T) {
	flow := NewGitHubCopilot(nil)
	if flow.options.KnownModelIDs != nil {
		t.Fatal("default Copilot model catalog was loaded during construction")
	}
	if modelIDs := flow.knownModelIDs(); len(modelIDs) == 0 {
		t.Fatal("default Copilot model catalog was not loaded on demand")
	}
	if flow.options.KnownModelIDs != nil {
		t.Fatal("lazy default model catalog replaced the nil option sentinel")
	}
}

func TestGitHubCopilotModelsRequestAloneHasFiveSecondTimeout(t *testing.T) {
	client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		if !ok {
			t.Fatal("models request has no deadline")
		}
		remaining := time.Until(deadline)
		if remaining < 4*time.Second || remaining > 5*time.Second {
			t.Fatalf("models deadline remaining = %s", remaining)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
		}, nil
	})}
	flow := NewGitHubCopilot(&GitHubCopilotOptions{HTTPClient: client, CopilotBaseURL: "https://copilot.test", KnownModelIDs: []string{}})
	ids, err := flow.fetchAvailableModels(context.Background(), "token", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("models = %v", ids)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (roundTrip roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
