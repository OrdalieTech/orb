package codingagent

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/OrdalieTech/pi-go/ai"
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

func TestParseModelPatternUsesLocaleCompareForPartialWinner(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	models := []ai.Model{
		{Provider: "fixture", ID: "~a"},
		{Provider: "fixture", ID: "A-a"},
	}
	result := ParseModelPattern("a", models)
	if result.Model == nil || result.Model.ID != "A-a" {
		t.Fatalf("model = %#v, want fixture/A-a", result.Model)
	}
}

func TestParseModelPatternTrimsProviderAndModelComponents(t *testing.T) {
	models := []ai.Model{{Provider: "openai", ID: "gpt-4o"}}
	result := ParseModelPattern(" openai / gpt-4o ", models)
	if result.Model == nil || result.Model.Provider != "openai" || result.Model.ID != "gpt-4o" {
		t.Fatalf("model = %#v, want openai/gpt-4o", result.Model)
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
		{Type: "warning", Message: `Invalid thinking level "invalid" in pattern "gpt-4o:invalid". Using default instead.`, Pattern: "gpt-4o:invalid"},
		{Type: "warning", Message: `No models match pattern "missing"`, Pattern: "missing"},
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

func TestResolveModelScopeUsesMinimatchGlobstarSemantics(t *testing.T) {
	models := []ai.Model{{Provider: "openrouter", ID: "qwen/qwen3-coder"}, {Provider: "openrouter", ID: "flat"}}
	scoped, diagnostics := ResolveModelScope([]string{"openrouter/**"}, models)
	if len(diagnostics) != 0 || len(scoped) != 2 {
		t.Fatalf("globstar scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
	scoped, diagnostics = ResolveModelScope([]string{"openrouter/*"}, models)
	if len(diagnostics) != 0 || len(scoped) != 1 || scoped[0].Model.ID != "flat" {
		t.Fatalf("single-star scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
	scoped, diagnostics = ResolveModelScope([]string{"openrouter/**", "openrouter/qwen/**"}, models)
	if len(diagnostics) != 0 || len(scoped) != 2 {
		t.Fatalf("duplicate-only second glob scope = %#v, diagnostics = %#v", scoped, diagnostics)
	}
}

func TestModelGlobMatchUsesUpstreamBraceAndExtglobSemantics(t *testing.T) {
	tests := []struct {
		pattern, modelID string
		want             bool
	}{
		{pattern: "OPENAI/GPT-{4O,5}*", modelID: "openai/gpt-4o", want: true},
		{pattern: "{anthropic,openai}/**", modelID: "openai/gpt-5", want: true},
		{pattern: "@(anthropic|openai)/**", modelID: "openai/gpt-5", want: true},
		{pattern: "openrouter/@(qwen|openai)/**", modelID: "openrouter/qwen/x", want: true},
		{pattern: "openrouter/!(qwen)/**", modelID: "openrouter/openai/x", want: true},
		{pattern: "openrouter/!(qwen)/**", modelID: "openrouter/qwen/x", want: false},
		{pattern: "openrouter/?(qwen)/**", modelID: "openrouter/x", want: false},
		{pattern: "openrouter/?(qwen)/**", modelID: "openrouter/qwen/x", want: true},
		{pattern: "openrouter/*(qwen)/**", modelID: "openrouter/qwenqwen/x", want: true},
		{pattern: "openrouter/+(qwen)/**", modelID: "openrouter/qwenqwen/x", want: true},
		{pattern: "openrouter/{a..c}/**", modelID: "openrouter/B/x", want: true},
		{pattern: "openrouter/*", modelID: "openrouter/.hidden", want: false},
		{pattern: "openrouter/**", modelID: "openrouter/.hidden", want: false},
		{pattern: "**", modelID: ".hidden", want: false},
	}
	for _, test := range tests {
		t.Run(test.pattern+"_"+test.modelID, func(t *testing.T) {
			if got := modelGlobMatch(test.pattern, test.modelID); got != test.want {
				t.Fatalf("modelGlobMatch(%q, %q) = %t, want %t", test.pattern, test.modelID, got, test.want)
			}
		})
	}
}

func TestResolveModelScopeCombinesBraceExtglobNocaseAndThinking(t *testing.T) {
	models := []ai.Model{
		{Provider: "anthropic", ID: "claude/sonnet"},
		{Provider: "openrouter", ID: "qwen/coder"},
		{Provider: "openrouter", ID: "openai/gpt"},
		{Provider: "zai", ID: "qwen/coder"},
	}
	scoped, diagnostics := ResolveModelScope([]string{"{ANTHROPIC,OPENROUTER}/@(CLAUDE|QWEN)/**:high"}, models)
	if len(diagnostics) != 0 || len(scoped) != 2 {
		t.Fatalf("scoped models = %#v, diagnostics = %#v", scoped, diagnostics)
	}
	for _, model := range scoped {
		if model.ThinkingLevel == nil || *model.ThinkingLevel != ai.ModelThinkingHigh {
			t.Fatalf("thinking level = %v for %s/%s", model.ThinkingLevel, model.Model.Provider, model.Model.ID)
		}
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

func TestPreferredAvailableModelUsesPinnedProviderDefaults(t *testing.T) {
	models := []ai.Model{
		{Provider: "custom", ID: "first"},
		{Provider: "vercel-ai-gateway", ID: "zai/glm-5.1"},
		{Provider: "openai", ID: "gpt-5.5"},
	}
	preferred := PreferredAvailableModel(models)
	if preferred == nil || preferred.Provider != "openai" || preferred.ID != "gpt-5.5" {
		t.Fatalf("preferred model = %#v", preferred)
	}
	withoutDefaults := PreferredAvailableModel(models[:1])
	if withoutDefaults == nil || withoutDefaults.Provider != "custom" || withoutDefaults.ID != "first" {
		t.Fatalf("first fallback = %#v", withoutDefaults)
	}
	if PreferredAvailableModel(nil) != nil {
		t.Fatal("empty available list returned a model")
	}
	if selected := DefaultAvailableModel("openai", models); selected == nil || selected.ID != "gpt-5.5" {
		t.Fatalf("openai default = %#v", selected)
	}
	if selected := DefaultAvailableModel("openai", models[:2]); selected != nil {
		t.Fatalf("missing exact default selected %#v", selected)
	}
	if selected := DefaultAvailableModel("custom", models); selected != nil {
		t.Fatalf("unknown provider default selected %#v", selected)
	}
}

func TestQwenTokenPlanProviderDefaults(t *testing.T) {
	models := []ai.Model{
		{Provider: "qwen-token-plan", ID: "qwen3.6-plus"},
		{Provider: "qwen-token-plan", ID: "qwen3.7-max"},
		{Provider: "qwen-token-plan-cn", ID: "qwen3.7-max"},
	}
	for _, provider := range []string{"qwen-token-plan", "qwen-token-plan-cn"} {
		selected := DefaultAvailableModel(provider, models)
		if selected == nil || selected.ID != "qwen3.7-max" {
			t.Fatalf("%s default = %#v", provider, selected)
		}
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
