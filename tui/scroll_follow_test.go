package tui

import "testing"

// A transcript scrolled up for reading stays put while frames stream in, and
// ScrollToBottom is the explicit reattach the interactive editor calls on
// submit so a sent message is visible.
func TestScrollToBottomReattachesFollow(t *testing.T) {
	body := &mutableLines{lines: []string{"line 0", "line 1", "line 2", "line 3", "line 4", "line 5"}}
	ui := NewTUI(newFakeTerminal(20, 6))
	ui.SetViewport(body, &mutableLines{lines: []string{"editor"}})
	ui.previousLines = ui.renderViewport(20, 6)

	ui.renderMu.Lock()
	ui.scrollViewportLocked(-2)
	detachedEnd := ui.viewportEnd
	follow := ui.viewportFollow
	ui.renderMu.Unlock()
	if follow {
		t.Fatal("scrolling up left live follow attached")
	}

	// Streaming more lines must not drag the view back on its own.
	body.lines = append(body.lines, "line 6", "line 7")
	ui.previousLines = ui.renderViewport(20, 6)
	ui.renderMu.Lock()
	held := ui.viewportEnd == detachedEnd && !ui.viewportFollow
	ui.renderMu.Unlock()
	if !held {
		t.Fatal("appended lines moved a detached viewport")
	}

	ui.ScrollToBottom()

	ui.renderMu.Lock()
	defer ui.renderMu.Unlock()
	if !ui.viewportFollow {
		t.Fatal("ScrollToBottom did not reattach live follow")
	}
}
