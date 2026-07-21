package jsbridge

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/OrdalieTech/pigo/codingagent/extensions"
)

// TestBridgeCallBudget guards the M4 criterion: VM bridge calls stay under
// 8 ms on the corpus shapes (command dispatch plus a ctx.ui round-trip).
func TestBridgeCallBudget(t *testing.T) {
	project := t.TempDir()
	source := `
export default function (pi) {
  pi.registerCommand("ping", { handler: async (_args, ctx) => {
    ctx.ui.setStatus("bench", "ok");
  }});
}
`
	ui := newScriptedUI()
	runner := loadBridgeRunner(t, project, []bridgeSource{{"bench.ts", source}}, extensions.RunnerOptions{UI: ui, Mode: extensions.ModeTUI})
	command := runner.Command("ping")
	if command == nil {
		t.Fatal("bench command missing")
	}
	commandContext := runner.CreateCommandContext()
	const warmup, runs = 20, 200
	for range warmup {
		if err := command.Handler(context.Background(), "", commandContext); err != nil {
			t.Fatal(err)
		}
	}
	durations := make([]time.Duration, 0, runs)
	for range runs {
		start := time.Now()
		if err := command.Handler(context.Background(), "", commandContext); err != nil {
			t.Fatal(err)
		}
		durations = append(durations, time.Since(start))
	}
	sort.Slice(durations, func(a, b int) bool { return durations[a] < durations[b] })
	median := durations[runs/2]
	p90 := durations[runs*9/10]
	t.Logf("bridge call latency: median %s, p90 %s over %d runs", median, p90, runs)
	if p90 >= 8*time.Millisecond {
		t.Fatalf("bridge call p90 %s exceeds the 8 ms budget", p90)
	}
}
