package runner_test

import (
	"testing"

	"github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestF5CasesAwaitImplementation(t *testing.T) {
	var fixture struct {
		Cases []struct {
			Name string `json:"name"`
		} `json:"cases"`
	}
	runner.LoadJSON(t, "F5", "cases.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("F5 contains no cases")
	}
	for _, fixtureCase := range fixture.Cases {
		t.Run(fixtureCase.Name, func(t *testing.T) {
			t.Skip("WP-140: truncation implementation is intentionally pending")
		})
	}
}
