package tui

import (
	"context"
	"strings"
	"testing"
)

type safetyCompletionProvider struct {
	result CompletionResult
}

func (p *safetyCompletionProvider) GetSuggestions(context.Context, []string, int, int, bool) *AutocompleteSuggestions {
	return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "done", Label: "done"}}, Prefix: "draft"}
}

func (p *safetyCompletionProvider) ApplyCompletion(lines []string, _, _ int, _ AutocompleteItem, _ string) CompletionResult {
	// A provider may edit its input before returning an invalid result.
	lines[0] = "mutated"
	return p.result
}

func TestEditorCompletionResultSafety(t *testing.T) {
	cases := []struct {
		name   string
		result CompletionResult
		valid  bool
	}{
		{"nil-lines", CompletionResult{}, false},
		{"empty-lines", CompletionResult{Lines: []string{}}, false},
		{"negative-line", CompletionResult{Lines: []string{"replacement"}, CursorLine: -1}, false},
		{"line-at-length", CompletionResult{Lines: []string{"replacement"}, CursorLine: 1}, false},
		{"negative-column", CompletionResult{Lines: []string{"replacement"}, CursorCol: -1}, false},
		{"column-past-end", CompletionResult{Lines: []string{"replacement"}, CursorCol: 12}, false},
		{"unicode-byte-column", CompletionResult{Lines: []string{"é界"}, CursorCol: 5}, false},
		{"end-of-line", CompletionResult{Lines: []string{"done"}, CursorCol: 4}, true},
		{"unicode-end-of-line", CompletionResult{Lines: []string{"first", "é界"}, CursorLine: 1, CursorCol: 2}, true},
		{"empty-line", CompletionResult{Lines: []string{""}}, true},
	}
	for _, pathway := range []string{"popup-enter", "popup-tab", "forced-single"} {
		for _, tc := range cases {
			t.Run(pathway+"/"+tc.name, func(t *testing.T) {
				editor := newTestEditor()
				editor.SetText("first\ndraft")
				editor.SetAutocompleteProvider(&safetyCompletionProvider{result: tc.result})
				if pathway == "forced-single" {
					press(editor, "\t")
					editor.flushAutocomplete()
				} else {
					editor.tryTriggerAutocomplete()
					editor.flushAutocomplete()
					if !editor.IsShowingAutocomplete() {
						t.Fatal("expected completion popup")
					}
					key := "\r"
					if pathway == "popup-tab" {
						key = "\t"
					}
					press(editor, key)
				}
				text, line, col := "first\ndraft", 1, 5
				if tc.valid {
					text = strings.Join(tc.result.Lines, "\n")
					line, col = tc.result.CursorLine, tc.result.CursorCol
				}
				wantText(t, editor, text)
				wantCursor(t, editor, line, col)
				editor.SetAutocompleteProvider(nil)
				press(editor, "!")
				wantText(t, editor, text+"!")
				wantCursor(t, editor, line, col+1)
			})
		}
	}
}

type callbackSafetyProvider struct {
	safetyCompletionProvider
	apply func()
}

func (p *callbackSafetyProvider) ApplyCompletion([]string, int, int, AutocompleteItem, string) CompletionResult {
	p.apply()
	return p.result
}

func TestEditorCompletionFreshness(t *testing.T) {
	for _, path := range []string{"popup", "forced-single"} {
		for _, mode := range []string{"blocking", "reentrant"} {
			for _, change := range []string{"text", "cursor", "provider", "request"} {
				t.Run(path+"/"+mode+"/"+change, func(t *testing.T) {
					editor := newTestEditor()
					editor.SetText("draft")
					mutate := func() {
						switch change {
						case "text":
							editor.SetText("newer")
						case "cursor":
							editor.mu.Lock()
							editor.setCursorCol(2)
							editor.mu.Unlock()
						case "provider":
							editor.SetAutocompleteProvider(nil)
						case "request":
							editor.mu.Lock()
							editor.requestAutocomplete(false, false)
							editor.mu.Unlock()
						}
					}
					entered, release := make(chan struct{}), make(chan struct{})
					provider := &callbackSafetyProvider{safetyCompletionProvider: safetyCompletionProvider{result: CompletionResult{Lines: []string{"stale"}, CursorCol: 5}}}
					provider.apply = func() {
						if mode == "reentrant" {
							mutate()
							return
						}
						close(entered)
						<-release
					}
					editor.SetAutocompleteProvider(provider)
					if path == "popup" {
						editor.tryTriggerAutocomplete()
						editor.flushAutocomplete()
					}
					done := make(chan struct{})
					go func() {
						if path == "popup" {
							press(editor, "\r")
						} else {
							press(editor, "\t")
						}
						editor.flushAutocomplete()
						close(done)
					}()
					if mode == "blocking" {
						<-entered
						mutate()
						close(release)
					}
					<-done
					text, col := "draft", 5
					if change == "text" {
						text = "newer"
					}
					if change == "cursor" {
						col = 2
					}
					wantText(t, editor, text)
					wantCursor(t, editor, 0, col)
				})
			}
		}
	}
}

func TestEditorCompletionOwnsReturnedLines(t *testing.T) {
	for _, path := range []string{"popup", "forced-single"} {
		t.Run(path, func(t *testing.T) {
			editor := newTestEditor()
			editor.SetText("draft")
			lines := []string{"done"}
			editor.SetAutocompleteProvider(&safetyCompletionProvider{result: CompletionResult{Lines: lines, CursorCol: 4}})
			if path == "popup" {
				editor.tryTriggerAutocomplete()
				editor.flushAutocomplete()
				press(editor, "\r")
			} else {
				press(editor, "\t")
				editor.flushAutocomplete()
			}
			lines[0] = "corrupted"
			wantText(t, editor, "done")
		})
	}
}

func TestEditorRejectedSlashCompletionDoesNotSubmit(t *testing.T) {
	for _, stale := range []bool{false, true} {
		editor := newTestEditor()
		editor.SetText("/draft")
		want := "/draft"
		provider := &callbackSafetyProvider{apply: func() {}}
		if stale {
			want = "newer text"
			provider.result = CompletionResult{Lines: []string{"/done"}, CursorCol: 5}
			provider.apply = func() { editor.SetText(want) }
		}
		editor.SetAutocompleteProvider(provider)
		editor.mu.Lock()
		editor.applyAutocompleteSuggestions(&AutocompleteSuggestions{
			Prefix: "/draft", Items: []AutocompleteItem{{Value: "/done", Label: "/done"}},
		}, "regular")
		editor.mu.Unlock()
		submitted := false
		editor.OnSubmit = func(string) { submitted = true }
		press(editor, "\r")
		if submitted {
			t.Fatalf("rejected completion submitted text (stale=%t)", stale)
		}
		wantText(t, editor, want)
	}
}

func TestEditorCompletionPanicReacquiresLock(t *testing.T) {
	editor := newTestEditor()
	provider := &callbackSafetyProvider{apply: func() { panic("provider panic") }}
	editor.SetAutocompleteProvider(provider)
	func() {
		editor.mu.Lock()
		defer editor.mu.Unlock()
		defer func() {
			if got := recover(); got != "provider panic" {
				t.Errorf("panic = %v, want provider panic", got)
			}
			// Check before the caller's deferred unlock, avoiding a fatal
			// unlocked-mutex cleanup if this contract regresses.
			if editor.mu.TryLock() {
				t.Error("ApplyCompletion panic left editor unlocked")
			}
		}()
		editor.applyCompletionResult(SelectItem{Value: "done"})
	}()
	editor.SetText("still usable")
	wantText(t, editor, "still usable")
}
