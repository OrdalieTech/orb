package runner_test

import (
	"sort"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/conformance/runner"
	"github.com/OrdalieTech/pigo/tui"
)

type f12FullScreenFixture struct {
	SchemaVersion int                        `json:"schemaVersion"`
	Cases         []f12FullScreenFixtureCase `json:"cases"`
}

type f12FullScreenFixtureCase struct {
	Name      string   `json:"name"`
	Width     int      `json:"width"`
	Rows      int      `json:"rows"`
	User      string   `json:"user"`
	Assistant string   `json:"assistant"`
	Editor    string   `json:"editor"`
	Status    string   `json:"status"`
	Expected  []string `json:"expected"`
}

var f12ReplaySink int

func loadF12FullScreenFixture(t testing.TB) f12FullScreenFixture {
	t.Helper()
	var fixture f12FullScreenFixture
	runner.LoadJSON(t, "F12", "full-screen.json", &fixture)
	if fixture.SchemaVersion != 1 || len(fixture.Cases) != 4 {
		t.Fatalf("F12 full-screen header = version %d, cases %d", fixture.SchemaVersion, len(fixture.Cases))
	}
	return fixture
}

func renderF12FullScreen(fixtureCase f12FullScreenFixtureCase) []string {
	terminal := &f12Terminal{columns: fixtureCase.Width, rows: fixtureCase.Rows}
	uiInstance := tui.NewTUI(terminal)
	root := &tui.Container{}
	root.AddChild(tui.NewText(fixtureCase.User, 1, 0, f12Style("blue-bg")))
	root.AddChild(tui.NewSpacer(1))
	root.AddChild(tui.NewMarkdown(fixtureCase.Assistant, 1, 0, f12MarkdownTheme(), nil, nil))
	root.AddChild(tui.NewSpacer(1))
	editor := tui.NewEditor(uiInstance, f12EditorTheme)
	editor.SetText(fixtureCase.Editor)
	root.AddChild(editor)
	root.AddChild(tui.NewTruncatedText(fixtureCase.Status, 1, 0))
	return root.Render(fixtureCase.Width)
}

func TestF12FullScreenCompositesMatchUpstream(t *testing.T) {
	fixture := loadF12FullScreenFixture(t)
	wantWidths := []int{100, 72, 48, 32}
	for index, fixtureCase := range fixture.Cases {
		if fixtureCase.Width != wantWidths[index] {
			t.Fatalf("case %d width = %d, want %d", index, fixtureCase.Width, wantWidths[index])
		}
		t.Run(fixtureCase.Name, func(t *testing.T) {
			got := renderF12FullScreen(fixtureCase)
			if diff := linesDiff(fixtureCase.Expected, got); diff != "" {
				t.Fatal(diff)
			}
			for lineIndex, line := range got {
				if width := tui.VisibleWidth(line); width > fixtureCase.Width {
					t.Fatalf("line %d width = %d, terminal width %d", lineIndex, width, fixtureCase.Width)
				}
			}
		})
	}
}

func TestF12ReplayCorpusFrameBudget(t *testing.T) {
	fixture := loadF12FullScreenFixture(t)
	const (
		batches        = 11
		passesPerBatch = 6
		frameBudget    = 16 * time.Millisecond
	)

	for _, fixtureCase := range fixture.Cases {
		f12ReplaySink += len(renderF12FullScreen(fixtureCase))
	}

	averages := make([]time.Duration, batches)
	for batch := range averages {
		started := time.Now()
		for range passesPerBatch {
			for _, fixtureCase := range fixture.Cases {
				f12ReplaySink += len(renderF12FullScreen(fixtureCase))
			}
		}
		averages[batch] = time.Since(started) / time.Duration(passesPerBatch*len(fixture.Cases))
	}
	sort.Slice(averages, func(left, right int) bool { return averages[left] < averages[right] })
	median := averages[len(averages)/2]
	p90 := averages[len(averages)*9/10]
	t.Logf("F12 replay corpus: median %s/frame, p90 %s/frame, range %s..%s over %d timed frames",
		median, p90, averages[0], averages[len(averages)-1], batches*passesPerBatch*len(fixture.Cases))
	if p90 >= frameBudget {
		t.Fatalf("F12 replay corpus p90 = %s/frame, budget < %s/frame", p90, frameBudget)
	}
}

func BenchmarkF12ReplayCorpus(b *testing.B) {
	fixture := loadF12FullScreenFixture(b)
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		fixtureCase := fixture.Cases[iteration%len(fixture.Cases)]
		f12ReplaySink += len(renderF12FullScreen(fixtureCase))
	}
}
