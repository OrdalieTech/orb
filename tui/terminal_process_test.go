//go:build unix || windows

package tui

import (
	"testing"
	"time"
)

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
