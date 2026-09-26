package modes

import (
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/tui"
)

func TestTurnFooterClosesEveryTurn(t *testing.T) {
	prompt := func(text string) *ai.UserMessage {
		return &ai.UserMessage{Content: ai.NewUserText(text)}
	}
	reply := func(text string) *ai.AssistantMessage {
		message := assistantTextMessage(text)
		message.Model = "test-model"
		return message
	}
	mode := newPendingToolMode(t, []any{prompt("first"), reply("one"), prompt("second"), reply("two")})
	mode.renderInitialMessages()
	rendered := renderChatText(t, mode)
	if strings.Count(rendered, "test-model · 1s") != 2 || strings.Index(rendered, "test-model · 1s") > strings.Index(rendered, "second") {
		t.Fatalf("replayed turns lack their footers:\n%s", rendered)
	}

	// AgentStartEvent stamps the start; the fixture has no status bar to show.
	mode.turnStart = time.Now().Add(-72 * time.Second)
	mode.handleEvent(engine.MessageEndEvent{Message: reply("three")})
	mode.handleEvent(agent.AgentSettledEvent{})
	if rendered := renderChatText(t, mode); !strings.HasSuffix(strings.TrimSpace(rendered), "test-model · 1m 12s") {
		t.Fatalf("live turn lacks its footer:\n%s", rendered)
	}
}

func TestFormatTurnDuration(t *testing.T) {
	for elapsed, want := range map[time.Duration]string{
		0: "1s", 8400 * time.Millisecond: "8s", 72 * time.Second: "1m 12s", 3900 * time.Second: "1h 5m",
	} {
		if got := formatTurnDuration(elapsed); got != want {
			t.Errorf("formatTurnDuration(%s) = %q, want %q", elapsed, got, want)
		}
	}
}

// A failed compaction says why instead of leaving the transcript unchanged in silence.
func TestCompactionFailureIsShown(t *testing.T) {
	mode := newPendingToolMode(t, nil)
	mode.status = &tui.Container{}
	message := "Compaction failed: Nothing to compact (session too small)"
	mode.handleEvent(agent.CompactionEndEvent{Reason: "manual", ErrorMessage: &message})
	if rendered := renderChatText(t, mode); !strings.Contains(rendered, message) {
		t.Fatalf("compaction failure not shown:\n%s", rendered)
	}
}
