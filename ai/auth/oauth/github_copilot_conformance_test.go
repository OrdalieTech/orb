package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/conformance/runner"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestF2GitHubCopilotAccountPolicy(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name          string
			KnownModelIDs []string `json:"knownModelIds"`
			Models        json.RawMessage
			Host          string
			Requests      []string
			Progress      []string
			Credential    json.RawMessage
		}
	}
	runner.LoadJSON(t, "F2", "github-copilot-auth.json", &fixture)
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			var requests []string
			attempts := 0
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				pathname := request.URL.Path
				requests = append(requests, request.Method+" "+pathname)
				status := 200
				body := "{}"
				switch pathname {
				case "/login/device/code":
					body = `{"device_code":"device","user_code":"ABCD","verification_uri":"https://github.com/login/device","interval":0,"expires_in":900}`
				case "/login/oauth/access_token":
					body = `{"access_token":"refresh"}`
				case "/copilot_internal/v2/token":
					body = fmt.Sprintf(`{"token":"tid=fixture;proxy-ep=proxy.%s.githubcopilot.com","expires_at":1800000000}`, testCase.Host)
				case "/models":
					body = `{"data":` + string(testCase.Models) + `}`
					if testCase.Name == "rate-limited-policy" && attempts == 0 {
						status = 429
						body = `{"error":"rate limit"}`
					}
					attempts++
				default:
					if !strings.HasSuffix(pathname, "/policy") {
						t.Fatalf("unexpected path %s", pathname)
					}
					if testCase.Name == "rate-limited-policy" {
						status = 429
					}
				}
				return &http.Response{StatusCode: status, Status: fmt.Sprintf("%d %s", status, http.StatusText(status)), Header: http.Header{"Content-Type": []string{"application/json"}, "Retry-After": []string{"0"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			flow := NewGitHubCopilot(&GitHubCopilotOptions{HTTPClient: client, KnownModelIDs: testCase.KnownModelIDs, Sleep: func(context.Context, time.Duration) error { return nil }})
			interaction := &copilotInteraction{}
			credential, err := flow.Login(context.Background(), interaction)
			if err != nil {
				t.Fatal(err)
			}
			got, err := credential.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := json.Compact(&want, testCase.Credential); err != nil {
				t.Fatal(err)
			}
			if diff := runner.ByteDiff(want.Bytes(), got); diff != "" {
				t.Fatal(diff)
			}
			if !reflect.DeepEqual(requests, testCase.Requests) {
				t.Fatalf("requests = %v, want %v", requests, testCase.Requests)
			}
			progress := []string{}
			for _, event := range interaction.events {
				if event.Type == auth.EventProgress {
					progress = append(progress, event.Message)
				}
			}
			if !reflect.DeepEqual(progress, testCase.Progress) {
				t.Fatalf("progress = %v, want %v", progress, testCase.Progress)
			}
		})
	}
}
