//go:build js && wasm

package worker

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"syscall/js"
	"testing"
	"time"
)

// socketPair returns two cross-wired fake Worker WebSockets: send on one
// dispatches a binary message (an ArrayBuffer, as workerd delivers it) on the
// other a macrotask later, and close closes both ends.
func socketPair() (js.Value, js.Value) {
	pair := js.Global().Get("Function").New(`
		const make = () => Object.assign(new EventTarget(), { closed: false, listening: 0 });
		const a = make(), b = make();
		for (const [self, other] of [[a, b], [b, a]]) {
			const add = self.addEventListener.bind(self), remove = self.removeEventListener.bind(self);
			self.addEventListener = (type, listener) => { self.listening++; add(type, listener); };
			self.removeEventListener = (type, listener) => { self.listening--; remove(type, listener); };
			self.send = data => {
				if (self.closed) throw new Error("socket closed");
				const copy = data.slice().buffer;
				setTimeout(() => other.dispatchEvent(Object.assign(new Event("message"), { data: copy })), 0);
			};
			self.close = () => {
				if (self.closed) return;
				self.closed = other.closed = true;
				setTimeout(() => { self.dispatchEvent(new Event("close")); other.dispatchEvent(new Event("close")); }, 0);
			};
		}
		return [a, b];
	`).Invoke()
	return pair.Index(0), pair.Index(1)
}

func TestSocketConnStreamsBinaryMessages(t *testing.T) {
	left, right := socketPair()
	a, b := NewSocketConn(left), NewSocketConn(right)
	payload := bytes.Repeat([]byte("orb bridge "), 10000)
	go func() {
		for offset := 0; offset < len(payload); offset += 16 << 10 {
			_, _ = a.Write(payload[offset:min(len(payload), offset+16<<10)])
		}
	}()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(b, got); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read %d bytes, %v", len(got), err)
	}

	if err := b.SetReadDeadline(time.Now().Add(20 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past the deadline = %v", err)
	}
	_ = b.SetReadDeadline(time.Time{})

	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("read after the peer closed = %v", err)
	}
	if _, err := a.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close = %v", err)
	}
	_ = b.Close()
	_ = a.Close()
	if left.Get("listening").Int() != 0 || right.Get("listening").Int() != 0 {
		t.Fatalf("listeners left registered: %d, %d", left.Get("listening").Int(), right.Get("listening").Int())
	}
	// A late event after Close reaches no released function.
	right.Call("dispatchEvent", js.Global().Get("Event").New("close"))
}

func TestSocketConnRejectsTextMessages(t *testing.T) {
	left, right := socketPair()
	conn := NewSocketConn(right)
	defer func() { _ = conn.Close() }()
	event := js.Global().Get("Event").New("message")
	event.Set("data", "text")
	right.Call("dispatchEvent", event)
	if _, err := conn.Read(make([]byte, 1)); err == nil || err == io.EOF {
		t.Fatalf("text message read = %v", err)
	}
	_ = left
}
