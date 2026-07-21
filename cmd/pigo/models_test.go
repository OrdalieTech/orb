package main

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/OrdalieTech/pigo/ai"
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
	for _, test := range cases {
		t.Run(test.Name, func(t *testing.T) {
			models := fixture.Models
			if test.Empty {
				models = nil
			} else if test.Models != nil {
				models = test.Models
			}
			if got := formatModelList(models, test.Search); got != test.Expected {
				t.Fatalf("filtered model list mismatch\n--- got ---\n%s--- want ---\n%s", got, test.Expected)
			}
		})
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
	if got := formatModelList(nil, ""); got != "No models available. Use /login to log into a provider via OAuth or API key. See:\n  docs/providers.md\n  docs/models.md\n" {
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
