package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/exchange"
	"github.com/OrdalieTech/orb/tui"
)

func TestExchangeComponentRendersPeerAndSubagentRails(t *testing.T) {
	peerOut := NewExchangeComponent(exchange.Message{
		Dir:    exchange.DirOut,
		Scope:  exchange.ScopePeer,
		Who:    "ordalie-lab-3 · Deploy",
		Text:   "Is staging migration 042 green?",
		Status: "sent",
	}, 1, false)
	out := tui.StripANSI(strings.Join(peerOut.Render(72), "\n"))
	if !strings.Contains(out, "→ ordalie-lab-3 · Deploy") || !strings.Contains(out, "sent") || !strings.Contains(out, "Is staging migration 042 green?") {
		t.Fatalf("unexpected peer out render:\n%s", out)
	}

	sub := NewExchangeComponent(exchange.Message{
		Dir:    exchange.DirOut,
		Scope:  exchange.ScopeSubagent,
		Who:    "reviewer",
		Text:   "Audit protocol.Write pooling",
		Status: "2 steps",
		Reply:  "Pool is race-free.",
		Steps:  "read protocol.go\nrun go test -race",
	}, 0, false)
	collapsed := tui.StripANSI(strings.Join(sub.Render(72), "\n"))
	if !strings.Contains(collapsed, "→ reviewer") || !strings.Contains(collapsed, "2 steps") || !strings.Contains(collapsed, "← Pool is race-free.") {
		t.Fatalf("unexpected collapsed subagent render:\n%s", collapsed)
	}
	sub.expanded = true
	expanded := tui.StripANSI(strings.Join(sub.Render(72), "\n"))
	if !strings.Contains(expanded, "· read protocol.go") || !strings.Contains(expanded, "← reviewer") {
		t.Fatalf("unexpected expanded subagent render:\n%s", expanded)
	}
}
