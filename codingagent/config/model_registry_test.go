package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestModelRegistryHotReloadMatchesErrorSnapshotSemantics(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "models.json")
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"first"}]}}}`)
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "first"); !ok || registry.Error() != "" {
		t.Fatalf("initial registry missing model or has error: %q", registry.Error())
	}

	write(`{"providers":`)
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "first"); ok || !strings.Contains(registry.Error(), "Failed to parse models.json") {
		t.Fatalf("malformed reload did not publish builtin-only error snapshot: %q", registry.Error())
	}

	write(`{"providers":{"local":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"second"}]}}}`)
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("local", "second"); !ok || registry.Error() != "" {
		t.Fatalf("fixed config did not hot reload: %q", registry.Error())
	}
}

func TestModelRegistryKeepsOtherProvidersOnCompositionError(t *testing.T) {
	directory := t.TempDir()
	content := `{"providers":{"bad":{"models":[{"id":"broken"}]},"good":{"baseUrl":"http://localhost/v1","api":"openai-completions","apiKey":"dummy","models":[{"id":"working"}]}}}`
	if err := os.WriteFile(filepath.Join(directory, "models.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	registry, err := NewModelRegistry(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Find("good", "working"); !ok {
		t.Fatal("valid provider was dropped after a sibling composition error")
	}
	if _, ok := registry.Find("bad", "broken"); ok || !strings.Contains(registry.Error(), `Provider "bad"`) {
		t.Fatalf("bad provider result/error = %q", registry.Error())
	}
}

func TestModelRegistryMatchesUpstreamCompositionCases(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/WP250/composition-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name                       string          `json:"name"`
		Config                     json.RawMessage `json:"config"`
		GoodModels                 []ai.Model      `json:"goodModels"`
		BadModels                  []ai.Model      `json:"badModels"`
		NonpositiveModels          []ai.Model      `json:"nonpositiveModels"`
		EmptyModels                []ai.Model      `json:"emptyModels"`
		AnthropicProviderPreserved bool            `json:"anthropicProviderPreserved"`
		Error                      string          `json:"error"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			directory := t.TempDir()
			if err := os.WriteFile(filepath.Join(directory, "models.json"), fixture.Config, 0o600); err != nil {
				t.Fatal(err)
			}
			registry, err := NewModelRegistry(directory)
			if err != nil {
				t.Fatal(err)
			}
			modelsFor := func(provider string) []ai.Model {
				models := make([]ai.Model, 0)
				for _, model := range registry.Models() {
					if string(model.Provider) == provider {
						models = append(models, model)
					}
				}
				return models
			}
			if !sameJSON(t, modelsFor("good"), fixture.GoodModels) || !sameJSON(t, modelsFor("bad"), fixture.BadModels) ||
				!sameJSON(t, modelsFor("nonpositive"), fixture.NonpositiveModels) || !sameJSON(t, modelsFor("empty"), fixture.EmptyModels) {
				t.Fatalf("provider models differ: good=%#v bad=%#v nonpositive=%#v empty=%#v", modelsFor("good"), modelsFor("bad"), modelsFor("nonpositive"), modelsFor("empty"))
			}
			anthropicPreserved := len(modelsFor("anthropic")) > 0
			if anthropicPreserved != fixture.AnthropicProviderPreserved {
				t.Fatalf("anthropic preserved = %t, want %t", anthropicPreserved, fixture.AnthropicProviderPreserved)
			}
			if registry.Error() != fixture.Error {
				t.Fatalf("error = %q, want %q", registry.Error(), fixture.Error)
			}
		})
	}
}
