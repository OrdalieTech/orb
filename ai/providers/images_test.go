package providers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestBuiltinImagesModelsMatchesPinnedOpenRouterCatalog(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "or-key")
	models := BuiltinImagesModels()
	registered := models.GetProviders()
	if len(registered) != 1 || registered[0].ID() != ai.ImagesProviderOpenRouter {
		t.Fatalf("providers = %#v", registered)
	}
	list := models.GetModels(ai.ImagesProviderOpenRouter)
	encoded, err := ai.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(encoded)); got != "24cba4f1ead2debd8e91f847f22aa882410c4c1e6cccd03a22d853174bfe0123" {
		t.Fatalf("catalog hash = %s (%d models)", got, len(list))
	}
	resolved, err := models.GetAuth(context.Background(), ai.ImagesProviderOpenRouter, nil)
	if err != nil || resolved == nil || resolved.Auth.APIKey == nil || *resolved.Auth.APIKey != "or-key" {
		t.Fatalf("OpenRouter auth = %#v, %v", resolved, err)
	}
}
