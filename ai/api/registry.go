package api

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/orb/ai"
)

// StreamSimpleFunc streams one request through a provider wire shape.
type StreamSimpleFunc func(context.Context, *ai.Model, ai.Context, *ai.SimpleStreamOptions) (ai.AssistantMessageEventStream, error)

// Provider binds one API family to its wire adapter. A family's adapter code
// links into a binary only when an assembly selects its Provider.
type Provider struct {
	API          ai.API
	StreamSimple StreamSimpleFunc
}

// Registry dispatches models to the API families an assembly selected.
// ai/api/all holds the registry with every family.
type Registry struct {
	providers map[ai.API]StreamSimpleFunc
}

// NewRegistry registers providers in order; a later Provider for the same API
// replaces an earlier one.
func NewRegistry(providers ...Provider) *Registry {
	registry := &Registry{providers: make(map[ai.API]StreamSimpleFunc, len(providers))}
	for _, provider := range providers {
		registry.providers[provider.API] = provider.StreamSimple
	}
	return registry
}

// Has reports whether api is registered.
func (registry *Registry) Has(api ai.API) bool {
	_, ok := registry.providers[api]
	return ok
}

// StreamSimple dispatches a model to its registered wire-shape adapter.
func (registry *Registry) StreamSimple(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (ai.AssistantMessageEventStream, error) {
	if model == nil {
		return nil, fmt.Errorf("ai: model is nil")
	}
	model, options = prepareCloudflareRequest(model, options)
	stream, ok := registry.providers[model.API]
	if !ok {
		return nil, fmt.Errorf("ai: unsupported API %q", model.API)
	}
	return stream(ctx, model, requestContext, options)
}

// CompleteSimple collects StreamSimple into its terminal assistant message.
func (registry *Registry) CompleteSimple(
	ctx context.Context,
	model *ai.Model,
	requestContext ai.Context,
	options *ai.SimpleStreamOptions,
) (*ai.AssistantMessage, error) {
	stream, err := registry.StreamSimple(ctx, model, requestContext, options)
	if err != nil {
		return nil, err
	}
	return ai.Collect(stream)
}

// Families whose adapters live in this package. Each constructor references
// only its own adapter, so unselected families are dropped by the linker.

func AnthropicMessages() Provider {
	return Provider{API: ai.APIAnthropicMessages, StreamSimple: StreamSimpleAnthropicMessages}
}

func AzureOpenAIResponses() Provider {
	return Provider{API: ai.APIAzureOpenAIResponses, StreamSimple: StreamSimpleAzureOpenAIResponses}
}

func GoogleGenerativeAI() Provider {
	return Provider{API: ai.APIGoogleGenerativeAI, StreamSimple: StreamSimpleGoogleGenerativeAI}
}

func GoogleVertex() Provider {
	return Provider{API: ai.APIGoogleVertex, StreamSimple: StreamSimpleGoogleVertex}
}

func MistralConversations() Provider {
	return Provider{API: ai.APIMistralConversations, StreamSimple: StreamSimpleMistralConversations}
}

func OpenAICodexResponses() Provider {
	return Provider{API: ai.APIOpenAICodexResponses, StreamSimple: StreamSimpleOpenAICodexResponses}
}

func OpenAICompletions() Provider {
	return Provider{API: ai.APIOpenAICompletions, StreamSimple: StreamSimpleOpenAICompletions}
}

func OpenAIResponses() Provider {
	return Provider{API: ai.APIOpenAIResponses, StreamSimple: StreamSimpleOpenAIResponses}
}

func PiMessages() Provider {
	return Provider{API: ai.APIPiMessages, StreamSimple: StreamSimplePiMessages}
}
