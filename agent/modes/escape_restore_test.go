package modes

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/OrdalieTech/orb/agent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

func newRestoreMode(t *testing.T) *InteractiveMode {
	t.Helper()
	mode, _, _, _ := newF12ShutdownMode(t)
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

func TestPluralMessages(t *testing.T) {
	if got := pluralMessages(1); got != "1 queued message" {
		t.Fatalf("pluralMessages(1) = %q", got)
	}
	if got := pluralMessages(3); !strings.Contains(got, "3 queued messages") {
		t.Fatalf("pluralMessages(3) = %q", got)
	}
}
