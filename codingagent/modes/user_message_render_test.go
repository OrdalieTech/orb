package modes

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/codingagent/modes/theme"
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
