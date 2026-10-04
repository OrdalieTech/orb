package tui

import (
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
