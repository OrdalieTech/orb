package models

import "testing"

// The generated catalog is the single source of hand corrections
// (cataloggen applyCorrections); loading must not need a second copy.
func TestBuiltinCatalogCarriesGenerateTimeCorrections(t *testing.T) {
	catalog, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	for _, expect := range []struct {
		provider, id             string
		contextWindow, maxTokens float64
	}{
		{"openai", "gpt-5.4", 272000, 128000},
		{"openai", "gpt-5-pro", 400000, 128000},
		{"anthropic", "claude-opus-4-6", 1000000, 128000},
	} {
		model, ok := catalog.Find(expect.provider, expect.id)
		if !ok {
			t.Fatalf("missing %s/%s", expect.provider, expect.id)
		}
		if model.ContextWindow != expect.contextWindow || model.MaxTokens != expect.maxTokens {
			t.Fatalf("%s/%s = context %v / max %v, want %v/%v",
				expect.provider, expect.id, model.ContextWindow, model.MaxTokens, expect.contextWindow, expect.maxTokens)
		}
	}
}
