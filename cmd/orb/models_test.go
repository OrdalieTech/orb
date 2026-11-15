package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
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
	// The empty-list guidance resolves doc pointers through
	// AuthGuidanceDocPaths, so the fixture's literal upstream paths are
	// substituted with the resolved ones.
	providersDoc, modelsDoc := agent.AuthGuidanceDocPaths()
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
