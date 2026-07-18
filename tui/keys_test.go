package tui

import "testing"

func TestMatchesAndParsesKeyProtocols(t *testing.T) {
	t.Cleanup(func() { SetKittyProtocolActive(false) })
	tests := []struct {
		data   string
		key    KeyID
		parsed string
	}{
		{"\x1b[A", "up", "up"}, {"\x1b[Z", "shift+tab", "shift+tab"}, {"\x03", "ctrl+c", "ctrl+c"},
		{"\x1b[1089::99;5u", "ctrl+c", "ctrl+c"}, {"\x1b[107::118;5u", "ctrl+k", "ctrl+k"},
		{"\x1b[27;6;69~", "ctrl+shift+e", "shift+ctrl+e"}, {"\x1b[57417u", "left", "left"},
		{"\x1b[107;13u", "ctrl+super+k", "ctrl+super+k"}, {"\x1b[49;5u", "ctrl+1", "ctrl+1"},
		{"\x1b[5$", "shift+pageUp", "shift+pageUp"}, {"\x1b[8^", "ctrl+end", "ctrl+end"},
	}
	for _, test := range tests {
		if !MatchesKey(test.data, test.key) {
			t.Errorf("MatchesKey(%q, %q) = false", test.data, test.key)
		}
		if got := ParseKey(test.data); got != test.parsed {
			t.Errorf("ParseKey(%q) = %q, want %q", test.data, got, test.parsed)
		}
	}
	if MatchesKey("\x1b[107::118;5u", "ctrl+v") {
		t.Fatal("base-layout fallback overrode authoritative Latin key")
	}
}

func TestKittyEventTypeAndPrintable(t *testing.T) {
	if !IsKeyRelease("\x1b[97;1:3u") || IsKeyRelease("\x1b[200~90:62:3F\x1b[201~") {
		t.Fatal("release classification mismatch")
	}
	if !IsKeyRepeat("\x1b[1;1:2A") {
		t.Fatal("repeat not detected")
	}
	if got := DecodeKittyPrintable("\x1b[57410u"); got != "/" {
		t.Fatalf("keypad decode = %q", got)
	}
	if got := DecodePrintableKey("\x1b[27;2;196~"); got != "Ä" {
		t.Fatalf("modifyOtherKeys decode = %q", got)
	}
}

func TestKittyLegacyAmbiguity(t *testing.T) {
	SetKittyProtocolActive(false)
	if !MatchesKey("\n", "enter") || ParseKey("\n") != "enter" {
		t.Fatal("legacy linefeed should be enter")
	}
	SetKittyProtocolActive(true)
	if !MatchesKey("\n", "shift+enter") || MatchesKey("\n", "enter") || ParseKey("\n") != "shift+enter" {
		t.Fatal("Kitty linefeed should be shift+enter")
	}
}
