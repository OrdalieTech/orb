package tui

import (
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
