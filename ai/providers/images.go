package providers

import (
	"context"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/ai/api"
	"github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/ai/models"
)

func OpenRouterImages() ai.ImagesProvider {
	return ai.CreateImagesProvider(ai.CreateImagesProviderOptions{
		ID: ai.ImagesProviderOpenRouter, Name: "OpenRouter",
		Auth: auth.ProviderAuth{APIKey: auth.EnvAPIKeyAuth{
			DisplayName: "OpenRouter API key", EnvVars: []string{"OPENROUTER_API_KEY"},
		}},
		Models: models.BuiltinImages(ai.ImagesProviderOpenRouter),
		API: func(ctx context.Context, request ai.ImagesRequest) (*ai.AssistantImages, error) {
			return api.GenerateOpenRouterImages(ctx, request.Model, request.Context, request.Options)
		},
	})
}

func BuiltinImagesProviders() []ai.ImagesProvider {
	return []ai.ImagesProvider{OpenRouterImages()}
}

func BuiltinImagesModels(options ...ai.ImagesModelsOptions) ai.MutableImagesModels {
	result := ai.CreateImagesModels(options...)
	for _, provider := range BuiltinImagesProviders() {
		result.SetProvider(provider)
	}
	return result
}
