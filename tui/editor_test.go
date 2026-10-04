package tui

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestEditor() *Editor {
	return NewEditor(NewTUI(newFakeTerminal(80, 24)), EditorTheme{})
}

func press(target InputHandler, sequences ...string) {
	for _, sequence := range sequences {
		target.HandleInput(keyEventFor(sequence))
	}
}

func pressN(target InputHandler, count int, sequence string) {
	for range count {
		target.HandleInput(keyEventFor(sequence))
	}
}

func wantText(t *testing.T, editor *Editor, want string) {
	t.Helper()
	if got := editor.GetText(); got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}

func wantCursor(t *testing.T, editor *Editor, line, col int) {
	t.Helper()
	gotLine, gotCol := editor.GetCursor()
	if gotLine != line || gotCol != col {
		t.Fatalf("cursor = {%d %d}, want {%d %d}", gotLine, gotCol, line, col)
	}
}

// Ported from upstream editor.test.ts "Prompt history navigation".
func TestEditorHistoryNavigation(t *testing.T) {
	editor := newTestEditor()
	press(editor, "\x1b[A")
	wantText(t, editor, "")

	editor.AddToHistory("first")
	editor.AddToHistory("second")
	editor.AddToHistory("third")
	press(editor, "\x1b[A")
	wantText(t, editor, "third")
	press(editor, "\x1b[A")
	wantText(t, editor, "second")
	press(editor, "\x1b[A")
	wantText(t, editor, "first")
	press(editor, "\x1b[A")
	wantText(t, editor, "first")

	press(editor, "\x1b[B")
	wantText(t, editor, "second")
	press(editor, "\x1b[B")
	wantText(t, editor, "third")
}

func TestEditorHistoryDraftRestore(t *testing.T) {
	editor := newTestEditor()
	editor.AddToHistory("prompt")
	editor.SetText("draft")
	press(editor, "\x1b[D", "\x1b[D")

	press(editor, "\x1b[A") // jump to start before history browsing
	wantText(t, editor, "draft")
	wantCursor(t, editor, 0, 0)

	press(editor, "\x1b[A")
	wantText(t, editor, "prompt")

	press(editor, "\x1b[B") // restores draft
	wantText(t, editor, "draft")
	wantCursor(t, editor, 0, 0)
}

// Ported from "Backslash+Enter newline workaround".
func TestEditorBackslashEnter(t *testing.T) {
	editor := newTestEditor()
	press(editor, "a", "\\")
	wantText(t, editor, "a\\")
	press(editor, "\r")
	wantText(t, editor, "a\n")

	editor = newTestEditor()
	var submitted []string
	editor.OnSubmit = func(text string) { submitted = append(submitted, text) }
	press(editor, "a", "\\", "b", "\r")
	if len(submitted) != 1 || submitted[0] != "a\\b" {
		t.Fatalf("submitted = %q", submitted)
	}

	editor = newTestEditor()
	press(editor, "a", "\\", "\\", "\r")
	wantText(t, editor, "a\\\n")
}

// Ported from "Unicode text editing behavior".
func TestEditorUnicodeEditing(t *testing.T) {
	editor := newTestEditor()
	press(editor, "H", "e", "l", "l", "o", " ", "ä", "ö", "ü", " ", "😀")
	wantText(t, editor, "Hello äöü 😀")

	editor = newTestEditor()
	press(editor, "ä", "ö", "ü", "\x7f")
	wantText(t, editor, "äö")

	editor = newTestEditor()
	press(editor, "😀", "👍", "\x7f")
	wantText(t, editor, "😀")

	editor = newTestEditor()
	press(editor, "ä", "ö", "ü", "\x1b[D", "\x1b[D", "x")
	wantText(t, editor, "äxöü")

	editor = newTestEditor()
	press(editor, "😀", "👍", "🎉", "\x1b[D", "\x1b[D", "x")
	wantText(t, editor, "😀x👍🎉")

	editor = newTestEditor()
	press(editor, "ä", "ö", "ü", "\n")
	press(editor, "Ä", "Ö", "Ü")
	wantText(t, editor, "äöü\nÄÖÜ")

	editor = newTestEditor()
	editor.SetText("Hällö Wörld! 😀 äöüÄÖÜß")
	wantText(t, editor, "Hällö Wörld! 😀 äöüÄÖÜß")

	editor = newTestEditor()
	press(editor, "a", "b", "\x01", "x")
	wantText(t, editor, "xab")
}

func TestEditorWordNavigationKeys(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("foo bar... baz")

	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 11)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 7)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 7)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 10)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 14)

	editor.SetText("   foo bar")
	press(editor, "\x01", "\x1b[1;5C")
	wantCursor(t, editor, 0, 6)

	editor.SetText("foo.bar baz")
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 8)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 3)
	press(editor, "\x01", "\x1b[1;5C")
	wantCursor(t, editor, 0, 3)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 7)
}

// Ported from "Kill ring".
func TestEditorKillRing(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("foo bar baz")
	press(editor, "\x17")
	wantText(t, editor, "foo bar ")
	press(editor, "\x01", "\x19")
	wantText(t, editor, "bazfoo bar ")

	editor = newTestEditor()
	editor.SetText("hello world")
	press(editor, "\x01")
	pressN(editor, 6, "\x1b[C")
	press(editor, "\x15")
	wantText(t, editor, "world")
	press(editor, "\x19")
	wantText(t, editor, "hello world")

	editor = newTestEditor()
	editor.SetText("hello world")
	press(editor, "\x01", "\x0b")
	wantText(t, editor, "")
	press(editor, "\x19")
	wantText(t, editor, "hello world")

	editor = newTestEditor()
	editor.SetText("test")
	press(editor, "\x19")
	wantText(t, editor, "test")
}

// Ported from "Undo".
func TestEditorUndo(t *testing.T) {
	editor := newTestEditor()
	press(editor, "\x1f")
	wantText(t, editor, "")

	// Word coalescing: the space captures state before itself, so one undo
	// removes " world" and the next removes "hello".
	press(editor, "h", "e", "l", "l", "o", " ", "w", "o", "r", "l", "d")
	wantText(t, editor, "hello world")
	press(editor, "\x1f")
	wantText(t, editor, "hello")
	press(editor, "\x1f")
	wantText(t, editor, "")

	// Spaces undo one at a time.
	editor = newTestEditor()
	press(editor, "h", "e", "l", "l", "o", " ", " ")
	wantText(t, editor, "hello  ")
	press(editor, "\x1f")
	wantText(t, editor, "hello ")
	press(editor, "\x1f")
	wantText(t, editor, "hello")
	press(editor, "\x1f")
	wantText(t, editor, "")

	editor = newTestEditor()
	press(editor, "a", "b", "\x7f")
	wantText(t, editor, "a")
	press(editor, "\x1f")
	wantText(t, editor, "ab")

	editor = newTestEditor()
	editor.SetText("foo bar")
	press(editor, "\x17")
	wantText(t, editor, "foo ")
	press(editor, "\x1f")
	wantText(t, editor, "foo bar")

	editor = newTestEditor()
	editor.SetText("test")
	press(editor, "\x17", "\x19")
	wantText(t, editor, "test")
	press(editor, "\x1f")
	wantText(t, editor, "")

	// Paste is atomic.
	editor = newTestEditor()
	press(editor, "a")
	editor.HandleInput(keyEventFor("\x1b[200~pasted text\x1b[201~"))
	wantText(t, editor, "apasted text")
	press(editor, "\x1f")
	wantText(t, editor, "a")

	// InsertTextAtCursor is atomic.
	editor = newTestEditor()
	press(editor, "x")
	editor.InsertTextAtCursor("[image #1]")
	wantText(t, editor, "x[image #1]")
	press(editor, "\x1f")
	wantText(t, editor, "x")

	// setText to empty is undoable.
	editor = newTestEditor()
	editor.SetText("content")
	editor.SetText("")
	wantText(t, editor, "")
	press(editor, "\x1f")
	wantText(t, editor, "content")

	// Submit clears the undo stack.
	editor = newTestEditor()
	press(editor, "a", "b", "\r", "\x1f")
	wantText(t, editor, "")
}

// Ported from "Sticky column".
func TestEditorStickyColumn(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("2222222222x222\n\n1111111111_111111111111")
	wantCursor(t, editor, 2, 23)
	press(editor, "\x01")
	pressN(editor, 10, "\x1b[C")
	wantCursor(t, editor, 2, 10)

	press(editor, "\x1b[A")
	wantCursor(t, editor, 1, 0)
	press(editor, "\x1b[A")
	wantCursor(t, editor, 0, 10)

	editor = newTestEditor()
	editor.SetText("1111111111_111\n\n2222222222x222222222222")
	press(editor, "\x1b[A", "\x1b[A", "\x01")
	pressN(editor, 10, "\x1b[C")
	wantCursor(t, editor, 0, 10)
	press(editor, "\x1b[B")
	wantCursor(t, editor, 1, 0)
	press(editor, "\x1b[B")
	wantCursor(t, editor, 2, 10)

	// Reset on horizontal movement.
	editor = newTestEditor()
	editor.SetText("1234567890\n\n1234567890")
	press(editor, "\x01")
	pressN(editor, 5, "\x1b[C")
	wantCursor(t, editor, 2, 5)
	press(editor, "\x1b[A")
	wantCursor(t, editor, 1, 0)
	press(editor, "\x1b[D") // resets sticky
	wantCursor(t, editor, 0, 10)
}

// Ported from "Paste marker atomic behavior".
func pasteWithMarker(t *testing.T, editor *Editor) string {
	t.Helper()
	bigContent := strings.TrimRight(strings.Repeat("line\n", 20), "\n")
	editor.HandleInput(keyEventFor("\x1b[200~" + bigContent + "\x1b[201~"))
	return editor.GetText()
}

func taggedPaste(tag string) string {
	lines := make([]string, 12)
	for index := range lines {
		lines[index] = tag + string(rune('a'+index))
	}
	return strings.Join(lines, "\n")
}

func TestEditorPasteMarkers(t *testing.T) {
	editor := newTestEditor()
	text := pasteWithMarker(t, editor)
	if !pasteMarkerRegex.MatchString(text) {
		t.Fatalf("no marker in %q", text)
	}

	editor = newTestEditor()
	press(editor, "A")
	pasteWithMarker(t, editor)
	press(editor, "B")
	marker := pasteMarkerRegex.FindString(editor.GetText())

	press(editor, "\x01")
	wantCursor(t, editor, 0, 0)
	press(editor, "\x1b[C")
	wantCursor(t, editor, 0, 1)
	press(editor, "\x1b[C")
	wantCursor(t, editor, 0, 1+len(marker))
	press(editor, "\x1b[C")
	wantCursor(t, editor, 0, 1+len(marker)+1)

	press(editor, "\x1b[D")
	wantCursor(t, editor, 0, 1+len(marker))
	press(editor, "\x1b[D")
	wantCursor(t, editor, 0, 1)
	press(editor, "\x1b[D")
	wantCursor(t, editor, 0, 0)

	// Backspace deletes the whole marker.
	press(editor, "\x1b[C", "\x1b[C")
	press(editor, "\x7f")
	wantText(t, editor, "AB")
	wantCursor(t, editor, 0, 1)

	// Undo restores it.
	press(editor, "\x1f")
	wantText(t, editor, "A"+marker+"B")
}

func TestEditorImageMarkerIsOneEditingUnit(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("A[Image #12]B")
	press(editor, "\x01", "\x1b[C", "\x1b[C")
	wantCursor(t, editor, 0, len("A[Image #12]"))
	press(editor, "\x7f")
	wantText(t, editor, "AB")
	wantCursor(t, editor, 0, 1)
	press(editor, "\x1f")
	wantText(t, editor, "A[Image #12]B")
	press(editor, "\x1b[D")
	wantCursor(t, editor, 0, 1)
	press(editor, "\x1b[3~")
	wantText(t, editor, "AB")
	editor.SetText("A[Image #12]B")
	editor.Render(40)
	editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 10, Clicks: 1})
	wantCursor(t, editor, 0, len("A[Image #12]"))
	press(editor, "\x7f")
	wantText(t, editor, "AB")
}

func TestEditorPasteRegistryTracksMarkerDeletion(t *testing.T) {
	t.Run("undo deletion", func(t *testing.T) {
		editor := newTestEditor()
		paste := taggedPaste("alpha")
		editor.HandleInput(keyEventFor("\x1b[200~" + paste + "\x1b[201~"))
		press(editor, "\x7f", "\x1f")
		if got := editor.GetExpandedText(); got != paste {
			t.Fatalf("expanded after undo = %q, want %q", got, paste)
		}
	})

	t.Run("undo first of two", func(t *testing.T) {
		editor := newTestEditor()
		pasteA, pasteB := taggedPaste("alpha"), taggedPaste("beta")
		editor.HandleInput(keyEventFor("\x1b[200~" + pasteA + "\x1b[201~"))
		editor.HandleInput(keyEventFor("\x1b[200~" + pasteB + "\x1b[201~"))
		press(editor, "\x01", "\x1b[C", "\x7f", "\x1f")
		if got := editor.GetExpandedText(); got != pasteA+pasteB {
			t.Fatalf("expanded after undo = %q, want both pastes", got)
		}
	})

	t.Run("out of order markers", func(t *testing.T) {
		editor := newTestEditor()
		pasteA, pasteB, pasteC := taggedPaste("alpha"), taggedPaste("beta"), taggedPaste("gamma")
		editor.HandleInput(keyEventFor("\x1b[200~" + pasteA + "\x1b[201~"))
		press(editor, "\x01")
		editor.HandleInput(keyEventFor("\x1b[200~" + pasteB + "\x1b[201~"))
		press(editor, "\x01")
		editor.HandleInput(keyEventFor("\x1b[200~" + pasteC + "\x1b[201~"))
		press(editor, "\x05", "\x7f")
		if got := editor.GetExpandedText(); got != pasteC+pasteB {
			t.Fatalf("expanded after renumber = %q, want reordered pastes", got)
		}
	})

	t.Run("undo set text", func(t *testing.T) {
		editor := newTestEditor()
		paste := taggedPaste("alpha")
		editor.HandleInput(keyEventFor("\x1b[200~" + paste + "\x1b[201~"))
		editor.SetText("replacement")
		press(editor, "\x1f")
		if got := editor.GetExpandedText(); got != paste {
			t.Fatalf("expanded after undo = %q, want %q", got, paste)
		}
	})
}

func TestEditorPasteMarkerExpansion(t *testing.T) {
	editor := newTestEditor()
	bigContent := strings.TrimRight(strings.Repeat("line\n", 20), "\n")
	editor.HandleInput(keyEventFor("\x1b[200~" + bigContent + "\x1b[201~"))
	if got := editor.GetExpandedText(); got != bigContent {
		t.Fatalf("expanded = %q", got)
	}

	var submitted string
	editor.OnSubmit = func(text string) { submitted = text }
	press(editor, "\r")
	if submitted != bigContent {
		t.Fatalf("submitted = %q", submitted)
	}
}

func TestEditorPasteHookKeepsOrdinaryText(t *testing.T) {
	editor := newTestEditor()
	editor.OnPaste = func(text string) bool { return text == "image" }
	editor.HandleInput(keyEventFor("\x1b[200~image\x1b[201~"))
	wantText(t, editor, "")
	editor.HandleInput(keyEventFor("\x1b[200~ordinary text\x1b[201~"))
	wantText(t, editor, "ordinary text")
}

type scriptedProvider struct {
	suggest func(lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions
	calls   atomic.Int64
}

type slashScriptedProvider struct{ *scriptedProvider }

func (*slashScriptedProvider) TriggerCharacters() []string { return []string{"/"} }

type reentrantProvider struct {
	*scriptedProvider
	editor        *Editor
	gateCalled    atomic.Bool
	applyCalled   atomic.Bool
	triggerCalled atomic.Bool
}

func (provider *reentrantProvider) TriggerCharacters() []string {
	provider.triggerCalled.Store(true)
	_ = provider.editor.GetText()
	return []string{"%"}
}

func (provider *reentrantProvider) ShouldTriggerFileCompletion([]string, int, int) bool {
	provider.gateCalled.Store(true)
	_ = provider.editor.GetText()
	return true
}

func (provider *reentrantProvider) ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) CompletionResult {
	provider.applyCalled.Store(true)
	_ = provider.editor.GetText()
	return provider.scriptedProvider.ApplyCompletion(lines, cursorLine, cursorCol, item, prefix)
}

func (provider *scriptedProvider) GetSuggestions(_ context.Context, lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions {
	provider.calls.Add(1)
	return provider.suggest(lines, cursorLine, cursorCol, force)
}

// ApplyCompletion mirrors the upstream test helper: replace prefix with value.
func (provider *scriptedProvider) ApplyCompletion(lines []string, cursorLine, cursorCol int, item AutocompleteItem, prefix string) CompletionResult {
	line := ""
	if cursorLine < len(lines) {
		line = lines[cursorLine]
	}
	before := runeSlice(line, 0, cursorCol-runeLen(prefix))
	after := runeSliceFrom(line, cursorCol)
	newLines := append([]string(nil), lines...)
	newLines[cursorLine] = before + item.Value + after
	return CompletionResult{Lines: newLines, CursorLine: cursorLine, CursorCol: cursorCol - runeLen(prefix) + runeLen(item.Value)}
}

func TestEditorSkillBadgeCompletionRequiresSubmit(t *testing.T) {
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func([]string, int, int, bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "/skill:inspect", Label: "[skill] inspect"}}, Prefix: "@i"}
	}})
	var submitted string
	editor.OnSubmit = func(text string) { submitted = text }
	editor.SetText("@i")
	editor.tryTriggerAutocomplete()
	editor.flushAutocomplete()
	press(editor, "\r")
	if submitted != "" || editor.GetText() != "/skill:inspect" {
		t.Fatalf("completion submitted %q with editor text %q", submitted, editor.GetText())
	}
	press(editor, "\r")
	if submitted != "/skill:inspect" {
		t.Fatalf("submitted = %q", submitted)
	}
}

func TestEditorAutocompleteHooksMayReenter(t *testing.T) {
	editor := newTestEditor()
	provider := &reentrantProvider{editor: editor}
	provider.scriptedProvider = &scriptedProvider{suggest: func([]string, int, int, bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "done", Label: "done"}}, Prefix: "x"}
	}}
	editor.SetAutocompleteProvider(provider)
	press(editor, "x", "\t")
	editor.flushAutocomplete()
	wantText(t, editor, "done")
	if !provider.triggerCalled.Load() || !provider.gateCalled.Load() || !provider.applyCalled.Load() {
		t.Fatalf("hooks called: trigger=%v gate=%v apply=%v", provider.triggerCalled.Load(), provider.gateCalled.Load(), provider.applyCalled.Load())
	}
}

func TestEditorAutocompleteMenu(t *testing.T) {
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func(lines []string, _, cursorCol int, force bool) *AutocompleteSuggestions {
		if !force {
			return nil
		}
		prefix := runeSlice(lines[0], 0, cursorCol)
		if prefix == "src" {
			return &AutocompleteSuggestions{
				Items:  []AutocompleteItem{{Value: "src/", Label: "src/"}, {Value: "src.txt", Label: "src.txt"}},
				Prefix: "src",
			}
		}
		return nil
	}})

	press(editor, "s", "r", "c")
	press(editor, "\t")
	editor.flushAutocomplete()
	wantText(t, editor, "src")
	if !editor.IsShowingAutocomplete() {
		t.Fatal("menu should be showing")
	}
	press(editor, "\t")
	wantText(t, editor, "src/")
	if editor.IsShowingAutocomplete() {
		t.Fatal("menu should close after accept")
	}
}

func TestEditorAutocompleteDebounce(t *testing.T) {
	provider := &scriptedProvider{}
	provider.suggest = func(lines []string, _, cursorCol int, _ bool) *AutocompleteSuggestions {
		prefix := runeSlice(lines[0], 0, cursorCol)
		return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "@main.ts", Label: "main.ts"}}, Prefix: prefix}
	}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(provider)

	press(editor, "@", "m", "a", "i")
	if calls := provider.calls.Load(); calls != 0 {
		t.Fatalf("calls before debounce = %d", calls)
	}
	if editor.IsShowingAutocomplete() {
		t.Fatal("menu should not show before debounce")
	}
	time.Sleep(50 * time.Millisecond)
	editor.flushAutocomplete()
	if calls := provider.calls.Load(); calls != 1 {
		t.Fatalf("calls after debounce = %d", calls)
	}
	if !editor.IsShowingAutocomplete() {
		t.Fatal("menu should show after debounce")
	}
}

func TestEditorSlashCommandConfirmSubmits(t *testing.T) {
	commands := []SlashCommand{{Name: "help", Description: "Show help"}}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(NewCombinedAutocompleteProvider(commands, t.TempDir(), ""))
	var submitted []string
	editor.OnSubmit = func(text string) { submitted = append(submitted, text) }

	press(editor, "/", "h", "e")
	editor.flushAutocomplete()
	if !editor.IsShowingAutocomplete() {
		t.Fatal("slash menu should show")
	}
	press(editor, "\r")
	if len(submitted) != 1 || submitted[0] != "/help" {
		t.Fatalf("submitted = %q", submitted)
	}
	wantText(t, editor, "")
}

func TestEditorInlineSlashCompletionKeepsDraft(t *testing.T) {
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&slashScriptedProvider{&scriptedProvider{suggest: func([]string, int, int, bool) *AutocompleteSuggestions {
		return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "/skill:inspect", Label: "[skill] inspect"}}, Prefix: "/"}
	}}})
	submitted := false
	editor.OnSubmit = func(string) { submitted = true }
	press(editor, "a", " ", "/")
	editor.flushAutocomplete()
	if !editor.IsShowingAutocomplete() {
		t.Fatal("inline slash menu did not open")
	}
	press(editor, "\r")
	if submitted || editor.GetText() != "a /skill:inspect" {
		t.Fatalf("inline skill submitted instead of staying in the draft: %q, submitted=%v", editor.GetText(), submitted)
	}
}

func TestEditorStaleSlashAutocompleteDoesNotRewriteSubmit(t *testing.T) {
	block := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(block)
		}
	}()
	var calls atomic.Int64
	provider := &scriptedProvider{suggest: func([]string, int, int, bool) *AutocompleteSuggestions {
		if calls.Add(1) > 1 {
			<-block
		}
		return &AutocompleteSuggestions{
			Items:  []AutocompleteItem{{Value: "parallel-cleanup", Label: "parallel-cleanup"}},
			Prefix: "/",
		}
	}}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(provider)
	var submitted string
	editor.OnSubmit = func(text string) { submitted = text }

	press(editor, "/")
	editor.flushAutocomplete()
	press(editor, "p", "l", "u", "g", "i", "n", "s", " ", "\r")
	if submitted != "/plugins" {
		t.Fatalf("submitted = %q, want /plugins", submitted)
	}

	close(block)
	released = true
	editor.flushAutocomplete()
}

// flushAutocomplete blocks until no debounce timer or request is pending
// (the Go analog of upstream tests awaiting the request task).
func (editor *Editor) flushAutocomplete() {
	editor.mu.Lock()
	for editor.autocompleteBusy > 0 {
		editor.autocompleteIdle.Wait()
	}
	editor.mu.Unlock()
}

func TestEditorNativeSelectionEditing(t *testing.T) {
	editor := newTestEditor()
	editor.theme.Selection = func(s string) string { return "\x1b[48;2;45;48;52m" + s + "\x1b[49m" }
	copied := ""
	editor.ui.SetSelectionHandler(func(s string) { copied = s })
	editor.SetText("hello café 👩‍💻\nsecond paragraph")
	editor.Render(24)
	press(editor, "\x1b[1;10A") // Command+Shift+Up: select to document start.
	if got := editor.selectedText(); got != editor.GetText() {
		t.Fatalf("document selection = %q", got)
	}
	press(editor, "\x03")
	if copied != editor.GetText() {
		t.Fatalf("clipboard = %q", copied)
	}
	press(editor, "replacement")
	if editor.GetText() != "replacement" || editor.HasSelection() {
		t.Fatal("typing did not replace the selection")
	}
	press(editor, "\x1f")
	if editor.GetText() != "hello café 👩‍💻\nsecond paragraph" {
		t.Fatal("selection replacement was not one undo step")
	}
	press(editor, "\x1b[1;9B", "\x1b[1;6D") // Command+Down, then Ctrl+Shift+Left.
	if got := editor.selectedText(); got != "paragraph" {
		t.Fatalf("word selection = %q", got)
	}
	if !strings.Contains(strings.Join(editor.Render(24), ""), "\x1b[48;2;45;48;52m") {
		t.Fatal("selection did not use the adaptive style")
	}
	press(editor, "\x7f")
	if editor.GetText() != "hello café 👩‍💻\nsecond " {
		t.Fatalf("selection deletion = %q", editor.GetText())
	}
	editor.SetText("café 👩‍💻 hello")
	editor.Render(30)
	editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 2, Clicks: 2})
	editor.HandleMouse(MouseEvent{Type: MouseDrag, Row: 1, Column: 10})
	editor.HandleMouse(MouseEvent{Type: MouseRelease, Row: 1, Column: 10})
	if copied != "café 👩‍💻 hello" {
		t.Fatalf("word drag clipboard = %q", copied)
	}
	press(editor, "\x1b[D")
	if editor.HasSelection() || editor.state.cursorCol != 0 {
		t.Fatal("left did not collapse selection to its start")
	}
}
