package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

func TestNormalizeAzureOpenAIBaseURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "cognitive services root", input: "https://resource.cognitiveservices.azure.com", want: "https://resource.cognitiveservices.azure.com/openai/v1"},
		{name: "foundry root", input: "https://resource.ai.azure.com", want: "https://resource.ai.azure.com/openai/v1"},
		{name: "openai root", input: "https://resource.openai.azure.com", want: "https://resource.openai.azure.com/openai/v1"},
		{name: "openai path", input: "https://resource.cognitiveservices.azure.com/openai", want: "https://resource.cognitiveservices.azure.com/openai/v1"},
		{name: "v1 path", input: "https://resource.cognitiveservices.azure.com/openai/v1", want: "https://resource.cognitiveservices.azure.com/openai/v1"},
		{name: "responses path", input: "https://resource.services.ai.azure.com/openai/v1/responses", want: "https://resource.services.ai.azure.com/openai/v1"},
		{name: "Azure query removed", input: "https://resource.openai.azure.com/openai?api-version=old", want: "https://resource.openai.azure.com/openai/v1"},
		{name: "proxy preserved", input: "https://proxy.example.com/custom/v1", want: "https://proxy.example.com/custom/v1"},
		{name: "proxy query preserved", input: "https://proxy.example.com/v1?custom=true", want: "https://proxy.example.com/v1?custom=true"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAzureOpenAIBaseURL(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("normalized URL = %q, want %q", got, test.want)
			}
		})
	}
	if _, err := normalizeAzureOpenAIBaseURL("not-a-url"); err == nil {
		t.Fatal("invalid Azure URL was accepted")
	}
}

func TestAzureOpenAIMaxRetries(t *testing.T) {
	previousClient := azureOpenAIHTTPClient
	attempts := 0
	azureOpenAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusInternalServerError
		body := "retry"
		if attempts == 2 {
			status = http.StatusOK
			body = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"azure-retry\",\"status\":\"completed\",\"output\":[]}}\n\n"
		}
		return &http.Response{
			StatusCode: status, Status: http.StatusText(status),
			Header: http.Header{"Content-Type": []string{"text/event-stream"}, "Retry-After-Ms": []string{"0"}},
			Body:   io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})}
	t.Cleanup(func() { azureOpenAIHTTPClient = previousClient })

	key := "test-key"
	baseURL := "https://resource.openai.azure.com"
	retries := 1
	model := &ai.Model{ID: "deployment", API: ai.APIAzureOpenAIResponses, Provider: "azure-openai-responses"}
	stream, err := StreamAzureOpenAIResponsesWithOptions(context.Background(), model, ai.Context{}, &AzureOpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key, MaxRetries: &retries}, AzureBaseURL: &baseURL,
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := ai.Collect(stream)
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || message.StopReason != ai.StopReasonStop {
		t.Fatalf("attempts = %d, result = %#v", attempts, message)
	}
}

func TestAzureOpenAIRequestUsesAzureAuthAndSDKQueryReplacement(t *testing.T) {
	previousClient := azureOpenAIHTTPClient
	var captured *http.Request
	var body []byte
	azureOpenAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request
		var err error
		body, err = io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK, Status: "200 OK", Request: request,
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"azure-query\",\"status\":\"completed\",\"output\":[]}}\n\n",
			)),
		}, nil
	})}
	t.Cleanup(func() { azureOpenAIHTTPClient = previousClient })

	key := "test-key"
	baseURL := "https://proxy.example.com/custom/v1?custom=true"
	apiVersion := "v1"
	deployment := "explicit-deployment"
	model := &ai.Model{ID: "model", API: ai.APIAzureOpenAIResponses, Provider: "azure-openai-responses"}
	stream, err := StreamAzureOpenAIResponsesWithOptions(context.Background(), model, ai.Context{}, &AzureOpenAIResponsesOptions{
		StreamOptions: ai.StreamOptions{APIKey: &key}, AzureBaseURL: &baseURL, AzureAPIVersion: &apiVersion, AzureDeploymentName: &deployment,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	if captured == nil {
		t.Fatal("request was not captured")
	}
	if got := captured.URL.String(); got != "https://proxy.example.com/custom/v1?api-version=v1" {
		t.Fatalf("request URL = %q", got)
	}
	if got := captured.Header.Get("api-key"); got != key {
		t.Fatalf("api-key = %q", got)
	}
	if got := captured.Header.Get("Authorization"); got != "" {
		t.Fatalf("Authorization = %q, want empty", got)
	}
	var payload OpenAIResponsesPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Model != deployment {
		t.Fatalf("deployment model = %q", payload.Model)
	}
}

func TestAzureDeploymentNameResolution(t *testing.T) {
	t.Setenv("AZURE_OPENAI_DEPLOYMENT_NAME_MAP", "")
	model := &ai.Model{ID: "gpt-4o-mini"}
	options := &AzureOpenAIResponsesOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
		"AZURE_OPENAI_DEPLOYMENT_NAME_MAP": "other=ignored, gpt-4o-mini=mini-deployment, invalid, empty=",
	}}}
	if got := resolveAzureDeploymentName(model, options); got != "mini-deployment" {
		t.Fatalf("deployment = %q, want mini-deployment", got)
	}
	explicit := "explicit-deployment"
	options.AzureDeploymentName = &explicit
	if got := resolveAzureDeploymentName(model, options); got != explicit {
		t.Fatalf("explicit deployment = %q, want %q", got, explicit)
	}
}

func TestResolveAzureOpenAIConfig(t *testing.T) {
	for _, name := range []string{"AZURE_OPENAI_BASE_URL", "AZURE_OPENAI_RESOURCE_NAME", "AZURE_OPENAI_API_VERSION"} {
		t.Setenv(name, "")
	}
	model := &ai.Model{BaseURL: "https://model.openai.azure.com"}
	options := &AzureOpenAIResponsesOptions{StreamOptions: ai.StreamOptions{Env: ai.ProviderEnv{
		"AZURE_OPENAI_RESOURCE_NAME": "env-resource",
		"AZURE_OPENAI_API_VERSION":   "2025-04-01-preview",
	}}}
	config, err := resolveAzureOpenAIConfig(model, options)
	if err != nil {
		t.Fatal(err)
	}
	if config.baseURL != "https://env-resource.openai.azure.com/openai/v1" || config.apiVersion != "2025-04-01-preview" {
		t.Fatalf("config = %#v", config)
	}

	baseURL := "https://proxy.example.com/custom/v1"
	apiVersion := "v1"
	options.AzureBaseURL = &baseURL
	options.AzureAPIVersion = &apiVersion
	config, err = resolveAzureOpenAIConfig(model, options)
	if err != nil {
		t.Fatal(err)
	}
	if config.baseURL != baseURL || config.apiVersion != apiVersion {
		t.Fatalf("explicit config = %#v", config)
	}
}

func TestAzureOpenAISimpleReasoningOff(t *testing.T) {
	previousClient := azureOpenAIHTTPClient
	azureOpenAIHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("X-Extension") != "yes" {
			t.Errorf("hooked header = %q", request.Header.Get("X-Extension"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"azure-off\",\"status\":\"completed\",\"output\":[]}}\n\n",
			)),
			Request: request,
		}, nil
	})}
	t.Cleanup(func() { azureOpenAIHTTPClient = previousClient })

	key := "test-key"
	reasoning := ai.ThinkingLevel("off")
	model := &ai.Model{
		ID: "deployment", API: ai.APIAzureOpenAIResponses, Provider: "azure-openai-responses",
		BaseURL: "https://resource.openai.azure.com", Reasoning: true, MaxTokens: 1_024,
	}
	var captured *OpenAIResponsesPayload
	stream, err := StreamSimpleAzureOpenAIResponses(context.Background(), model, ai.Context{}, &ai.SimpleStreamOptions{
		StreamOptions: ai.StreamOptions{
			APIKey: &key,
			OnPayload: func(_ context.Context, payload any, _ *ai.Model) (any, bool, error) {
				captured = payload.(*OpenAIResponsesPayload)
				return nil, false, nil
			},
			TransformHeaders: func(_ context.Context, headers ai.ProviderHeaders, _ *ai.Model) (ai.ProviderHeaders, error) {
				value := "yes"
				headers["X-Extension"] = &value
				return headers, nil
			},
		},
		Reasoning: &reasoning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ai.Collect(stream); err != nil {
		t.Fatal(err)
	}
	if captured == nil || captured.Reasoning == nil {
		t.Fatalf("reasoning payload = %#v, want disabled effort", captured)
	}
	if captured.Reasoning.Effort != "none" || captured.Reasoning.Summary != nil || len(captured.Include) != 0 {
		t.Fatalf("reasoning payload = %#v, want effort none without summary/include", captured)
	}
}
