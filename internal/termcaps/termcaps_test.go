package termcaps

import "testing"

// A Herdr pane inside Ghostty gets links but no images: Herdr draws none, and
// Ghostty's variables leak into its panes.
func TestHerdrPaneHasLinksAndNoImages(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("TERM_PROGRAM", "herdr")
	t.Setenv("GHOSTTY_RESOURCES_DIR", "/Applications/Ghostty.app/Contents/Resources/ghostty")
	if got := Detect(nil); got.Images != "" || !got.Hyperlinks {
		t.Fatalf("herdr pane capabilities = %+v", got)
	}
}
