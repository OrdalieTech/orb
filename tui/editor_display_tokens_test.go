package tui

import (
	"strings"
	"testing"
)

// chipTokens chips whitespace-delimited "/skill:name" runs whose name is known.
func chipTokens(known ...string) func(string) []DisplayToken {
	return func(line string) []DisplayToken {
		var tokens []DisplayToken
		runes := []rune(line)
		for start := 0; start < len(runes); {
			end := start
			for end < len(runes) && runes[end] != ' ' {
				end++
			}
			word := string(runes[start:end])
			for _, name := range known {
				if word == "/skill:"+name {
					tokens = append(tokens, DisplayToken{Start: start, End: end, Display: "<" + name + ">"})
				}
			}
			start = end + 1
		}
		return tokens
	}
}

func renderedBody(editor *Editor, width int) []string {
	lines := editor.Render(width)
	body := make([]string, 0, len(lines))
	for _, line := range lines[1 : len(lines)-1] {
		body = append(body, StripANSI(strings.ReplaceAll(line, CursorMarker, "")))
	}
	return body
}

func TestEditorDisplayTokensDrawChipKeepText(t *testing.T) {
	editor := newTestEditor()
	editor.SetDisplayTokens(chipTokens("docx"))
	editor.SetText("use /skill:docx now")
	editor.SetFocused(true)

	if got := editor.GetText(); got != "use /skill:docx now" {
		t.Fatalf("text = %q, want the canonical token", got)
	}
	body := renderedBody(editor, 30)
	if len(body) != 1 || body[0] != "use <docx> now "+strings.Repeat(" ", 30-15) {
		t.Fatalf("rendered = %q", body)
	}
	if width := VisibleWidth(editor.Render(30)[1]); width != 30 {
		t.Fatalf("line width = %d, want 30 (padding follows the drawn width)", width)
	}

	// A cursor on the token's first rune inverts the whole chip.
	editor.SetText("/skill:docx")
	press(editor, "\x01")
	if line := editor.Render(30)[1]; !strings.Contains(line, "\x1b[7m<docx>\x1b[0m") {
		t.Fatalf("cursor on chip = %q", line)
	}
}

func TestEditorDisplayTokensEditAsOneUnit(t *testing.T) {
	editor := newTestEditor()
	editor.SetDisplayTokens(chipTokens("docx"))

	editor.SetText("use /skill:docx")
	press(editor, "\x7f")
	wantText(t, editor, "use ")
	press(editor, "\x1f")
	wantText(t, editor, "use /skill:docx")

	editor.SetText("use /skill:docx now")
	press(editor, "\x01", "\x1b[C", "\x1b[C", "\x1b[C", "\x1b[C")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1b[C")
	wantCursor(t, editor, 0, len("use /skill:docx"))
	press(editor, "\x1b[D")
	wantCursor(t, editor, 0, 4)
	press(editor, "\x1b[3~")
	wantText(t, editor, "use  now")

	editor.SetText("use /skill:docx now")
	press(editor, "\x05", "\x1b[D", "\x1b[D", "\x1b[D", "\x1b[D", "\x17")
	wantText(t, editor, "use  now")
}

// A cut at the cursor must not fake a token boundary: inside "/skill:docxy"
// the prefix "/skill:docx" is not a token of the line.
func TestEditorDisplayTokensUseWholeLineBoundaries(t *testing.T) {
	editor := newTestEditor()
	editor.SetDisplayTokens(chipTokens("docx"))
	editor.SetText("/skill:docxy")
	press(editor, "\x1b[D", "\x7f")
	wantText(t, editor, "/skill:docy")
	if body := renderedBody(editor, 30); strings.Contains(body[0], "<docx>") {
		t.Fatalf("non-token rendered as chip: %q", body)
	}
}

func TestEditorDisplayTokensMapMouseAndSelection(t *testing.T) {
	editor := newTestEditor()
	editor.SetDisplayTokens(chipTokens("docx"))
	editor.SetText("a /skill:docx b")
	editor.Render(30)

	// Drawn as "a <docx> b": column 9 is "b", rune offset 14 of the text.
	editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 9, Clicks: 1})
	wantCursor(t, editor, 0, len("a /skill:docx "))
	// Clicks on the chip land on its nearer edge.
	editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 3, Clicks: 1})
	wantCursor(t, editor, 0, 2)
	editor.HandleMouse(MouseEvent{Type: MousePress, Row: 1, Column: 7, Clicks: 1})
	wantCursor(t, editor, 0, len("a /skill:docx"))

	if got := tokenDisplayColumn("a /skill:docx b", chipTokens("docx")("a /skill:docx b"), len("a /skill:docx b")); got != len("a <docx> b") {
		t.Fatalf("display column = %d", got)
	}
}

func TestEditorDisplayTokensWrapByText(t *testing.T) {
	editor := newTestEditor()
	editor.SetDisplayTokens(chipTokens("docx"))
	editor.SetText(strings.Repeat("x", 12) + " /skill:docx tail")
	for _, line := range renderedBody(editor, 20) {
		if VisibleWidth(line) != 20 {
			t.Fatalf("wrapped line %q is %d wide", line, VisibleWidth(line))
		}
	}
	editor.SetDisplayTokens(nil)
	if body := renderedBody(editor, 40); !strings.Contains(body[0], "/skill:docx") {
		t.Fatalf("removing the scanner kept chips: %q", body)
	}
}
