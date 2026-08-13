package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/codingagent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

// Repeated renders must be idempotent: the component previously mutated the
// band box's cached line slice, so OSC 133 zone markers accumulated one copy
// per frame on live terminals.
func TestUserMessageRenderIsIdempotent(t *testing.T) {
	initTestTheme(t)
	c := NewUserMessageComponent("hello", theme.MarkdownTheme(), 1, nil)
	first := strings.Join(c.Render(40), "\n")
	for range 2 {
		if again := strings.Join(c.Render(40), "\n"); again != first {
			t.Fatalf("render drifted between frames:\nfirst: %q\nagain: %q", first, again)
		}
	}
	if count := strings.Count(first, osc133ZoneStart); count != 1 {
		t.Fatalf("OSC zone start occurrences = %d, want 1", count)
	}
}

// The gap the reader actually sees is the one between blocks: a trailing blank
// on the user message doubled it against the assistant's leading spacer.
func TestOneBlankRowSeparatesAUserMessageFromTheReply(t *testing.T) {
	initTestTheme(t)
	thread := &tui.Container{}
	thread.AddChild(NewUserMessageComponent("hello", theme.MarkdownTheme(), 1, nil))
	thread.AddChild(NewAssistantMessageComponent(
		&ai.AssistantMessage{Content: ai.AssistantContent{&ai.TextContent{Text: "hi back"}}},
		true, theme.MarkdownTheme(), "Thinking...", 1, nil,
	))
	lines := thread.Render(40)
	user, reply := -1, -1
	for index, line := range lines {
		if strings.Contains(line, "hello") {
			user = index
		}
		if strings.Contains(line, "hi back") {
			reply = index
		}
	}
	if user < 0 || reply < 0 {
		t.Fatalf("thread did not render both blocks: %#v", lines)
	}
	if gap := reply - user - 1; gap != 1 {
		t.Fatalf("%d blank rows between the message and its reply, want 1: %#v", gap, lines[user:reply+1])
	}
}
