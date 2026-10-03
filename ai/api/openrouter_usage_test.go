package api

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/OrdalieTech/orb/ai"
)

func TestOpenRouterReportedUsageCost(t *testing.T) {
	var fixture struct {
		Owner     string       `json:"owner"`
		ModelCost ai.ModelCost `json:"modelCost"`
		Cases     []struct {
			Name         string        `json:"name"`
			Provider     ai.ProviderID `json:"provider"`
			BaseURL      string        `json:"baseUrl"`
			Usage        string        `json:"usage"`
			ExpectedCost ai.Cost       `json:"expectedCost"`
		} `json:"cases"`
	}
	data, err := os.ReadFile("testdata/openrouter-usage.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Owner != "Orb" || len(fixture.Cases) != 8 {
		t.Fatalf("unexpected fixture owner/case count: %s/%d", fixture.Owner, len(fixture.Cases))
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			model := &ai.Model{ID: "fixture-model", API: ai.APIOpenAICompletions, Provider: test.Provider,
				BaseURL: "https://fixture.invalid/v1", Input: ai.InputModalities{ai.InputText}, Cost: fixture.ModelCost}
			if test.BaseURL != "" {
				model.BaseURL = test.BaseURL
			}
			usage := parseOpenAICompletionsUsage(json.RawMessage(test.Usage), model)
			if !reflect.DeepEqual(usage.Cost, test.ExpectedCost) {
				t.Fatalf("cost = %#v, want %#v", usage.Cost, test.ExpectedCost)
			}
			if usage.Input != 50 || usage.Output != 5 || usage.CacheRead != 40 || usage.CacheWrite != 10 || usage.TotalTokens != 105 || usage.Reasoning == nil || *usage.Reasoning != 2 {
				t.Fatalf("token accounting = %#v", usage)
			}
			if test.BaseURL != "" {
				return
			}
			for _, choiceUsage := range []bool{false, true} {
				chunk := `{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":` + test.Usage + `}`
				if choiceUsage {
					chunk = `{"choices":[{"delta":{},"finish_reason":"stop","usage":` + test.Usage + `}]}`
				}
				message, _ := collectOpenAICompletionsFixture(t, openAICompletionsFixtureStreamForModel(t, model, chunk))
				if !reflect.DeepEqual(message.Usage.Cost, test.ExpectedCost) {
					t.Fatalf("choice usage %v: cost = %#v, want %#v", choiceUsage, message.Usage.Cost, test.ExpectedCost)
				}
			}
		})
	}
}

func TestReportedCostIgnoresOpenRouterInAProxyPath(t *testing.T) {
	model := &ai.Model{Provider: "openai", BaseURL: "https://gateway.invalid/providers/openrouter.ai/other/v1",
		Cost: ai.ModelCost{ModelCostRates: ai.ModelCostRates{Input: 1, Output: 2}}}
	usage := parseOpenAICompletionsUsage(json.RawMessage(`{"prompt_tokens":100,"completion_tokens":10,"cost":123}`), model)
	expected := usage
	ai.CalculateCost(model, &expected)
	if usage.Cost.Total != expected.Cost.Total {
		t.Fatalf("total = %v, want catalog estimate %v", usage.Cost.Total, expected.Cost.Total)
	}
}
