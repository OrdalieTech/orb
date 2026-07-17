package truncate_test

import (
	"testing"

	"github.com/OrdalieTech/pi-go/conformance/runner"
)

func TestUpstreamF5Conformance(t *testing.T) {
	var fixture struct {
		Cases []jsonCase `json:"cases"`
	}
	runner.LoadJSON(t, "F5", "cases.json", &fixture)
	if len(fixture.Cases) == 0 {
		t.Fatal("F5 contains no cases")
	}
	t.Skip("WP-140: implement internal/truncate before enabling F5 comparisons")
}

type jsonCase struct {
	Name string `json:"name"`
}
