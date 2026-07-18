package tui

import (
	"testing"
	"time"
)

func TestKeyboardProtocolNegotiationParser(t *testing.T) {
	tests := []struct {
		sequence, kind string
		flags          int
		ok             bool
	}{{"\x1b[?7u", "kitty-flags", 7, true}, {"\x1b[?0u", "kitty-flags", 0, true}, {"\x1b[?62;4;52c", "device-attributes", 0, true}, {"\x1b[A", "", 0, false}}
	for _, test := range tests {
		got, ok := ParseKeyboardProtocolNegotiation(test.sequence)
		if ok != test.ok || got.Type != test.kind || got.Flags != test.flags {
			t.Errorf("parse %q = %#v, %v", test.sequence, got, ok)
		}
	}
}

func TestNormalizeAppleTerminalInput(t *testing.T) {
	if got := NormalizeAppleTerminalInput("\r", true, true); got != "\x1b[13;2u" {
		t.Fatalf("shift return = %q", got)
	}
	if got := NormalizeAppleTerminalInput("\r", true, false); got != "\r" {
		t.Fatalf("plain return = %q", got)
	}
	if got := NormalizeAppleTerminalInput("a", true, true); got != "a" {
		t.Fatalf("non-return = %q", got)
	}
}

func TestProcessTerminalReassemblesAndReplaysNegotiationFragments(t *testing.T) {
	input := make(chan string, 3)
	terminal := &ProcessTerminal{started: true, inputHandler: func(value string) { input <- value }}
	t.Cleanup(func() {
		terminal.mu.Lock()
		terminal.clearNegotiationBufferLocked()
		terminal.mu.Unlock()
		SetKittyProtocolActive(false)
	})
	terminal.handleSequence("\x1b[?")
	terminal.handleSequence("7")
	terminal.handleSequence("u")
	if !terminal.KittyProtocolActive() {
		t.Fatal("split Kitty response did not activate protocol")
	}
	select {
	case leaked := <-input:
		t.Fatalf("negotiation leaked as input: %q", leaked)
	default:
	}

	terminal.handleSequence("\x1b[?")
	terminal.handleSequence("x")
	if got := <-input; got != "\x1b[?" {
		t.Fatalf("buffered input = %q", got)
	}
	if got := <-input; got != "x" {
		t.Fatalf("current input = %q", got)
	}
}

func TestProcessTerminalDrainWaitsForInputIdleAndRestoresHandler(t *testing.T) {
	handler := func(string) {}
	terminal := &ProcessTerminal{started: true, inputHandler: handler}
	go func() {
		time.Sleep(15 * time.Millisecond)
		terminal.mu.Lock()
		terminal.lastInput = time.Now()
		terminal.mu.Unlock()
	}()
	started := time.Now()
	terminal.DrainInput(200*time.Millisecond, 30*time.Millisecond)
	if elapsed := time.Since(started); elapsed < 35*time.Millisecond || elapsed > 150*time.Millisecond {
		t.Fatalf("drain duration = %s", elapsed)
	}
	terminal.mu.Lock()
	restored := terminal.inputHandler != nil
	terminal.mu.Unlock()
	if !restored {
		t.Fatal("input handler was not restored after drain")
	}
}
