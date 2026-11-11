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

// Alacritty leaves TERM_PROGRAM unset (pi #11767).
func TestAlacrittyHasLinksWithoutTermProgram(t *testing.T) {
	for _, env := range [][2]string{{"TERM", "alacritty"}, {"ALACRITTY_WINDOW_ID", "94371"}} {
		for _, name := range []string{"TMUX", "TERM_PROGRAM", "KITTY_WINDOW_ID", "GHOSTTY_RESOURCES_DIR", "WEZTERM_PANE", "WARP_SESSION_ID", "WARP_TERMINAL_SESSION_UUID", "ITERM_SESSION_ID", "WT_SESSION", "ALACRITTY_WINDOW_ID"} {
			t.Setenv(name, "")
		}
		t.Setenv("TERM", "xterm-256color")
		t.Setenv(env[0], env[1])
		if got := Detect(nil); !got.Hyperlinks || !got.TrueColor {
			t.Errorf("%s=%s capabilities = %+v, want hyperlinks", env[0], env[1], got)
		}
	}
}
