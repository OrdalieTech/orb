package theme

import (
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/tui"
)

// Fading blends every color toward the page and keeps the text; a palette
// without a known page stays faint across the line's resets.
func TestFade(t *testing.T) {
	registry := Load(LoadOptions{Mode: TrueColor})
	terminal, _ := registry.Get("terminal")
	line := "plain \x1b[38;2;200;100;0mhue\x1b[39m \x1b[1;48;2;0;0;0mdark\x1b[0m end"

	faint := terminal.Fade([]string{line}, .5)[0]
	if tui.StripANSI(faint) != tui.StripANSI(line) || strings.Count(faint, "\x1b[2m") != 2 {
		t.Fatalf("faint fallback = %q", faint)
	}

	terminal.SetTerminalBackground(tui.RgbColor{R: 100, G: 100, B: 100})
	faded := terminal.Fade([]string{line}, .5)[0]
	if tui.StripANSI(faded) != tui.StripANSI(line) {
		t.Fatalf("fade changed the text: %q", faded)
	}
	for _, want := range []string{"38;2;150;100;50", "48;2;50;50;50", "\x1b[0;38;2;"} {
		if !strings.Contains(faded, want) {
			t.Fatalf("faded line %q lacks %q", faded, want)
		}
	}
	if !strings.HasPrefix(faded, "\x1b[38;2;") || strings.Contains(faded, "\x1b[39m ") {
		t.Fatalf("default text kept full ink: %q", faded)
	}
}
