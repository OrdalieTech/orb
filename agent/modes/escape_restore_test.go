package modes

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/tui"
)

func newRestoreMode(t *testing.T) *InteractiveMode {
	t.Helper()
	mode, _, _, _ := newF12ShutdownMode(t)
	mode.keybindings = NewAppKeybindings(nil)
	mode.editor = NewCustomEditor(mode.ui, theme.EditorTheme(), mode.keybindings)
	mode.chat = &tui.Container{}
	return mode
}

// Upstream restores queued messages to the editor when escape aborts a run
// (interactive-mode.ts restoreQueuedMessagesToEditor), keeping the current
// draft after them.
func TestAbortRestoresQueuedMessagesAfterCurrentDraft(t *testing.T) {
	mode := newRestoreMode(t)
	mode.setActiveEditorText("draft in progress")

	mode.restoreToEditor(nil, []string{"queued one", "queued two"})

	got := mode.activeEditorText(false)
	want := "queued one\n\nqueued two\n\ndraft in progress"
	if got != want {
		t.Fatalf("editor text = %q, want %q", got, want)
	}
}

// A turn that never showed anything gives its prompt back ahead of everything
// queued behind it, so the editor reads in the order the user wrote it.
func TestUnsentPromptLeadsRestoredText(t *testing.T) {
	mode := newRestoreMode(t)
	mode.setActiveEditorText("half-typed follow-up")

	prompt := "the prompt nobody answered"
	mode.restoreToEditor(&prompt, []string{"queued while waiting"})

	got := mode.activeEditorText(false)
	want := "the prompt nobody answered\n\nqueued while waiting\n\nhalf-typed follow-up"
	if got != want {
		t.Fatalf("editor text = %q, want %q", got, want)
	}
}

func TestRestoreToEditorKeepsDraftWhenNothingWasPending(t *testing.T) {
	mode := newRestoreMode(t)
	mode.setActiveEditorText("untouched draft")

	mode.restoreToEditor(nil, nil)

	if got := mode.activeEditorText(false); got != "untouched draft" {
		t.Fatalf("editor text = %q, want the draft untouched", got)
	}
}

// pendingPromptEntryID reports the trailing user message only while the
// assistant has committed nothing after it.
func TestPendingPromptEntryIDOnlyWhileUnanswered(t *testing.T) {
	mode := newRestoreMode(t)
	manager := mode.session.Manager()
	// The shared harness pre-writes a stub session file; appends here flush for real.
	if err := os.Remove(manager.GetSessionFile()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	if id := mode.pendingPromptEntryID(); id != "" {
		t.Fatalf("empty session reported pending prompt %q", id)
	}

	userID, err := manager.AppendMessage(json.RawMessage(`{"role":"user","content":[{"type":"text","text":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := mode.pendingPromptEntryID(); got != userID {
		t.Fatalf("pending prompt = %q, want the trailing user message %q", got, userID)
	}

	if _, err := manager.AppendMessage(json.RawMessage(`{"role":"assistant","content":[{"type":"text","text":"hi"}]}`)); err != nil {
		t.Fatal(err)
	}
	if got := mode.pendingPromptEntryID(); got != "" {
		t.Fatalf("pending prompt = %q, want none once the assistant answered", got)
	}
}

// Editor round trips go through the expanded text: SetText drops the paste
// registry, so a marker put back would stay literal (pi #10146).
func TestLargePasteSurvivesEditorRoundTrips(t *testing.T) {
	paste := strings.TrimSpace(strings.Repeat("pasted line\n", 20))
	for name, roundTrip := range map[string]func(*InteractiveMode){
		"restore queued": func(mode *InteractiveMode) { mode.restoreToEditor(nil, []string{"queued"}) },
		"switch editor":  func(mode *InteractiveMode) { mode.installEditorFactory(nil) },
		"inline custom": func(mode *InteractiveMode) {
			ui := NewInteractiveUI(mode)
			outcome := f12UILifecycleRunCustom(t.Context(), ui, func(_ extensions.UIHost, _ extensions.Theme, _ extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
				go done(nil)
				return &tui.Text{}, nil
			}, nil)
			f12UILifecycleReceive(t, outcome, "inline custom result")
		},
	} {
		t.Run(name, func(t *testing.T) {
			mode := newRestoreMode(t)
			mode.editorContainer = &tui.Container{}
			mode.editor.HandleInput(tui.KeyEvent{Raw: "\x1b[200~" + paste + "\x1b[201~"})
			roundTrip(mode)
			if got := mode.editor.GetExpandedText(); !strings.Contains(got, paste) {
				t.Fatalf("editor text = %q, want the pasted content", got)
			}
		})
	}
}

func TestFollowUpSubmitsExpandedTextWithImages(t *testing.T) {
	mode := newRestoreMode(t)
	mode.editorContainer = &tui.Container{}
	mode.setupKeyHandlers()
	paste := strings.TrimSpace(strings.Repeat("pasted line\n", 20))
	image := &ai.ImageContent{Data: "aGk=", MimeType: "image/png"}
	mode.pendingImages = []*ai.ImageContent{image}
	mode.editor.SetText("see [Image #1] ")
	mode.editor.HandleInput(tui.KeyEvent{Raw: "\x1b[200~" + paste + "\x1b[201~"})
	mode.editor.actionHandlers["app.message.followUp"]()
	entry := <-mode.inputCh
	if entry.text != "see [Image #1] "+paste || !entry.followUp || len(entry.images) != 1 || entry.images[0] != image {
		t.Fatalf("follow-up = %+v, want the expanded text with its image", entry)
	}
}
