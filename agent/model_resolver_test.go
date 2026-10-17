package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

type patternFixture struct {
	Models []ai.Model `json:"models"`
	Cases  []struct {
		Pattern  string     `json:"pattern"`
		Models   []ai.Model `json:"models,omitempty"`
		Expected struct {
			Model *struct {
				Provider ai.ProviderID `json:"provider"`
				ID       string        `json:"id"`
			} `json:"model"`
			ThinkingLevel *ai.ModelThinkingLevel `json:"thinkingLevel"`
			Warning       *string                `json:"warning"`
		} `json:"expected"`
	} `json:"cases"`
	Scopes []struct {
		Patterns []string   `json:"patterns"`
		Models   []ai.Model `json:"models,omitempty"`
		Expected struct {
			Models []struct {
				Provider      ai.ProviderID          `json:"provider"`
				ID            string                 `json:"id"`
				ThinkingLevel *ai.ModelThinkingLevel `json:"thinkingLevel"`
			} `json:"models"`
			Diagnostics []ModelDiagnostic `json:"diagnostics"`
		} `json:"expected"`
	} `json:"scopes"`
}

func loadPatternFixture(t *testing.T) patternFixture {
	t.Helper()
	data, err := os.ReadFile("../conformance/fixtures/WP250/patterns.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture patternFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestParseModelPatternMatchesUpstreamFixture(t *testing.T) {
	fixture := loadPatternFixture(t)
	for _, test := range fixture.Cases {
		t.Run(test.Pattern, func(t *testing.T) {
			available := fixture.Models
			if test.Models != nil {
				available = test.Models
			}
			result := ParseModelPattern(test.Pattern, available)
			if test.Expected.Model == nil {
				if result.Model != nil {
					t.Fatalf("model = %s/%s, want nil", result.Model.Provider, result.Model.ID)
				}
			} else if result.Model == nil || result.Model.Provider != test.Expected.Model.Provider || result.Model.ID != test.Expected.Model.ID {
				t.Fatalf("model = %#v, want %#v", result.Model, test.Expected.Model)
			}
			if !equalThinkingLevel(result.ThinkingLevel, test.Expected.ThinkingLevel) {
				t.Fatalf("thinkingLevel = %v, want %v", result.ThinkingLevel, test.Expected.ThinkingLevel)
			}
			wantWarning := ""
			if test.Expected.Warning != nil {
				wantWarning = *test.Expected.Warning
			}
			if result.Warning != wantWarning {
				t.Fatalf("warning = %q, want %q", result.Warning, wantWarning)
			}
		})
	}
}

func equalThinkingLevel(left, right *ai.ModelThinkingLevel) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func TestResolveModelScopeMatchesUpstreamDiagnostics(t *testing.T) {
	fixture := loadPatternFixture(t)
	models, diagnostics := ResolveModelScope([]string{"sonnet:high", "gpt-4o:invalid", "missing"}, fixture.Models)
	if len(models) != 2 || models[0].Model.ID != "claude-sonnet-4-5" || models[1].Model.ID != "gpt-4o" {
		t.Fatalf("unexpected scoped models: %#v", models)
	}
	if models[0].ThinkingLevel == nil || *models[0].ThinkingLevel != ai.ModelThinkingHigh || models[1].ThinkingLevel != nil {
		t.Fatalf("unexpected scoped thinking levels: %#v", models)
	}
	want := []ModelDiagnostic{
		{Type: "warning", Code: "invalid-thinking-level", Message: `Invalid thinking level "invalid" in pattern "gpt-4o:invalid". Using default instead.`, Pattern: "gpt-4o:invalid"},
		{Type: "warning", Code: "no-match", Message: `No models match pattern "missing"`, Pattern: "missing"},
	}
	if string(mustJSON(t, diagnostics)) != string(mustJSON(t, want)) {
		t.Fatalf("diagnostics = %#v, want %#v", diagnostics, want)
	}
}

func TestResolveModelScopeMatchesUpstreamGlobFixtures(t *testing.T) {
	fixture := loadPatternFixture(t)
	for _, test := range fixture.Scopes {
		t.Run(test.Patterns[0], func(t *testing.T) {
			available := fixture.Models
			if test.Models != nil {
				available = test.Models
			}
			models, diagnostics := ResolveModelScope(test.Patterns, available)
			if len(models) != len(test.Expected.Models) {
				t.Fatalf("scoped models = %#v, want %#v", models, test.Expected.Models)
			}
			for index, expected := range test.Expected.Models {
				if models[index].Model.Provider != expected.Provider || models[index].Model.ID != expected.ID || !equalThinkingLevel(models[index].ThinkingLevel, expected.ThinkingLevel) {
					t.Fatalf("scoped model %d = %#v, want %#v", index, models[index], expected)
				}
			}
			if string(mustJSON(t, diagnostics)) != string(mustJSON(t, test.Expected.Diagnostics)) {
				t.Fatalf("diagnostics = %#v, want %#v", diagnostics, test.Expected.Diagnostics)
			}
		})
	}
}

func TestResolveCLIModelProviderPrefixAndCustomThinking(t *testing.T) {
	fixture := loadPatternFixture(t)

	resolved := ResolveCLIModel("", "openrouter/qwen", nil, fixture.Models)
	if resolved.Model == nil || resolved.Model.Provider != "openrouter" || resolved.Model.ID != "qwen/qwen3-coder:exacto" {
		t.Fatalf("provider-prefixed fuzzy model = %#v, error %q", resolved.Model, resolved.Error)
	}

	resolved = ResolveCLIModel("openrouter", "openrouter/openai/ghost-model", nil, fixture.Models)
	if resolved.Model == nil || resolved.Model.ID != "openai/ghost-model" {
		t.Fatalf("duplicated provider prefix was not stripped: %#v", resolved.Model)
	}

	resolved = ResolveCLIModel("openrouter", "new/model:high", nil, fixture.Models)
	if resolved.Model == nil || resolved.Model.ID != "new/model" || !resolved.Model.Reasoning || resolved.ThinkingLevel == nil || *resolved.ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("custom thinking fallback = %#v, thinking %v", resolved.Model, resolved.ThinkingLevel)
	}

	high := ai.ModelThinkingHigh
	resolved = ResolveCLIModel("openrouter", "new/model:high", &high, fixture.Models)
	if resolved.Model == nil || resolved.Model.ID != "new/model:high" || resolved.ThinkingLevel != nil {
		t.Fatalf("explicit thinking must preserve suffix in fallback id: %#v, thinking %v", resolved.Model, resolved.ThinkingLevel)
	}
	resolved = ResolveCLIModel("openrouter", "new/model", &high, fixture.Models)
	if resolved.Model == nil || !resolved.Model.Reasoning {
		t.Fatalf("explicit non-off thinking must enable custom-model reasoning: %#v", resolved.Model)
	}
	off := ai.ModelThinkingOff
	resolved = ResolveCLIModel("openai", "new-model", &off, fixture.Models)
	if resolved.Model == nil || resolved.Model.Reasoning {
		t.Fatalf("explicit off thinking must not enable fallback reasoning: %#v", resolved.Model)
	}
}

func TestResolveCLIModelPrefersAProviderWithCredentials(t *testing.T) {
	models := []ai.Model{{Provider: "openai", ID: "gpt-6-sol"}, {Provider: "openrouter", ID: "openai/gpt-6-sol"}, {Provider: "openai-codex", ID: "gpt-6-sol"}, {Provider: "vercel", ID: "openai/gpt-6-sol"}}
	signedIn := func(provider string) bool { return provider == "openai-codex" }
	for _, pattern := range []string{"gpt-6-sol", "gpt-6-sol:high", "6-sol"} {
		if resolved := ResolveCLIModel("", pattern, nil, models, signedIn); resolved.Model == nil || resolved.Model.Provider != "openai-codex" {
			t.Fatalf("%s resolved to %#v", pattern, resolved.Model)
		}
	}
	// None or several with credentials is ambiguous: catalog order would pick a billing route.
	for _, usable := range []func(string) bool{func(string) bool { return false }, func(p string) bool { return p != "openrouter" }} {
		if resolved := ResolveCLIModel("", "gpt-6-sol", nil, models, usable); resolved.Model != nil || !strings.Contains(resolved.Error, "openai-codex/gpt-6-sol, openai/gpt-6-sol") {
			t.Fatalf("ambiguous id resolved to %#v, error %q", resolved.Model, resolved.Error)
		}
	}
	// A provider-like prefix the provider cannot serve is a gateway's id, thinking suffix or not.
	gateway := func(p string) bool { return p == "openrouter" }
	if resolved := ResolveCLIModel("", "openai/gpt-6-sol:high", nil, models, gateway); resolved.Model == nil || resolved.Model.Provider != "openrouter" ||
		resolved.ThinkingLevel == nil || *resolved.ThinkingLevel != ai.ModelThinkingHigh {
		t.Fatalf("gateway id resolved to %#v", resolved)
	}
	if resolved := ResolveCLIModel("", "openai/gpt-6-sol", nil, models, func(p string) bool { return p == "openrouter" || p == "vercel" }); resolved.Model != nil || resolved.Error == "" {
		t.Fatalf("two gateways resolved to %#v", resolved.Model)
	}
	both := func(string) bool { return true }
	if resolved := ResolveCLIModel("", "model", nil, []ai.Model{{Provider: "a", ID: "Model"}, {Provider: "b", ID: "model"}}, both); resolved.Error == "" {
		t.Fatalf("ids differing in case resolved to %#v", resolved.Model)
	}
	// A scope adds each provider's model and warns, unless the pattern names the provider.
	scoped, diagnostics := ResolveModelScope([]string{"gpt-6-sol", "openai-codex/gpt-6-sol"}, models)
	if len(scoped) != 2 || len(diagnostics) != 1 || diagnostics[0].Code != "ambiguous" {
		t.Fatalf("scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
	if scoped, diagnostics = ResolveModelScope([]string{" openai-codex / gpt-6-sol "}, models); len(scoped) != 1 || len(diagnostics) != 0 {
		t.Fatalf("named scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
	if scoped, diagnostics = ResolveModelScope([]string{"m[1m]:high"}, []ai.Model{{Provider: "a", ID: "m[1m]"}, {Provider: "b", ID: "m[1m]"}}); len(scoped) != 2 || len(diagnostics) != 1 {
		t.Fatalf("bracketed scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
