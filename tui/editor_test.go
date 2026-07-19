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

func TestEditorHistoryRules(t *testing.T) {
	editor := newTestEditor()
	editor.AddToHistory("   ")
	press(editor, "\x1b[A")
	wantText(t, editor, "")

	editor.AddToHistory("same")
	editor.AddToHistory("same")
	editor.AddToHistory("other")
	editor.AddToHistory("same")
	press(editor, "\x1b[A")
	wantText(t, editor, "same")
	press(editor, "\x1b[A")
	wantText(t, editor, "other")
	press(editor, "\x1b[A")
	wantText(t, editor, "same")
	press(editor, "\x1b[A")
	wantText(t, editor, "same") // oldest
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

func TestEditorNewlineKeyMatrix(t *testing.T) {
	for name, sequence := range map[string]string{
		"shift-enter-kitty": "\x1b[13;2u",
		"shift-enter-xterm": "\x1b[13;2~",
		"ctrl-j":            "\n",
		"alt-enter":         "\x1b\r",
	} {
		t.Run(name, func(t *testing.T) {
			editor := newTestEditor()
			press(editor, "a", sequence, "b")
			wantText(t, editor, "a\nb")
		})
	}
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

func TestEditorDeleteWordMatrix(t *testing.T) {
	editor := newTestEditor()

	editor.SetText("foo bar baz")
	press(editor, "\x17")
	wantText(t, editor, "foo bar ")

	editor.SetText("foo bar   ")
	press(editor, "\x17")
	wantText(t, editor, "foo ")

	editor.SetText("foo bar...")
	press(editor, "\x17")
	wantText(t, editor, "foo bar")

	editor.SetText("foo.bar")
	press(editor, "\x17")
	wantText(t, editor, "foo.")

	editor.SetText("foo:bar")
	press(editor, "\x17")
	wantText(t, editor, "foo:")

	editor.SetText("line one\nline two")
	press(editor, "\x17")
	wantText(t, editor, "line one\nline ")

	editor.SetText("line one\n")
	press(editor, "\x17")
	wantText(t, editor, "line one")

	editor.SetText("foo 😀😀 bar")
	press(editor, "\x17")
	wantText(t, editor, "foo 😀😀 ")
	press(editor, "\x17")
	wantText(t, editor, "foo ")

	editor.SetText("foo bar")
	press(editor, "\x1b\x7f")
	wantText(t, editor, "foo ")
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

// Ported from "stops at fullwidth Chinese punctuation (issue #4972)" and
// "handles mixed CJK and ASCII word movement".
func TestEditorCJKWordMovement(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("你好，世界")
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 3)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 2)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 0)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 2)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 3)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 5)

	editor.SetText("hello你好，world世界")
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 13)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 8)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 7)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 5)
	press(editor, "\x1b[1;5D")
	wantCursor(t, editor, 0, 0)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 5)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 7)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 8)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 13)
	press(editor, "\x1b[1;5C")
	wantCursor(t, editor, 0, 15)
}

// Ported from "Word wrapping" wordWrapLine cases.
func TestWordWrapLine(t *testing.T) {
	wantChunks := func(t *testing.T, line string, width int, want ...string) {
		t.Helper()
		chunks := wordWrapLine(line, width, nil)
		got := make([]string, len(chunks))
		for index, chunk := range chunks {
			got[index] = chunk.text
		}
		if len(got) != len(want) {
			t.Fatalf("wordWrapLine(%q, %d) = %q, want %q", line, width, got, want)
		}
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("wordWrapLine(%q, %d)[%d] = %q, want %q", line, width, index, got[index], want[index])
			}
		}
	}

	wantChunks(t, "hello world test", 11, "hello ", "world test")
	wantChunks(t, "hello world test", 12, "hello world ", "test")
	wantChunks(t, "aaaaaaaaaaaa aaaa", 12, "aaaaaaaaaaaa", " aaaa")
	wantChunks(t, "      aaaaaaaaaaaa", 12, "      ", "aaaaaaaaaaaa")
	wantChunks(t, "Lorem ipsum dolor sit amet,    consectetur", 30, "Lorem ipsum dolor sit ", "amet,    consectetur")
	wantChunks(t, "Lorem ipsum dolor sit amet,              consectetur", 30, "Lorem ipsum dolor sit ", "amet,              consectetur")
	wantChunks(t, "Lorem ipsum dolor sit amet,               consectetur", 30, "Lorem ipsum dolor sit ", "amet,               ", "consectetur")
	wantChunks(t, "Lorem ipsum dolor sit amet,                         consectetur", 30, "Lorem ipsum dolor sit ", "amet,                         ", "consectetur")
	wantChunks(t, "Lorem ipsum dolor sit amet,                          consectetur", 30, "Lorem ipsum dolor sit ", "amet,                         ", " consectetur")
	wantChunks(t, "Lorem ipsum dolor sit amet,                                     consectetur", 30, "Lorem ipsum dolor sit ", "amet,                         ", "            consectetur")

	// Force-break when a wide char after a word-boundary wrap still overflows.
	line := " " + strings.Repeat("a", 186) + "你"
	chunks := wordWrapLine(line, 187, nil)
	reconstructed := ""
	for _, chunk := range chunks {
		if VisibleWidth(chunk.text) > 187 {
			t.Fatalf("chunk overflows: %q", chunk.text)
		}
		reconstructed += runeSlice(line, chunk.startIndex, chunk.endIndex)
	}
	if reconstructed != line {
		t.Fatalf("content lost during wrap")
	}
}

func TestCJKBreakScriptExtensions(t *testing.T) {
	for _, value := range []string{"·", "、", "あ", "한", "ㄅ", "界", "ｦ"} {
		if !isCJKBreakGrapheme(value) {
			t.Errorf("%q should match cjkBreakRegex", value)
		}
	}
	for _, value := range []string{"A", "Ａ", "０", "🙂"} {
		if isCJKBreakGrapheme(value) {
			t.Errorf("%q should not match cjkBreakRegex", value)
		}
	}
}

func TestWordWrapLineOversizedAtomicSegments(t *testing.T) {
	marker := "[paste #1 +20 lines]"
	checkChunks := func(t *testing.T, line string, segments []segment) []textChunk {
		t.Helper()
		chunks := wordWrapLine(line, 10, segments)
		reconstructed := ""
		for _, chunk := range chunks {
			if VisibleWidth(chunk.text) > 10 {
				t.Fatalf("chunk %q exceeds width", chunk.text)
			}
			reconstructed += runeSlice(line, chunk.startIndex, chunk.endIndex)
		}
		if reconstructed != line {
			t.Fatalf("content lost: %q != %q", reconstructed, line)
		}
		return chunks
	}

	line := "A" + marker + "B"
	checkChunks(t, line, []segment{{text: "A", index: 0}, {text: marker, index: 1}, {text: "B", index: 1 + len(marker)}})

	line = marker + "B"
	chunks := checkChunks(t, line, []segment{{text: marker, index: 0}, {text: "B", index: len(marker)}})
	if !strings.Contains(chunks[len(chunks)-1].text, "B") {
		t.Fatalf("B not on last chunk: %q", chunks[len(chunks)-1].text)
	}

	line = "A" + marker
	chunks = checkChunks(t, line, []segment{{text: "A", index: 0}, {text: marker, index: 1}})
	if chunks[0].text != "A" {
		t.Fatalf("first chunk = %q", chunks[0].text)
	}

	marker2 := "[paste #2 +30 lines]"
	line = marker + marker2
	checkChunks(t, line, []segment{{text: marker, index: 0}, {text: marker2, index: len(marker)}})

	line = marker + " hello world"
	segments := []segment{{text: marker, index: 0}, {text: " ", index: len(marker)}}
	for i, r := range "hello world" {
		if r == ' ' {
			segments = append(segments, segment{text: " ", index: len(marker) + 1 + i})
		} else {
			segments = append(segments, segment{text: string(r), index: len(marker) + 1 + i})
		}
	}
	chunks = checkChunks(t, line, segments)
	if chunks[len(chunks)-1].text != "world" {
		t.Fatalf("last chunk = %q, want world", chunks[len(chunks)-1].text)
	}
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

func TestEditorKillRingCycling(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("first")
	press(editor, "\x17")
	editor.SetText("second")
	press(editor, "\x17")
	editor.SetText("third")
	press(editor, "\x17")
	wantText(t, editor, "")

	press(editor, "\x19")
	wantText(t, editor, "third")
	press(editor, "\x1by")
	wantText(t, editor, "second")
	press(editor, "\x1by")
	wantText(t, editor, "first")
	press(editor, "\x1by")
	wantText(t, editor, "third")
}

func TestEditorKillRingAccumulation(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("one two three")
	press(editor, "\x17", "\x17", "\x17")
	wantText(t, editor, "")
	press(editor, "\x19")
	wantText(t, editor, "one two three")

	editor = newTestEditor()
	editor.SetText("line1\nline2\nline3")
	press(editor, "\x15", "\x15", "\x15", "\x15", "\x15")
	wantText(t, editor, "")
	press(editor, "\x19")
	wantText(t, editor, "line1\nline2\nline3")

	editor = newTestEditor()
	editor.SetText("prefix|suffix")
	press(editor, "\x01")
	pressN(editor, 6, "\x1b[C")
	press(editor, "\x0b", "\x0b")
	wantText(t, editor, "prefix")
	press(editor, "\x19")
	wantText(t, editor, "prefix|suffix")

	editor = newTestEditor()
	editor.SetText("foo bar baz")
	press(editor, "\x17")
	press(editor, "x")
	press(editor, "\x17")
	wantText(t, editor, "foo bar ")
	press(editor, "\x19")
	wantText(t, editor, "foo bar x")
	press(editor, "\x1by")
	wantText(t, editor, "foo bar baz")
}

func TestEditorKillRingAltD(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("foo bar baz")
	press(editor, "\x01", "\x1bd")
	wantText(t, editor, " bar baz")
	press(editor, "\x19")
	wantText(t, editor, "foo bar baz")

	editor = newTestEditor()
	editor.SetText("ab\ncd")
	press(editor, "\x1b[A", "\x05", "\x1bd")
	wantText(t, editor, "abcd")
}

func TestEditorAltDeleteWordForward(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("foo bar")
	press(editor, "\x01", "\x1b[3;3~")
	wantText(t, editor, " bar")
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

func TestEditorInsertTextAtCursorMultiline(t *testing.T) {
	editor := newTestEditor()
	press(editor, "a", "b")
	press(editor, "\x1b[D")
	editor.InsertTextAtCursor("1\n2\n3")
	wantText(t, editor, "a1\n2\n3b")
	wantCursor(t, editor, 2, 1)

	editor = newTestEditor()
	editor.InsertTextAtCursor("x\r\ny\rz")
	wantText(t, editor, "x\ny\nz")
}

// Ported from "Character jump (Ctrl+])".
func TestEditorCharacterJump(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("hello world")
	press(editor, "\x01")
	press(editor, "\x1d", "o")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1d", "o")
	wantCursor(t, editor, 0, 7)

	// Backward.
	press(editor, "\x05")
	press(editor, "\x1b\x1d", "l")
	wantCursor(t, editor, 0, 9)
	press(editor, "\x1b\x1d", "l")
	wantCursor(t, editor, 0, 3)

	// Across lines.
	editor.SetText("abc\ndef")
	press(editor, "\x01")
	for editor.state.cursorLine > 0 {
		press(editor, "\x1b[A")
	}
	press(editor, "\x01", "\x1d", "f")
	wantCursor(t, editor, 1, 2)

	// Not found: stays put.
	editor.SetText("abc")
	press(editor, "\x01", "\x1d", "z")
	wantCursor(t, editor, 0, 0)

	// Cancelled by pressing the hotkey again.
	press(editor, "\x1d", "\x1d", "x")
	wantText(t, editor, "xabc")

	// Case sensitive.
	editor.SetText("aXx")
	press(editor, "\x01", "\x1d", "x")
	wantCursor(t, editor, 0, 2)
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

func TestEditorPasteExpansionUsesInsertionOrder(t *testing.T) {
	editor := newTestEditor()
	first := strings.Repeat("a", 1001) + "[paste #2 1001 chars]"
	second := strings.Repeat("b", 1001)
	editor.HandleInput(keyEventFor("\x1b[200~" + first + "\x1b[201~"))
	editor.HandleInput(keyEventFor("\x1b[200~" + second + "\x1b[201~"))
	want := strings.Repeat("a", 1001) + second + second
	if got := editor.GetExpandedText(); got != want {
		t.Fatalf("expanded nested markers out of order: got %d bytes, want %d", len(got), len(want))
	}
}

func TestEditorManuallyTypedMarkerNotAtomic(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("[paste #1 +5 lines]")
	press(editor, "\x7f")
	wantText(t, editor, "[paste #1 +5 lines")
}

func TestEditorCharsPasteMarkerLabel(t *testing.T) {
	editor := newTestEditor()
	long := strings.Repeat("x", 1001)
	editor.HandleInput(keyEventFor("\x1b[200~" + long + "\x1b[201~"))
	wantText(t, editor, "[paste #1 1001 chars]")
	if got := editor.GetExpandedText(); got != long {
		t.Fatalf("expanded length = %d", len(got))
	}
}

func TestEditorPasteDecodesCSIu(t *testing.T) {
	// tmux popups re-encode Ctrl+J as CSI-u inside bracketed paste.
	editor := newTestEditor()
	editor.HandleInput(keyEventFor("\x1b[200~ab\x1b[106;5ucd\x1b[201~"))
	wantText(t, editor, "ab\ncd")
}

type scriptedProvider struct {
	suggest func(lines []string, cursorLine, cursorCol int, force bool) *AutocompleteSuggestions
	calls   atomic.Int64
}

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

// Ported from "Autocomplete" (force-file flows).
func TestEditorAutocompleteForceFile(t *testing.T) {
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func(lines []string, _, cursorCol int, force bool) *AutocompleteSuggestions {
		if !force {
			return nil
		}
		prefix := runeSlice(lines[0], 0, cursorCol)
		if prefix == "Work" {
			return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: "Workspace/", Label: "Workspace/"}}, Prefix: "Work"}
		}
		return nil
	}})

	press(editor, "W", "o", "r", "k")
	wantText(t, editor, "Work")
	press(editor, "\t")
	editor.flushAutocomplete()
	wantText(t, editor, "Workspace/")
	if editor.IsShowingAutocomplete() {
		t.Fatal("menu should not be showing after auto-apply")
	}
	press(editor, "\x1f")
	wantText(t, editor, "Work")
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

func TestEditorAutocompleteAfterUnicodeWhitespace(t *testing.T) {
	provider := &scriptedProvider{suggest: func(lines []string, _ int, cursorCol int, _ bool) *AutocompleteSuggestions {
		prefix := runeSlice(lines[0], 0, cursorCol)
		return &AutocompleteSuggestions{Items: []AutocompleteItem{{Value: prefix, Label: prefix}}, Prefix: prefix}
	}}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(provider)
	press(editor, "\u00a0", "#", "x")
	editor.flushAutocomplete()
	if provider.calls.Load() == 0 || !editor.IsShowingAutocomplete() {
		t.Fatalf("Unicode-whitespace trigger: calls=%d showing=%v", provider.calls.Load(), editor.IsShowingAutocomplete())
	}
}

func TestEditorJavaScriptWhitespaceTrim(t *testing.T) {
	editor := newTestEditor()
	editor.AddToHistory("\uFEFF")
	press(editor, "\x1b[A")
	wantText(t, editor, "")

	var submitted string
	editor.OnSubmit = func(value string) { submitted = value }
	editor.SetText("\uFEFF hello \uFEFF")
	press(editor, "\r")
	if submitted != "hello" {
		t.Fatalf("submitted = %q, want hello", submitted)
	}
}

func TestEditorPageMovement(t *testing.T) {
	editor := newTestEditor()
	editor.SetText(strings.TrimSuffix(strings.Repeat("line\n", 20), "\n"))
	press(editor, "\x1b[5~")
	wantCursor(t, editor, 12, 4)
	press(editor, "\x1b[6~")
	wantCursor(t, editor, 19, 4)
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

func TestEditorAutocompleteForceKeepsOpenWhileTyping(t *testing.T) {
	allFiles := []AutocompleteItem{
		{Value: "readme.md", Label: "readme.md"},
		{Value: "package.json", Label: "package.json"},
		{Value: "src/", Label: "src/"},
		{Value: "dist/", Label: "dist/"},
	}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(&scriptedProvider{suggest: func(lines []string, _, cursorCol int, force bool) *AutocompleteSuggestions {
		prefix := runeSlice(lines[0], 0, cursorCol)
		if !force && !strings.Contains(prefix, "/") && !strings.HasPrefix(prefix, ".") {
			return nil
		}
		var filtered []AutocompleteItem
		for _, file := range allFiles {
			if strings.HasPrefix(strings.ToLower(file.Value), strings.ToLower(prefix)) {
				filtered = append(filtered, file)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		return &AutocompleteSuggestions{Items: filtered, Prefix: prefix}
	}})

	press(editor, "\t")
	editor.flushAutocomplete()
	if !editor.IsShowingAutocomplete() {
		t.Fatal("menu should open on Tab")
	}
	press(editor, "r")
	editor.flushAutocomplete()
	wantText(t, editor, "r")
	if !editor.IsShowingAutocomplete() {
		t.Fatal("menu should stay open in force mode")
	}
	press(editor, "e")
	editor.flushAutocomplete()
	wantText(t, editor, "re")
	if !editor.IsShowingAutocomplete() {
		t.Fatal("menu should stay open")
	}
	press(editor, "\t")
	wantText(t, editor, "readme.md")
	if editor.IsShowingAutocomplete() {
		t.Fatal("menu should close after Tab accept")
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

func TestEditorAutocompleteHidesWhenBackspacedToEmpty(t *testing.T) {
	commands := []SlashCommand{{Name: "help", Description: "Show help"}, {Name: "clear", Description: "Clear"}}
	editor := newTestEditor()
	editor.SetAutocompleteProvider(NewCombinedAutocompleteProvider(commands, t.TempDir(), ""))

	press(editor, "/")
	editor.flushAutocomplete()
	if !editor.IsShowingAutocomplete() {
		t.Fatal("slash menu should show")
	}
	press(editor, "\x7f")
	editor.flushAutocomplete()
	if editor.IsShowingAutocomplete() {
		t.Fatal("menu should hide when slash removed")
	}
	wantText(t, editor, "")
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

func TestEditorRenderBordersAndCursor(t *testing.T) {
	editor := newTestEditor()
	lines := editor.Render(10)
	if len(lines) != 3 {
		t.Fatalf("empty editor renders %d lines", len(lines))
	}
	if lines[0] != strings.Repeat("─", 10) || lines[2] != strings.Repeat("─", 10) {
		t.Fatalf("borders = %q / %q", lines[0], lines[2])
	}
	if !strings.Contains(lines[1], "\x1b[7m \x1b[0m") {
		t.Fatalf("content line missing cursor: %q", lines[1])
	}

	// Focused editor emits the cursor marker.
	editor.SetFocused(true)
	lines = editor.Render(10)
	if !strings.Contains(lines[1], CursorMarker) {
		t.Fatalf("focused render missing cursor marker: %q", lines[1])
	}

	// Wide chars never overflow the width.
	editor.SetText("こんにちは世界こんにちは世界")
	for _, line := range editor.Render(12) {
		clean := strings.ReplaceAll(line, CursorMarker, "")
		if VisibleWidth(clean) > 12 {
			t.Fatalf("line overflows: %q (%d)", line, VisibleWidth(clean))
		}
	}
}

func TestEditorScrollIndicators(t *testing.T) {
	editor := NewEditor(NewTUI(newFakeTerminal(80, 24)), EditorTheme{})
	editor.SetText(strings.TrimRight(strings.Repeat("line\n", 30), "\n"))
	press(editor, "\x1b[A") // ensure a render window above
	lines := editor.Render(20)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "↑") {
		t.Fatalf("no scroll-up indicator in %q", joined)
	}
}

func TestEditorGetLinesDefensiveCopy(t *testing.T) {
	editor := newTestEditor()
	editor.SetText("a\nb")
	lines := editor.GetLines()
	lines[0] = "mutated"
	wantText(t, editor, "a\nb")
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
