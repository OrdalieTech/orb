package models

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
)

// SYNC-2: this digest is JSON.stringify(Object.values(IMAGE_MODELS.openrouter))
// from upstream v0.81.0's generated image catalog. It catches additions,
// removals, ordering changes, and field-level drift that spot checks miss.
func TestSYNC2ImageCatalogMatchesUpstream(t *testing.T) {
	models := BuiltinImages(ai.ImagesProviderOpenRouter)
	if len(models) != 39 {
		t.Fatalf("image catalog has %d models, want 39", len(models))
	}
	b, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(b)
	const want = "40c176ec0df9d315d0f5920a3ed4550e084daa9e07533a838f5464e1135a3106"
	if got := hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("image catalog digest = %s, want %s", got, want)
	}
}

// SYNC-2: upstream HEAD adds the Krea 2 family and the beta auto router to the
// OpenRouter image catalog (image-models.generated.ts).
func TestSYNC2ImageCatalogAdditions(t *testing.T) {
	models := BuiltinImages(ai.ImagesProviderOpenRouter)
	byID := make(map[string]ai.ImagesModel, len(models))
	ids := make([]string, 0, len(models))
	for _, model := range models {
		byID[model.ID] = model
		ids = append(ids, model.ID)
	}
	if !slices.IsSorted(ids) {
		t.Fatalf("image catalog is not sorted by id: %v", ids)
	}
	for _, id := range []string{"krea/krea-2-large", "krea/krea-2-medium", "krea/krea-2-medium-turbo"} {
		model, ok := byID[id]
		if !ok {
			t.Fatalf("missing image model %s", id)
		}
		if len(model.Output) != 1 || model.Output[0] != ai.InputImage || model.Cost.Input != 0 {
			t.Fatalf("image model %s = %#v", id, model)
		}
	}
	beta, ok := byID["openrouter/auto-beta"]
	if !ok {
		t.Fatal("missing image model openrouter/auto-beta")
	}
	if beta.Name != "Auto Router (Beta)" || beta.Cost.Input != -1000000 || beta.Cost.Output != -1000000 {
		t.Fatalf("openrouter/auto-beta = %#v", beta)
	}
	if len(beta.Output) != 2 {
		t.Fatalf("openrouter/auto-beta output modalities = %#v", beta.Output)
	}
}
