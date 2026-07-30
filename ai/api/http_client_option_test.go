package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestPerRequestHTTPClientSupportedAdapters(t *testing.T) {
	key := "test-key"
	zeroRetries := 0
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}
	for _, test := range []struct {
		name string
		run  func(*testing.T, *http.Client)
	}{
		{name: "Anthropic", run: func(t *testing.T, client *http.Client) {
			model := anthropicTestModel()
			model.BaseURL = "https://ambient.invalid"
			stream, err := StreamAnthropicMessagesWithOptions(context.Background(), model, requestContext, &AnthropicMessagesOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "OpenAI Completions", run: func(t *testing.T, client *http.Client) {
			model := &ai.Model{ID: "fixture", API: ai.APIOpenAICompletions, Provider: "openai", BaseURL: "https://ambient.invalid/v1", MaxTokens: 128}
			stream, err := StreamOpenAICompletionsWithOptions(context.Background(), model, requestContext, &OpenAICompletionsOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "OpenAI Responses", run: func(t *testing.T, client *http.Client) {
			model := responsesTestModel()
			model.BaseURL = "https://ambient.invalid/v1"
			stream, err := StreamOpenAIResponsesWithOptions(context.Background(), model, requestContext, &OpenAIResponsesOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "Azure OpenAI Responses", run: func(t *testing.T, client *http.Client) {
			baseURL := "https://fixture.openai.azure.com"
			model := &ai.Model{ID: "deployment", API: ai.APIAzureOpenAIResponses, Provider: "azure-openai-responses", MaxTokens: 128}
			stream, err := StreamAzureOpenAIResponsesWithOptions(context.Background(), model, requestContext, &AzureOpenAIResponsesOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
				AzureBaseURL:  &baseURL,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "Mistral", run: func(t *testing.T, client *http.Client) {
			model := &ai.Model{ID: "fixture", API: ai.APIMistralConversations, Provider: "mistral", BaseURL: "https://ambient.invalid", MaxTokens: 128}
			stream, err := StreamMistralConversationsWithOptions(context.Background(), model, requestContext, &MistralConversationsOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "Codex SSE", run: func(t *testing.T, client *http.Client) {
			model := codexTestModel()
			model.BaseURL = "https://ambient.invalid/backend-api"
			token := codexAPITestToken(t, "account-fixture")
			transport := ai.TransportSSE
			stream, err := StreamOpenAICodexResponsesWithOptions(context.Background(), &model, requestContext, &OpenAICodexResponsesOptions{
				StreamOptions: ai.StreamOptions{APIKey: &token, HTTPClient: client, Transport: &transport, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
		{name: "pi-messages", run: func(t *testing.T, client *http.Client) {
			model := piMessagesTestModel("https://ambient.invalid/v1", nil)
			stream, err := StreamPiMessagesWithOptions(context.Background(), model, requestContext, &PiMessagesOptions{
				StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _ = ai.Collect(stream)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				return &http.Response{
					StatusCode: http.StatusUnauthorized,
					Status:     "401 Unauthorized",
					Header:     http.Header{"Content-Type": []string{"application/json"}},
					Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"custom client"}}`)),
					Request:    request,
				}, nil
			})}
			test.run(t, client)
			if got := calls.Load(); got != 1 {
				t.Fatalf("custom client calls = %d, want 1", got)
			}
		})
	}
}

func TestPerRequestHTTPClientOpenRouterImages(t *testing.T) {
	key := "test-key"
	zeroRetries := 0
	var calls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{
			StatusCode: http.StatusUnauthorized,
			Status:     "401 Unauthorized",
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"custom client"}}`)),
			Request:    request,
		}, nil
	})}
	_, err := GenerateOpenRouterImages(context.Background(), openRouterImagesTestModel(), ai.ImagesContext{
		Input: ai.ImagesContent{&ai.TextContent{Text: "draw"}},
	}, &ai.ImagesOptions{APIKey: &key, HTTPClient: client, MaxRetries: &zeroRetries})
	if err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("custom client calls = %d, want 1", got)
	}
}

func TestGoogleAdaptersRejectCustomHTTPClient(t *testing.T) {
	key := "test-key"
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("rejected Google client was called")
		return nil, nil
	})}
	requestContext := ai.Context{Messages: ai.MessageList{
		&ai.UserMessage{Content: ai.NewUserText("hello"), Timestamp: 1},
	}}
	for _, test := range []struct {
		name string
		api  ai.API
		run  func(*ai.Model) (ai.AssistantMessageEventStream, error)
		want string
	}{
		{
			name: "Generative AI", api: ai.APIGoogleGenerativeAI,
			run: func(model *ai.Model) (ai.AssistantMessageEventStream, error) {
				return StreamGoogleGenerativeAIWithOptions(context.Background(), model, requestContext, &GoogleOptions{
					StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client},
				})
			},
			want: "Custom HTTP client is not supported by the Google Generative AI adapter",
		},
		{
			name: "Vertex", api: ai.APIGoogleVertex,
			run: func(model *ai.Model) (ai.AssistantMessageEventStream, error) {
				return StreamGoogleVertexWithOptions(context.Background(), model, requestContext, &GoogleVertexOptions{
					StreamOptions: ai.StreamOptions{APIKey: &key, HTTPClient: client},
				})
			},
			want: "Custom HTTP client is not supported by the Google Vertex adapter",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := &ai.Model{ID: "fixture", API: test.api, Provider: "google", BaseURL: "https://ambient.invalid", MaxTokens: 128}
			stream, err := test.run(model)
			if err != nil {
				t.Fatal(err)
			}
			message, err := ai.Collect(stream)
			if err != nil {
				t.Fatal(err)
			}
			if message.ErrorMessage == nil || !strings.Contains(*message.ErrorMessage, test.want) {
				t.Fatalf("error message = %v, want %q", message.ErrorMessage, test.want)
			}
		})
	}
}
