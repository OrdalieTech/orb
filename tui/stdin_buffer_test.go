package tui

import (
	"testing"
	"time"
)

func TestStdinBufferSequencesPasteAndKittyDuplicates(t *testing.T) {
	var data, paste []string
	buffer := NewStdinBuffer(5*time.Millisecond, func(value string) { data = append(data, value) }, func(value string) { paste = append(paste, value) })
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
	buffer := NewStdinBuffer(time.Second,
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
	buffer := NewStdinBuffer(time.Second,
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
	buffer := NewStdinBuffer(5*time.Millisecond, func(value string) { data <- value }, nil)
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
	buffer := NewStdinBuffer(time.Second, func(value string) { data <- value }, nil)
	defer buffer.Close()
	buffer.ProcessBytes([]byte{0xe1})
	if got := <-data; got != "\x1ba" {
		t.Fatalf("high byte = %q", got)
	}
}

func TestStdinBufferReassemblesSplitUTF8(t *testing.T) {
	var data []string
	buffer := NewStdinBuffer(time.Second, func(value string) { data = append(data, value) }, nil)
	defer buffer.Close()
	buffer.Process("\xc3")
	buffer.Process("\xa9")
	buffer.Process("\x1b\xc3")
	buffer.Process("\xa9")
	if !equalLines(data, []string{"é", "\x1bé"}) {
		t.Fatalf("split UTF-8 = %#v", data)
	}
}
