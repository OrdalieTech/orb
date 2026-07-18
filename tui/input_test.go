package tui

import (
	"strings"
	"testing"
)

func wantValue(t *testing.T, input *Input, want string) {
	t.Helper()
	if got := input.GetValue(); got != want {
		t.Fatalf("value = %q, want %q", got, want)
	}
}

// Ported from upstream packages/tui/test/input.test.ts.
func TestInputSubmitAndBackslash(t *testing.T) {
	input := NewInput()
	var submitted []string
	input.OnSubmit = func(value string) { submitted = append(submitted, value) }
	press(input, "h", "e", "l", "l", "o", "\\", "\r")
	if len(submitted) != 1 || submitted[0] != "hello\\" {
		t.Fatalf("submitted = %q", submitted)
	}

	input = NewInput()
	press(input, "\\", "x")
	wantValue(t, input, "\\x")
}

func TestInputRenderWideText(t *testing.T) {
	cases := []string{
		"가나다라마바사아자차카타파하 한글 텍스트가 터미널 너비를 초과하면 크래시가 발생합니다 이것은 재현용 테스트입니다",
		"これはテスト文章です。日本語のテキストが正しく表示されるかどうかを確認するためのサンプルテキストです。あいうえお",
		"这是一段测试文本，用于验证中文字符在终端中的显示宽度是否被正确计算，如果不正确就会导致用户界面崩溃的问题",
		"ＡＢＣＤＥＦＧＨＩＪＫＬＭＮＯＰＱＲＳＴＵＶＷＸＹＺ０１２３４５６７８９ａｂｃｄｅｆｇｈｉｊｋｌｍ",
	}
	width := 93
	moves := map[string]func(*Input){
		"start":  func(*Input) {},
		"middle": func(input *Input) { pressN(input, 10, "\x1b[C") },
		"end":    func(input *Input) { press(input, "\x05") },
	}
	for _, text := range cases {
		for label, move := range moves {
			input := NewInput()
			input.SetValue(text)
			input.SetFocused(true)
			move(input)
			lines := input.Render(width)
			if len(lines) != 1 {
				t.Fatalf("render returned %d lines", len(lines))
			}
			clean := strings.ReplaceAll(lines[0], CursorMarker, "")
			if VisibleWidth(clean) > width {
				t.Fatalf("rendered line overflowed for %q at %s: %d", text, label, VisibleWidth(clean))
			}
		}
	}

	input := NewInput()
	input.SetValue("가나다라마바사아자차카타파하")
	input.SetFocused(true)
	press(input, "\x01")
	pressN(input, 5, "\x1b[C")
	lines := input.Render(20)
	clean := strings.ReplaceAll(lines[0], CursorMarker, "")
	if VisibleWidth(clean) > 20 {
		t.Fatalf("scrolled render overflowed: %d", VisibleWidth(clean))
	}
}

func TestInputKillRing(t *testing.T) {
	input := NewInput()
	input.SetValue("foo bar baz")
	press(input, "\x05", "\x17")
	wantValue(t, input, "foo bar ")
	press(input, "\x01", "\x19")
	wantValue(t, input, "bazfoo bar ")

	// ASCII punctuation boundaries.
	input = NewInput()
	input.SetValue("foo.bar")
	press(input, "\x05", "\x17")
	wantValue(t, input, "foo.")

	input = NewInput()
	input.SetValue("hello world")
	press(input, "\x05")
	pressN(input, 5, "\x1b[D")
	press(input, "\x15")
	wantValue(t, input, "world")
	press(input, "\x19")
	wantValue(t, input, "hello world")

	input = NewInput()
	input.SetValue("hello world")
	press(input, "\x01", "\x0b")
	wantValue(t, input, "")
	press(input, "\x19")
	wantValue(t, input, "hello world")

	input = NewInput()
	input.SetValue("test")
	press(input, "\x19")
	wantValue(t, input, "test")
}

// Ported from "non-delete actions break kill accumulation" (input.test.ts).
func TestInputKillRingCycling(t *testing.T) {
	input := NewInput()
	input.SetValue("foo bar baz")
	press(input, "\x05", "\x17")
	wantValue(t, input, "foo bar ")
	press(input, "x") // typing breaks accumulation
	wantValue(t, input, "foo bar x")
	press(input, "\x17")
	wantValue(t, input, "foo bar ")

	press(input, "\x19") // most recent entry is "x"
	wantValue(t, input, "foo bar x")
	press(input, "\x1by") // cycles to "baz"
	wantValue(t, input, "foo bar baz")

	// Alt+Y without preceding yank does nothing.
	input = NewInput()
	input.SetValue("stale")
	press(input, "\x05", "\x1by")
	wantValue(t, input, "stale")
}

func TestInputAltD(t *testing.T) {
	input := NewInput()
	input.SetValue("foo bar baz")
	press(input, "\x01", "\x1bd")
	wantValue(t, input, " bar baz")
	press(input, "\x19")
	wantValue(t, input, "foo bar baz")
}

func TestInputUndo(t *testing.T) {
	input := NewInput()
	press(input, "\x1f")
	wantValue(t, input, "")

	press(input, "h", "i", " ", "y", "o")
	wantValue(t, input, "hi yo")
	press(input, "\x1f")
	wantValue(t, input, "hi")
	press(input, "\x1f")
	wantValue(t, input, "")

	input = NewInput()
	press(input, "a", "b", "\x7f")
	wantValue(t, input, "a")
	press(input, "\x1f")
	wantValue(t, input, "ab")

	// Paste is atomic.
	input = NewInput()
	press(input, "a")
	input.HandleInput(keyEventFor("\x1b[200~more text\x1b[201~"))
	wantValue(t, input, "amore text")
	press(input, "\x1f")
	wantValue(t, input, "a")
}

func TestInputPasteStripsNewlines(t *testing.T) {
	input := NewInput()
	input.HandleInput(keyEventFor("\x1b[200~line1\nline2\r\nline3\ttab\x1b[201~"))
	wantValue(t, input, "line1line2line3    tab")
}

func TestInputGraphemeMovement(t *testing.T) {
	input := NewInput()
	press(input, "😀", "👍", "\x7f")
	wantValue(t, input, "😀")
	press(input, "🎉", "\x1b[D", "\x1b[D", "x")
	wantValue(t, input, "x😀🎉")
}
