package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
)

func TestModelsJSONDocsExamplesMatchUpstreamFixture(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/WP250/docs-examples.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Heading string          `json:"heading"`
		Config  json.RawMessage `json:"config"`
		Models  []ai.Model      `json:"models"`
		Error   *string         `json:"error"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Heading, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, fixture.Config, 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadModelConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			models, err := ApplyModelConfig(nil, config)
			if err != nil {
				t.Fatal(err)
			}
			if !sameJSON(t, models, fixture.Models) {
				t.Fatalf("models = %#v, want %#v", models, fixture.Models)
			}
		})
	}
}

func TestModelsJSONOverlayAndNestedCompatMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	data := []byte(`{
		// comments and trailing commas are accepted by upstream
		"providers": {
			"fixture": {
				"baseUrl": "https://override.example/v1",
				"compat": {"supportsStore": false, "openRouterRouting": {"only": ["a"]}},
				"models": [{"id": "existing", "reasoning": true, "compat": {"openRouterRouting": {"order": ["b"]}}},],
				"modelOverrides": {"existing": {"maxTokens": 777, "cost": {"output": 9}}},
			},
		},
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	config, err := LoadModelConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	base := []ai.Model{{ID: "existing", Name: "Existing", API: ai.APIOpenAICompletions, Provider: "fixture", BaseURL: "https://base.example", Input: ai.InputModalities{ai.InputText}, Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1, Output: 2}}, ContextWindow: 100, MaxTokens: 50, Compat: json.RawMessage(`{"supportsDeveloperRole":true,"openRouterRouting":{"allow_fallbacks":false}}`)}}
	models, err := ApplyModelConfig(base, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].Name != "existing" || models[0].BaseURL != "https://override.example/v1" || !models[0].Reasoning || models[0].ContextWindow != 128000 || models[0].MaxTokens != 777 || models[0].Cost.Input != 0 || models[0].Cost.Output != 9 {
		t.Fatalf("bad overlay: %#v", models)
	}
	var compat map[string]any
	if err := json.Unmarshal(models[0].Compat, &compat); err != nil {
		t.Fatal(err)
	}
	routing := compat["openRouterRouting"].(map[string]any)
	if compat["supportsDeveloperRole"] != nil || compat["supportsStore"] != false || routing["allow_fallbacks"] != nil || routing["only"] == nil || routing["order"] == nil {
		t.Fatalf("nested compat merge = %#v", compat)
	}
}

func TestApplyModelConfigTreatsEmptyHeadersAsPresent(t *testing.T) {
	base := []ai.Model{{ID: "existing", Provider: "fixture", BaseURL: "https://example.invalid", API: ai.APIOpenAICompletions}}
	config := &ModelConfig{Providers: map[string]ModelProviderConfig{"fixture": {Headers: map[string]string{}}}}
	models, err := ApplyModelConfig(base, config)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "existing" {
		t.Fatalf("models = %#v", models)
	}
}

func TestResolveConfigValuesAndHeadersAtRequestTime(t *testing.T) {
	t.Setenv("WP250_PROCESS_ENV", "process")
	for _, test := range []struct{ input, want string }{
		{"$KEY", "value"},
		{"${KEY}_suffix", "value_suffix"},
		{"$WP250_PROCESS_ENV", "process"},
		{"$$literal-$!bang", "$literal-!bang"},
		{"plain", "plain"},
		{"${BAD-NAME}", "${BAD-NAME}"},
	} {
		got, err := ResolveConfigValue(context.Background(), test.input, map[string]string{"KEY": "value"})
		if err != nil || got != test.want {
			t.Errorf("ResolveConfigValue(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := ResolveConfigValue(context.Background(), "$MISSING", nil); err == nil {
		t.Fatal("missing environment variable resolved")
	}

	directory := t.TempDir()
	counter := filepath.Join(directory, "counter")
	command := "!value=$(cat " + counter + " 2>/dev/null || echo 0); value=$((value+1)); printf %s $value > " + counter + "; printf token-$value"
	authHeader := true
	config := &ModelConfig{Providers: map[string]ModelProviderConfig{"fixture": {
		APIKey:     &command,
		AuthHeader: &authHeader,
		Headers:    map[string]string{"X-Provider": "$PROVIDER", "X-Same": "provider"},
		Models:     []ModelDefinition{{ID: "m", Headers: map[string]string{"X-Model": "!printf model", "x-same": "model"}}},
	}}}
	first, err := config.ResolveAPIKey(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := config.ResolveAPIKey(context.Background(), "fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	if *first != "token-1" || *second != "token-2" {
		t.Fatalf("commands were not resolved uncached per request: %q, %q", *first, *second)
	}
	baseHeaders := map[string]string{"X-Builtin": "yes", "X-Same": "builtin"}
	headers, err := config.ResolveModelHeaders(context.Background(), ai.Model{Provider: "fixture", ID: "m", Headers: &baseHeaders}, map[string]string{"PROVIDER": "provider-value"}, first)
	if err != nil {
		t.Fatal(err)
	}
	if headers == nil || (*headers)["X-Builtin"] != "yes" || (*headers)["X-Provider"] != "provider-value" || (*headers)["X-Model"] != "model" || (*headers)["x-same"] != "model" || (*headers)["Authorization"] != "Bearer token-1" {
		t.Fatalf("resolved headers = %#v", headers)
	}
	if _, err := config.ResolveModelHeaders(context.Background(), ai.Model{Provider: "fixture", ID: "m"}, map[string]string{"PROVIDER": "provider-value"}); err == nil {
		t.Fatal("authHeader succeeded without an API key")
	}
}

func TestLoadModelsJSONRejectsInvalidSchema(t *testing.T) {
	for name, content := range map[string]string{
		"missing providers":        `{}`,
		"null provider":            `{"providers":{"p":null}}`,
		"empty id":                 `{"providers":{"p":{"baseUrl":"x","api":"openai-completions","models":[{"id":""}]}}}`,
		"invalid input":            `{"providers":{"p":{"models":[{"id":"m","input":["audio"]}]}}}`,
		"incomplete model cost":    `{"providers":{"p":{"models":[{"id":"m","cost":{"input":1}}]}}}`,
		"invalid thinking value":   `{"providers":{"p":{"models":[{"id":"m","thinkingLevelMap":{"off":3}}]}}}`,
		"invalid common compat":    `{"providers":{"p":{"compat":{"supportsLongCacheRetention":"yes"}}}}`,
		"invalid override headers": `{"providers":{"p":{"modelOverrides":{"m":{"headers":{"X-Test":1}}}}}}`,
		"radius oauth":             `{"providers":{"p":{"oauth":"radius"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadModelConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if config.Error() == "" || len(config.Providers) != 0 {
				t.Fatalf("invalid models.json did not become an empty error snapshot: %#v", config)
			}
		})
	}
}

func TestModelsJSONValidationMatchesUpstreamFixture(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/WP250/validation-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name     string          `json:"name"`
		Config   json.RawMessage `json:"config"`
		Accepted bool            `json:"accepted"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, fixture.Config, 0o600); err != nil {
				t.Fatal(err)
			}
			config, err := LoadModelConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if accepted := config.Error() == ""; accepted != fixture.Accepted {
				t.Fatalf("accepted = %t, want %t; error = %q", accepted, fixture.Accepted, config.Error())
			}
		})
	}
}

func TestModelsJSONValidationUsesProviderInputOrder(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
		reject  string
	}{
		{
			name:    "z before a",
			content: `{"providers":{"z-invalid":{"models":[{"id":"m","input":["audio"]}]},"a-invalid":{"models":[{"id":"m","cost":{"input":1}}]}}}`,
			want:    "providers.z-invalid.models.0.input.0",
			reject:  "providers.a-invalid",
		},
		{
			name:    "a before z",
			content: `{"providers":{"a-invalid":{"models":[{"id":"m","cost":{"input":1}}]},"z-invalid":{"models":[{"id":"m","input":["audio"]}]}}}`,
			want:    "providers.a-invalid.models.0.cost.output",
			reject:  "providers.z-invalid",
		},
		{
			name:    "semantic validation",
			content: `{"providers":{"z-invalid":{"oauth":"radius"},"a-invalid":{"oauth":"radius"}}}`,
			want:    "providers.z-invalid.oauth",
			reject:  "providers.a-invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "models.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			var first string
			for iteration := 0; iteration < 20; iteration++ {
				config, err := LoadModelConfig(path)
				if err != nil {
					t.Fatal(err)
				}
				message := config.Error()
				if !strings.Contains(message, test.want) || strings.Contains(message, test.reject) {
					t.Fatalf("error = %q; want first provider path %q", message, test.want)
				}
				if iteration == 0 {
					first = message
				} else if message != first {
					t.Fatalf("validation error changed between loads:\nfirst: %q\nnow:   %q", first, message)
				}
			}
		})
	}
}

func sameJSON(t *testing.T, left, right any) bool {
	t.Helper()
	leftJSON, err := json.Marshal(left)
	if err != nil {
		t.Fatal(err)
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		t.Fatal(err)
	}
	return string(leftJSON) == string(rightJSON)
}
