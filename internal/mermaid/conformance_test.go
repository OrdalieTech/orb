package mermaid

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/OrdalieTech/orb/conformance/runner"
)

// The engine goldens are raw grok-mermaid 0.2.2 output captured at pi
// v0.84.1 and frozen as an Orb-owned snapshot (D35); byte parity of the
// canonical JSON is the gate. conformance/runner/f12_mermaid_test.go also
// covers the transformer against the same file.

type conformanceFixture struct {
	SchemaVersion int               `json:"schemaVersion"`
	Engine        []conformanceCase `json:"engine"`
}

type conformanceCase struct {
	Name   string          `json:"name"`
	Source string          `json:"source"`
	Art    *conformanceArt `json:"art"`
}

type conformanceArt struct {
	Plain    []string            `json:"plain"`
	Styled   [][]conformanceSpan `json:"styled"`
	Width    int                 `json:"width"`
	Warnings []string            `json:"warnings"`
}

type conformanceSpan struct {
	Cls  string `json:"cls"`
	Text string `json:"text"`
}

func TestUpstreamEngineConformance(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture conformanceFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.SchemaVersion != 1 || len(fixture.Engine) != 50 {
		t.Fatalf("fixture header = version %d, engine %d", fixture.SchemaVersion, len(fixture.Engine))
	}

	for _, engineCase := range fixture.Engine {
		t.Run(engineCase.Name, func(t *testing.T) {
			art := Render(engineCase.Source)
			if engineCase.Art == nil {
				if art != nil {
					t.Fatalf("Render returned art (width %d), want nil", art.Width)
				}
				return
			}
			if art == nil {
				t.Fatal("Render returned nil, want art")
			}
			want, err := json.Marshal(engineCase.Art)
			if err != nil {
				t.Fatal(err)
			}
			got, err := json.Marshal(artFromRender(art))
			if err != nil {
				t.Fatal(err)
			}
			if diff := runner.ByteDiff(want, got); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

// artFromRender maps the engine output onto the fixture shape, normalizing
// nil slices to empty so the comparison is byte-exact JSON — the same
// normalization as the runner test.
func artFromRender(art *Art) *conformanceArt {
	styled := make([][]conformanceSpan, len(art.Styled))
	for rowIndex, row := range art.Styled {
		spans := make([]conformanceSpan, len(row))
		for spanIndex, span := range row {
			spans[spanIndex] = conformanceSpan(span)
		}
		styled[rowIndex] = spans
	}
	plain := art.Plain
	if plain == nil {
		plain = []string{}
	}
	warnings := art.Warnings
	if warnings == nil {
		warnings = []string{}
	}
	return &conformanceArt{Plain: plain, Styled: styled, Width: art.Width, Warnings: warnings}
}
