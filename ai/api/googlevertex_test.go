package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestGoogleVertexStreamRequestHeadersAndEvents(t *testing.T) {
	model := vertexTestModel("gemini-2.5-flash-lite")
	modelHeaders := map[string]string{
		"Content-Type": "application/model-json",
		"X-Custom":     "model",
	}
	model.Headers = &modelHeaders

	apiKey := "vertex-key"
	overrideKey := "header-key"
	contentType := "application/option-json"
	customHeader := "option"
	temperature := 0.0
	maxTokens := 12.0
	reasoning := ai.ThinkingMinimal
	var capturedURL string
	var capturedHeader http.Header
	var capturedBody []byte
	previousClient := googleHTTPClient
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		capturedURL = request.URL.String()
		capturedHeader = request.Header.Clone()
		var err error
		capturedBody, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return googleTestResponse("data: {\"responseId\":\"vertex-response\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"thinking\",\"thought\":true,\"thoughtSignature\":\"signature\"},{\"text\":\"hello\"},{\"functionCall\":{\"id\":\"call\",\"name\":\"echo\",\"args\":{\"x\":1}}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":10,\"cachedContentTokenCount\":2,\"candidatesTokenCount\":3,\"thoughtsTokenCount\":4,\"totalTokenCount\":17}}\n\n"), nil
	})}
	t.Cleanup(func() { googleHTTPClient = previousClient })

	requestContext := ai.Context{
		SystemPrompt: vertexStringPointer("system"),
		Messages: ai.MessageList{
			&ai.UserMessage{Content: ai.NewUserText("hello")},
		},
	}
	stream, err := StreamSimpleGoogleVertex(context.Background(), model, requestContext, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &apiKey, Temperature: &temperature, MaxTokens: &maxTokens,
			Headers: ai.ProviderHeaders{
				"Content-Type":   &contentType,
				"X-Custom":       &customHeader,
				"X-Goog-Api-Key": &overrideKey,
			},
		},
		Reasoning: &reasoning,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}

	wantURL := "https://aiplatform.googleapis.com/v1/publishers/google/models/gemini-2.5-flash-lite:streamGenerateContent?alt=sse"
	if capturedURL != wantURL {
		t.Fatalf("request URL = %q, want %q", capturedURL, wantURL)
	}
	if got := capturedHeader.Get("Content-Type"); got != contentType {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := capturedHeader.Get("X-Custom"); got != customHeader {
		t.Fatalf("X-Custom = %q", got)
	}
	if got := capturedHeader.Get("X-Goog-Api-Key"); got != overrideKey {
		t.Fatalf("X-Goog-Api-Key = %q", got)
	}
	wantBody := `{"contents":[{"parts":[{"text":"hello"}],"role":"user"}],"systemInstruction":{"parts":[{"text":"system"}],"role":"user"},"generationConfig":{"temperature":0,"maxOutputTokens":12,"thinkingConfig":{"includeThoughts":true,"thinkingBudget":128}}}`
	if string(capturedBody) != wantBody {
		t.Fatalf("request body mismatch\nwant: %s\n got: %s", wantBody, capturedBody)
	}
	if message.API != ai.APIGoogleVertex || message.Provider != "google-vertex" || message.ResponseID == nil || *message.ResponseID != "vertex-response" {
		t.Fatalf("message identity = %#v", message)
	}
	if message.StopReason != ai.StopReasonToolUse || len(message.Content) != 3 {
		t.Fatalf("message content/stop = %#v / %q", message.Content, message.StopReason)
	}
	if message.Usage.Input != 8 || message.Usage.Output != 7 || message.Usage.CacheRead != 2 || message.Usage.Reasoning == nil || *message.Usage.Reasoning != 4 || message.Usage.TotalTokens != 17 {
		t.Fatalf("usage = %#v", message.Usage)
	}
}

func TestGoogleVertexADCStreamAuthAndHeaderOverrides(t *testing.T) {
	credentialPath := filepath.Join(t.TempDir(), "application_default_credentials.json")
	credential := `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":"refresh","quota_project_id":"quota-project"}`
	if err := os.WriteFile(credentialPath, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}

	previousAuthClient := googleVertexAuthHTTPClient
	previousGoogleClient := googleHTTPClient
	t.Cleanup(func() {
		googleVertexAuthHTTPClient = previousAuthClient
		googleHTTPClient = previousGoogleClient
	})

	tokenCalls := 0
	googleVertexAuthHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		tokenCalls++
		if request.URL.String() != googleVertexTokenURL {
			t.Fatalf("token URL = %q", request.URL)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			return nil, err
		}
		if values.Get("client_id") != "client" || values.Get("client_secret") != "secret" || values.Get("refresh_token") != "refresh" || values.Get("grant_type") != "refresh_token" {
			t.Fatalf("token form = %#v", values)
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"access_token":"adc-token","expires_in":3600,"token_type":"Bearer"}`)),
		}, nil
	})}

	var vertexRequests []http.Header
	googleHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		vertexRequests = append(vertexRequests, request.Header.Clone())
		return googleTestResponse("data: {\"candidates\":[{\"content\":{\"parts\":[]},\"finishReason\":\"STOP\"}]}\n\n"), nil
	})}

	baseOptions := func() *GoogleVertexOptions {
		return &GoogleVertexOptions{
			StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{"GOOGLE_APPLICATION_CREDENTIALS": credentialPath}},
			Project:       "project", Location: "global",
		}
	}
	model := vertexTestModel("gemini-3-flash-preview")
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello")},
	}}
	for index := 0; index < 2; index++ {
		options := baseOptions()
		if index == 1 {
			authorization := "Bearer custom-token"
			quota := "custom-quota"
			options.Headers = ai.ProviderHeaders{
				"Authorization":       &authorization,
				"X-Goog-User-Project": &quota,
			}
		}
		stream, err := StreamGoogleVertexWithOptions(context.Background(), model, requestContext, options)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ai.Collect(stream); err != nil {
			t.Fatal(err)
		}
	}
	if tokenCalls != 2 {
		t.Fatalf("token calls = %d, want one fresh ADC client per stream", tokenCalls)
	}
	if len(vertexRequests) != 2 {
		t.Fatalf("Vertex requests = %d", len(vertexRequests))
	}
	if got := vertexRequests[0].Get("Authorization"); got != "Bearer adc-token" {
		t.Fatalf("ADC Authorization = %q", got)
	}
	if got := vertexRequests[0].Get("X-Goog-User-Project"); got != "quota-project" {
		t.Fatalf("ADC quota project = %q", got)
	}
	if got := vertexRequests[1].Get("Authorization"); got != "Bearer custom-token" {
		t.Fatalf("custom Authorization = %q", got)
	}
	if got := vertexRequests[1].Get("X-Goog-User-Project"); got != "custom-quota" {
		t.Fatalf("custom quota project = %q", got)
	}
}

func vertexTestModel(id string) *ai.Model {
	return &ai.Model{
		ID: id, Name: id, API: ai.APIGoogleVertex, Provider: "google-vertex",
		Reasoning: true, Input: ai.InputModalities{ai.InputText, ai.InputImage},
		ContextWindow: 1_000_000, MaxTokens: 65_536,
	}
}

func vertexStringPointer(value string) *string { return &value }
