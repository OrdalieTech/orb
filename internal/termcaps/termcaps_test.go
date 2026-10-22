package termcaps

import "testing"

// A Herdr pane inside Ghostty gets links and Herdr's own Kitty graphics, not
// Ghostty's: Ghostty's variables leak into Herdr's panes.
func TestHerdrPaneHasLinksAndKittyImages(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "herdr")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/Applications/Ghostty.app/Contents/Resources/ghostty")
	if got := Detect(nil); got.Images != ImageProtocolKitty || !got.Hyperlinks {
		t.Fatalf("herdr pane capabilities = %+v", got)
	}
}
