package api

import (
	"context"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

// testProviders registers every family in this package. Bedrock has no SDK
// backend here (ai/api/bedrock imports this package), so tests that reach its
// transport supply a BedrockBackend of their own.
var testProviders = NewRegistry(
	BedrockConverse(BedrockBackend{}),
	AnthropicMessages(),
	GoogleGenerativeAI(),
	GoogleVertex(),
	MistralConversations(),
	AzureOpenAIResponses(),
	OpenAICodexResponses(),
	OpenAIResponses(),
	OpenAICompletions(),
	PiMessages(),
)

func TestRegistryRejectsUnselectedAPI(t *testing.T) {
	light := NewRegistry(OpenAICompletions(), AnthropicMessages())
	if !light.Has(ai.APIOpenAICompletions) || !light.Has(ai.APIAnthropicMessages) || light.Has(ai.APIBedrockConverse) {
		t.Fatal("light registry membership is wrong")
	}
	for _, api := range []ai.API{ai.APIBedrockConverse, ai.APIGoogleVertex, ai.APIOpenAIResponses, "not-an-api"} {
		stream, err := light.StreamSimple(context.Background(), &ai.Model{ID: "m", API: api}, ai.Context{}, nil)
		if stream != nil || err == nil || err.Error() != `ai: unsupported API "`+string(api)+`"` {
			t.Fatalf("%s: stream=%v err=%v", api, stream != nil, err)
		}
	}
	if _, err := light.StreamSimple(context.Background(), nil, ai.Context{}, nil); err == nil || err.Error() != "ai: model is nil" {
		t.Fatalf("nil model error = %v", err)
	}
}

func TestRegistryLaterProviderReplacesEarlier(t *testing.T) {
	called := false
	replacement := Provider{API: ai.APIOpenAICompletions, StreamSimple: func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error) {
		called = true
		return func(func(ai.AssistantMessageEvent, error) bool) {}, nil
	}}
	registry := NewRegistry(OpenAICompletions(), replacement)
	if _, err := registry.StreamSimple(context.Background(), &ai.Model{API: ai.APIOpenAICompletions}, ai.Context{}, nil); err != nil || !called {
		t.Fatalf("replacement not used: called=%t err=%v", called, err)
	}
}

func TestBedrockWithoutBackendFailsInStream(t *testing.T) {
	message, err := testProviders.CompleteSimple(context.Background(), &ai.Model{ID: "anthropic.claude-sonnet-4-5", API: ai.APIBedrockConverse, Provider: "amazon-bedrock"}, ai.Context{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != ai.StopReasonError || message.ErrorMessage == nil || *message.ErrorMessage != "ai/api: Bedrock ConverseStream has no transport backend (register ai/api/bedrock)" {
		t.Fatalf("message = %#v", message)
	}
}
