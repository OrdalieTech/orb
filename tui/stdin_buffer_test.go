package tui

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestStdinBufferPreservesPasteAndKeyOrderWithinOneRead(t *testing.T) {
	var events []string
	buffer := NewStdinBuffer(time.Second, time.Second,
		func(value string) { events = append(events, "data:"+value) },
		func(value string) { events = append(events, "paste:"+value) },
	)
	defer buffer.Close()

	buffer.Process("\x1b[200~pasted\x1b[201~\r")

	want := []string{"paste:pasted", "data:\r"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}

	events = nil
	buffer.Process("\x1b[200~split")
	buffer.Process(" paste\x1b[201~\r")
	want = []string{"paste:split paste", "data:\r"}
	if !slices.Equal(events, want) {
		t.Fatalf("split events = %#v, want %#v", events, want)
	}
}

func TestStdinBufferReassemblesSplitUTF8(t *testing.T) {
	var data []string
	buffer := NewStdinBuffer(time.Second, time.Second, func(value string) { data = append(data, value) }, nil)
	defer buffer.Close()
	buffer.Process("\xc3")
	buffer.Process("\xa9")
	buffer.Process("\x1b\xc3")
	buffer.Process("\xa9")
	if !slices.Equal(data, []string{"é", "\x1bé"}) {
		t.Fatalf("split UTF-8 = %#v", data)
	}
}

func TestStdinBufferStaleTimerCannotFlushFreshSequence(t *testing.T) {
	var data []string
	buffer := NewStdinBuffer(time.Hour, time.Hour, func(value string) { data = append(data, value) }, nil)
	defer buffer.Close()

	buffer.Process("\x1b[<35")
	buffer.mu.Lock()
	staleGeneration := buffer.timerGeneration
	buffer.mu.Unlock()

	buffer.Process(";10")

	// Simulate the first timer having fired but parked on mu until after
	// Process re-armed: the stale flush must not steal the fresh sequence's
	// completion window.
	if flushed := buffer.flushExpired(staleGeneration); flushed != nil {
		t.Fatalf("stale timer flushed %#v", flushed)
	}
	buffer.mu.Lock()
	buffered := buffer.buffer
	buffer.mu.Unlock()
	if buffered != "\x1b[<35;10" {
		t.Fatalf("buffered = %q", buffered)
	}
	if len(data) != 0 {
		t.Fatalf("data = %#v", data)
	}
}

func TestStdinBufferSplitSequenceKeepsCompletionWindow(t *testing.T) {
	// Timing port of the review repro: the second chunk of a split escape
	// sequence arriving at the flush deadline must still get a full timeout
	// window before the combined incomplete sequence is flushed raw.
	const timeout = 2 * time.Millisecond
	type stamped struct {
		value string
		at    time.Time
	}
	for trial := 0; trial < 50; trial++ {
		var mu sync.Mutex
		var emitted []stamped
		buffer := NewStdinBuffer(timeout, timeout, func(value string) {
			now := time.Now()
			mu.Lock()
			emitted = append(emitted, stamped{value: value, at: now})
			mu.Unlock()
		}, nil)
		buffer.Process("\x1b[<35")
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
		}
		// Stamped before Process arms the new window, so a pause before the
		// next line can only widen the measured gap.
		second := time.Now()
		buffer.Process(";10")
		time.Sleep(4 * timeout)
		mu.Lock()
		for _, entry := range emitted {
			if entry.value == "\x1b[<35;10" && entry.at.Before(second.Add(timeout/2)) {
				mu.Unlock()
				t.Fatalf("trial %d: incomplete sequence flushed %v after second chunk", trial, entry.at.Sub(second))
			}
		}
		mu.Unlock()
		buffer.Close()
	}
}

// A terminal reply that arrives split waits for its terminator instead of turning into typed
// hex and a Ctrl+G from its BEL (upstream 1.2.0); an Alt+] typed before text still comes through.
func TestStdinBufferWaitsForASplitTerminalReply(t *testing.T) {
	var mu sync.Mutex
	var data []string
	buffer := NewStdinBuffer(5*time.Millisecond, 5*time.Millisecond, func(value string) {
		mu.Lock()
		data = append(data, value)
		mu.Unlock()
	}, nil)
	received := func(want []string) {
		t.Helper()
		for deadline := time.Now().Add(time.Second); ; time.Sleep(5 * time.Millisecond) {
			mu.Lock()
			got := slices.Clone(data)
			mu.Unlock()
			if slices.Equal(got, want) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("data = %q, want %q", got, want)
			}
		}
	}
	buffer.Process("\x1b]11;rgb:1e1e/")
	time.Sleep(50 * time.Millisecond)
	buffer.Process("1e1e/2e2e\x07a")
	received([]string{"\x1b]11;rgb:1e1e/1e1e/2e2e\x07", "a"})
	buffer.Process("\x1b]x")
	received([]string{"\x1b]11;rgb:1e1e/1e1e/2e2e\x07", "a", "\x1b]", "x"})
}
