package modes

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/codingagent/modes/theme"
	"github.com/OrdalieTech/orb/tui"
)

// framePlain renders the composer and asserts every row occupies exactly
// `width` cells, then strips styling so the frame can be compared literally.
// The hardware-cursor marker is zero width but not an ANSI CSI, so it goes
// first.
func framePlain(t *testing.T, component tui.Component, width int) []string {
	t.Helper()
	lines := component.Render(width)
	plain := make([]string, len(lines))
	for index, line := range lines {
		line = strings.ReplaceAll(line, tui.CursorMarker, "")
		if got := tui.VisibleWidth(line); got != width {
			t.Fatalf("row %d rendered %d cells at width %d: %q", index, got, width, line)
		}
		plain[index] = selectorANSI.ReplaceAllString(line, "")
	}
	return plain
}

func wantFrame(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("composer frame:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func newFrameEditor(t *testing.T, columns, rows int) *CustomEditor {
	t.Helper()
	initTestTheme(t)
	editor := NewCustomEditor(tui.NewTUI(newFakeTerminal(columns, rows)), theme.EditorTheme(), NewAppKeybindings(nil))
	editor.SetFocused(true)
	return editor
}

func TestComposerFrameEmptyAndPopulated(t *testing.T) {
	editor := newFrameEditor(t, 40, 24)

	wantFrame(t, framePlain(t, editor, 12),
		"╭──────────╮",
		"│          │",
		"╰──────────╯",
	)

	// The wrap width is the interior, not the terminal width: "hello world go"
	// breaks after "hello" inside a 12-cell frame.
	editor.SetText("hello world go")
	wantFrame(t, framePlain(t, editor, 12),
		"╭──────────╮",
		"│hello     │",
		"│world go  │",
		"╰──────────╯",
	)

	editor.SetText("ab")
	lines := editor.Render(12)
	marker := strings.Index(lines[1], tui.CursorMarker)
	if marker < 0 {
		t.Fatalf("focused composer dropped the cursor marker: %q", lines[1])
	}
	if column := tui.VisibleWidth(lines[1][:marker]); column != 3 {
		t.Fatalf("cursor marker at column %d, want 3: %q", column, lines[1])
	}
}

func TestComposerFrameKeepsScrollHints(t *testing.T) {
	editor := newFrameEditor(t, 20, 24)
	editor.SetText(strings.TrimRight(strings.Repeat("draft\n", 20), "\n"))

	scrolled := framePlain(t, editor, 20)
	wantFrame(t, []string{scrolled[0], scrolled[len(scrolled)-1]},
		"╭─── ↑ 13 more ────╮",
		"╰──────────────────╯",
	)

	for range 25 {
		editor.HandleInput(tui.KeyEvent{Raw: "\x1b[A"})
	}
	atTop := framePlain(t, editor, 20)
	wantFrame(t, []string{atTop[0], atTop[len(atTop)-1]},
		"╭──────────────────╮",
		"╰─── ↓ 13 more ────╯",
	)
}

// frameCompletions is the smallest provider that keeps a popup on screen.
type frameCompletions struct{}

func (frameCompletions) GetSuggestions(_ context.Context, lines []string, _, _ int, _ bool) *tui.AutocompleteSuggestions {
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "/") {
		return nil
	}
	return &tui.AutocompleteSuggestions{
		Items:  []tui.AutocompleteItem{{Value: "/model", Label: "model"}, {Value: "/clear", Label: "clear"}},
		Prefix: lines[0],
	}
}

func (frameCompletions) ApplyCompletion(lines []string, cursorLine, _ int, item tui.AutocompleteItem, _ string) tui.CompletionResult {
	next := append([]string(nil), lines...)
	next[cursorLine] = item.Value
	return tui.CompletionResult{Lines: next, CursorLine: cursorLine, CursorCol: len([]rune(item.Value))}
}

func waitForPopup(t *testing.T, editor *CustomEditor) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !editor.IsShowingAutocomplete() {
		if time.Now().After(deadline) {
			t.Fatal("autocomplete popup never opened")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestComposerFrameHangsAutocompleteBelowTheClosedBox(t *testing.T) {
	editor := newFrameEditor(t, 24, 24)
	editor.SetAutocompleteProvider(frameCompletions{})
	editor.HandleInput(tui.KeyEvent{Raw: "/"})
	waitForPopup(t, editor)

	lines := framePlain(t, editor, 24)
	if len(lines) != 5 {
		t.Fatalf("composer with popup = %#v", lines)
	}
	wantFrame(t, lines[:3],
		"╭──────────────────────╮",
		"│/                     │",
		"╰──────────────────────╯",
	)
	// The popup keeps the interior's column offset without being framed, so a
	// click maps through the same one-column inset as the text rows.
	for _, row := range lines[3:] {
		if strings.ContainsAny(row, "│╭╮╰╯") || !strings.HasPrefix(row, " ") || !strings.HasSuffix(row, " ") {
			t.Fatalf("popup row was framed: %q", row)
		}
	}
	if !strings.Contains(lines[3], "model") || !strings.Contains(lines[4], "clear") {
		t.Fatalf("popup rows = %#v", lines[3:])
	}

	if !editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 4, Column: 3}) {
		t.Fatal("click on the second suggestion was not consumed")
	}
	if got := editor.GetText(); got != "/clear" {
		t.Fatalf("clicked suggestion applied %q", got)
	}
}

func TestComposerFrameMouseLandsOnTheClickedCharacter(t *testing.T) {
	editor := newFrameEditor(t, 24, 24)
	editor.SetText("abcdef")
	editor.Render(24)

	// Row 0 is the top rail; column 0 is the left rail, so column 1+n is the
	// nth character.
	if !editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 1, Column: 4}) {
		t.Fatal("click inside the composer was not consumed")
	}
	if _, column := editor.GetCursor(); column != 3 {
		t.Fatalf("click at column 4 placed the cursor at %d, want 3", column)
	}
	if editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 0, Column: 4}) {
		t.Fatal("click on the top rail should fall through to the transcript")
	}

	editor.Render(2)
	if !editor.HandleMouse(tui.MouseEvent{Type: tui.MousePress, Row: 1, Column: 1}) {
		t.Fatal("click in an unframed narrow composer was not consumed")
	}
	if _, column := editor.GetCursor(); column != 1 {
		t.Fatalf("narrow click at column 1 placed the cursor at %d", column)
	}
}

func TestComposerFrameNarrowWidths(t *testing.T) {
	editor := newFrameEditor(t, 40, 24)

	// Below four columns there is no interior to frame; the plain rails stay.
	wantFrame(t, framePlain(t, editor, 1), "─", " ", "─")
	wantFrame(t, framePlain(t, editor, 2), "──", "  ", "──")

	editor.SetText("hi")
	for width := range editorFrameMinWidth {
		lines := editor.Render(width)
		if len(lines) == 0 || strings.ContainsAny(strings.Join(lines, ""), "│╭╮╰╯") {
			t.Fatalf("composer at width %d = %#v", width, lines)
		}
	}
	wantFrame(t, framePlain(t, editor, 4), "╭──╮", "│h │", "│i │", "╰──╯")

	for width := editorFrameMinWidth; width <= 64; width++ {
		framePlain(t, editor, width)
	}
}
