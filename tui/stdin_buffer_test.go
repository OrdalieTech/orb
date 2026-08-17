package tui

import (
	"sync"
	"testing"
	"time"
)

func TestStdinBufferSequencesPasteAndKittyDuplicates(t *testing.T) {
	var data, paste []string
	buffer := NewStdinBuffer(5*time.Millisecond, 5*time.Millisecond, func(value string) { data = append(data, value) }, func(value string) { paste = append(paste, value) })
	defer buffer.Close()
	buffer.Process("abc\x1b[")
	buffer.Process("A")
	buffer.Process("\x1b[200~hello\nworld\x1b[201~")
	buffer.Process("\x1b[64u@")
	want := []string{"a", "b", "c", "\x1b[A", "\x1b[64u"}
	if !equalLines(data, want) {
		t.Fatalf("data = %#v, want %#v", data, want)
	}
	if !equalLines(paste, []string{"hello\nworld"}) {
		t.Fatalf("paste = %#v", paste)
	}
}

func TestStdinBufferPreservesPasteAndKeyOrderWithinOneRead(t *testing.T) {
	var events []string
	buffer := NewStdinBuffer(time.Second, time.Second,
		func(value string) { events = append(events, "data:"+value) },
		func(value string) { events = append(events, "paste:"+value) },
	)
	defer buffer.Close()

	buffer.Process("\x1b[200~pasted\x1b[201~\r")

	want := []string{"paste:pasted", "data:\r"}
	if !equalLines(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}

	events = nil
	buffer.Process("\x1b[200~split")
	buffer.Process(" paste\x1b[201~\r")
	want = []string{"paste:split paste", "data:\r"}
	if !equalLines(events, want) {
		t.Fatalf("split events = %#v, want %#v", events, want)
	}
}

func TestStdinBufferPreservesMixedEventsAndKittyResetAcrossPaste(t *testing.T) {
	var events []string
	buffer := NewStdinBuffer(time.Second, time.Second,
		func(value string) { events = append(events, "data:"+value) },
		func(value string) { events = append(events, "paste:"+value) },
	)
	defer buffer.Close()

	buffer.Process("a\x1b[64u\x1b[200~one\x1b[201~@\x1b[200~two\x1b[201~b")

	want := []string{"data:a", "data:\x1b[64u", "paste:one", "data:@", "paste:two", "data:b"}
	if !equalLines(events, want) {
		t.Fatalf("events = %#v, want %#v", events, want)
	}
}

func TestStdinBufferTimeoutAndWezTermEscape(t *testing.T) {
	data := make(chan string, 3)
	buffer := NewStdinBuffer(5*time.Millisecond, 5*time.Millisecond, func(value string) { data <- value }, nil)
	defer buffer.Close()
	buffer.Process("\x1b[")
	select {
	case got := <-data:
		if got != "\x1b[" {
			t.Fatalf("timeout = %q", got)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("incomplete sequence did not flush")
	}
	buffer.Process("\x1b\x1b[27;1:3u")
	if got := <-data; got != "\x1b" {
		t.Fatalf("first WezTerm event = %q", got)
	}
	if got := <-data; got != "\x1b[27;1:3u" {
		t.Fatalf("second WezTerm event = %q", got)
	}
}

func TestStdinBufferLegacyHighByte(t *testing.T) {
	data := make(chan string, 1)
	buffer := NewStdinBuffer(time.Second, time.Second, func(value string) { data <- value }, nil)
	defer buffer.Close()
	buffer.ProcessBytes([]byte{0xe1})
	if got := <-data; got != "\x1ba" {
		t.Fatalf("high byte = %q", got)
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
	if !equalLines(data, []string{"é", "\x1bé"}) {
		t.Fatalf("split UTF-8 = %#v", data)
	}
}

func TestStdinBufferKittyDedupeSkipsAstralCodepoints(t *testing.T) {
	var data []string
	buffer := NewStdinBuffer(5*time.Millisecond, 5*time.Millisecond, func(value string) { data = append(data, value) }, nil)
	defer buffer.Close()
	// Upstream compares sequence.length === 1 in UTF-16 code units, so an
	// astral printable echoed after its kitty CSI-u report is never deduped.
	buffer.Process("\x1b[128512u")
	buffer.Process("\U0001f600")
	if want := []string{"\x1b[128512u", "\U0001f600"}; !equalLines(data, want) {
		t.Fatalf("astral data = %#v, want %#v", data, want)
	}

	data = nil
	buffer.Process("\x1b[97u")
	buffer.Process("a")
	if want := []string{"\x1b[97u"}; !equalLines(data, want) {
		t.Fatalf("bmp data = %#v, want %#v", data, want)
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
	if buffered := buffer.Buffered(); buffered != "\x1b[<35;10" {
		t.Fatalf("buffered = %q", buffered)
	}
	if len(data) != 0 {
		t.Fatalf("data = %#v", data)
	}
	if got := buffer.Flush(); !equalLines(got, []string{"\x1b[<35;10"}) {
		t.Fatalf("flush = %#v", got)
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
		buffer.Process(";10")
		second := time.Now()
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

// Legacy Alt+Enter is ESC + CR: a lone ESC dispatches as Escape on its own
// short wait, while a partial report keeps the longer sequence wait.
func TestStdinBufferSplitsEscapeWaitFromSequenceWait(t *testing.T) {
	kitty := IsKittyProtocolActive()
	SetKittyProtocolActive(false)
	t.Cleanup(func() { SetKittyProtocolActive(kitty) })

	t.Run("lone escape flushes on its own wait", func(t *testing.T) {
		data := make(chan string, 2)
		buffer := NewStdinBuffer(time.Second, 5*time.Millisecond, func(value string) { data <- value }, nil)
		defer buffer.Close()
		buffer.Process("\x1b")
		select {
		case got := <-data:
			if got != "\x1b" {
				t.Fatalf("flushed %q", got)
			}
		case <-time.After(time.Second):
			t.Fatal("lone escape did not flush")
		}
	})

	t.Run("a longer escape wait reassembles split alt+enter", func(t *testing.T) {
		var data []string
		buffer := NewStdinBuffer(5*time.Millisecond, time.Minute, func(value string) { data = append(data, value) }, nil)
		defer buffer.Close()
		buffer.Process("\x1b")
		time.Sleep(25 * time.Millisecond)
		buffer.Process("\r")
		if !equalLines(data, []string{"\x1b\r"}) || ParseKey(data[0]) != "alt+enter" {
			t.Fatalf("split alt+enter = %#v", data)
		}
	})

	t.Run("the escape wait never truncates a partial report", func(t *testing.T) {
		var data []string
		buffer := NewStdinBuffer(time.Minute, 5*time.Millisecond, func(value string) { data = append(data, value) }, nil)
		defer buffer.Close()
		buffer.Process("\x1b[")
		time.Sleep(25 * time.Millisecond)
		buffer.Process("<65;48;39M")
		if !equalLines(data, []string{"\x1b[<65;48;39M"}) {
			t.Fatalf("fragmented mouse report = %#v", data)
		}
	})
}
