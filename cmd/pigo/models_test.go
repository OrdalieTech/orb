package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
)

func TestFormatModelListMatchesUpstreamFixture(t *testing.T) {
	data, err := os.ReadFile("../../conformance/fixtures/WP250/patterns.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Models []ai.Model `json:"models"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../../conformance/fixtures/WP250/list.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := formatModelList(fixture.Models, ""); got != string(want) {
		t.Fatalf("model list mismatch\n--- got ---\n%s--- want ---\n%s", got, want)
	}
	caseData, err := os.ReadFile("../../conformance/fixtures/WP250/list-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name, Search, Expected string
		Models                 []ai.Model
		Empty                  bool
	}
	if err := json.Unmarshal(caseData, &cases); err != nil {
		t.Fatal(err)
	}
	// LOG-m6: the empty-list guidance resolves doc pointers through
	// authGuidanceDocPaths like formatNoAPIKeyFoundMessage, so the fixture's
	// literal upstream paths are substituted with the resolved ones.
	providersDoc, modelsDoc := codingagent.AuthGuidanceDocPaths()
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			models := fixture.Models
			if test.Empty {
				models = nil
			} else if test.Models != nil {
				models = test.Models
			}
			expected := strings.Replace(test.Expected, "  docs/providers.md", "  "+providersDoc, 1)
			expected = strings.Replace(expected, "  docs/models.md", "  "+modelsDoc, 1)
			if got := formatModelList(models, test.Search); got != expected {
				t.Fatalf("filtered model list mismatch\n--- got ---\n%s--- want ---\n%s", got, expected)
			}
		})
	}
}

// LOG-m6: --list-models empty output shares upstream
// formatNoModelsAvailableMessage via the same doc-path resolution.
func TestLOGm6FormatModelListUsesAuthGuidance(t *testing.T) {
	if got, want := formatModelList(nil, ""), codingagent.FormatNoModelsAvailableMessage()+"\n"; got != want {
		t.Fatalf("empty model list = %q, want %q", got, want)
	}
}

func TestFormatModelListFilteringAndEmptyResults(t *testing.T) {
	models := []ai.Model{{Provider: "openai", ID: "gpt-4o", ContextWindow: 128000, MaxTokens: 4096, Input: ai.InputModalities{ai.InputText}}}
	if got := formatModelList(models, "g4"); got == "" || got == "No models matching \"g4\"\n" {
		t.Fatalf("fuzzy filter missed gpt-4o: %q", got)
	}
	if got := formatModelList(models, "claude"); got != "No models matching \"claude\"\n" {
		t.Fatalf("no-match output = %q", got)
	}
	// LOG-m6: empty output flows through codingagent.FormatNoModelsAvailableMessage.
	if got := formatModelList(nil, ""); got != codingagent.FormatNoModelsAvailableMessage()+"\n" {
		t.Fatalf("empty output = %q", got)
	}
}

func TestFuzzyModelMatchSwapsBothAlphaNumericOrders(t *testing.T) {
	for _, query := range []string{"g4", "4g"} {
		if !fuzzyModelMatch(query, "openai gpt-4o") {
			t.Fatalf("fuzzyModelMatch(%q) missed gpt-4o", query)
		}
	}
	if fuzzyModelMatch("4x", "openai gpt-4o") {
		t.Fatal("unrelated swapped query matched gpt-4o")
	}
}

func TestFormatModelListUsesLocaleCompareOrdering(t *testing.T) {
	t.Setenv("LC_ALL", "C.UTF-8")
	models := []ai.Model{
		{Provider: "Beta", ID: "model"},
		{Provider: "alpha", ID: "a-a"},
		{Provider: "alpha", ID: "A-a"},
		{Provider: "alpha", ID: "a_a"},
	}
	output := formatModelList(models, "")
	lines := strings.Split(strings.TrimSpace(output), "\n")[1:]
	ordered := make([]string, 0, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		ordered = append(ordered, fields[0]+"/"+fields[1])
	}
	want := []string{"alpha/a_a", "alpha/a-a", "alpha/A-a", "Beta/model"}
	if !slices.Equal(ordered, want) {
		t.Fatalf("model list order = %v, want %v:\n%s", ordered, want, output)
	}
}
