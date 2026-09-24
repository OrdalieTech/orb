//go:build js && wasm

package worker

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"syscall/js"
	"time"
)

// SocketConn is a net.Conn over an accepted Worker WebSocket (the server
// half of a WebSocketPair after accept()). Each Write is one binary message;
// Read sees incoming messages as one byte stream.
//
// The socket must not be hibernatable: the stream's state (a TLS session, for
// Bridge) lives in this Go runtime, which hibernation discards while the
// socket stays open.
type SocketConn struct {
	socket js.Value

	mu       sync.Mutex
	arrived  chan struct{}
	buffered [][]byte
	closed   bool
	cause    error
	deadline time.Time
	timer    *time.Timer

	events    []string
	listeners []js.Func
}

// NewSocketConn registers its listeners before returning, so it must be
// called in the same JavaScript turn as accept().
func NewSocketConn(socket js.Value) *SocketConn {
	conn := &SocketConn{socket: socket, arrived: make(chan struct{}, 1)}
	// Runtimes with the standard binary type deliver Blobs, whose bytes are
	// only readable asynchronously and could then arrive out of order.
	socket.Set("binaryType", "arraybuffer")
	listen := func(event string, handle func(js.Value)) {
		listener := js.FuncOf(func(_ js.Value, args []js.Value) any {
			handle(argument(args))
			return nil
		})
		conn.events, conn.listeners = append(conn.events, event), append(conn.listeners, listener)
		socket.Call("addEventListener", event, listener)
	}
	listen("message", func(event js.Value) {
		data := event.Get("data")
		if !data.InstanceOf(jsUint8Array) {
			if data.Type() != js.TypeObject || data.Get("byteLength").Type() != js.TypeNumber {
				conn.fail(errors.New("worker: text WebSocket message on a binary stream"))
				return
			}
			data = jsUint8Array.New(data)
		}
		chunk := make([]byte, data.Length())
		js.CopyBytesToGo(chunk, data)
		conn.mu.Lock()
		if !conn.closed {
			conn.buffered = append(conn.buffered, chunk)
		}
		conn.mu.Unlock()
		conn.wake()
	})
	listen("close", func(js.Value) { conn.fail(io.EOF) })
	listen("error", func(js.Value) { conn.fail(errors.New("worker: WebSocket error")) })
	return conn
}

func (conn *SocketConn) wake() {
	select {
	case conn.arrived <- struct{}{}:
	default:
	}
}

func (conn *SocketConn) fail(cause error) {
	conn.mu.Lock()
	if !conn.closed {
		conn.closed, conn.cause = true, cause
	}
	conn.mu.Unlock()
	conn.wake()
}

func (conn *SocketConn) Read(buffer []byte) (int, error) {
	for {
		conn.mu.Lock()
		if len(conn.buffered) > 0 {
			read := copy(buffer, conn.buffered[0])
			if conn.buffered[0] = conn.buffered[0][read:]; len(conn.buffered[0]) == 0 {
				conn.buffered = conn.buffered[1:]
			}
			conn.mu.Unlock()
			return read, nil
		}
		if conn.closed {
			cause := conn.cause
			conn.mu.Unlock()
			return 0, cause
		}
		if !conn.deadline.IsZero() && !time.Now().Before(conn.deadline) {
			conn.mu.Unlock()
			return 0, os.ErrDeadlineExceeded
		}
		conn.mu.Unlock()
		<-conn.arrived
	}
}

func (conn *SocketConn) Write(data []byte) (written int, err error) {
	conn.mu.Lock()
	closed := conn.closed
	conn.mu.Unlock()
	if closed {
		return 0, net.ErrClosed
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			written, err = 0, fmt.Errorf("worker: WebSocket send: %v", recovered)
		}
	}()
	conn.socket.Call("send", bytesToJS(data))
	return len(data), nil
}

// Close closes the socket, then removes and releases the listeners, so the
// runtime never calls a released function.
func (conn *SocketConn) Close() error {
	conn.mu.Lock()
	if !conn.closed {
		conn.closed, conn.cause = true, net.ErrClosed
	}
	events, listeners := conn.events, conn.listeners
	conn.events, conn.listeners = nil, nil
	if conn.timer != nil {
		conn.timer.Stop()
	}
	conn.mu.Unlock()
	conn.wake()
	if listeners == nil {
		return nil
	}
	func() {
		defer func() { _ = recover() }()
		conn.socket.Call("close", 1000, "closed")
	}()
	for index, listener := range listeners {
		conn.socket.Call("removeEventListener", events[index], listener)
		listener.Release()
	}
	return nil
}

type socketAddr struct{}

func (socketAddr) Network() string { return "websocket" }
func (socketAddr) String() string  { return "durable-object" }

func (conn *SocketConn) LocalAddr() net.Addr  { return socketAddr{} }
func (conn *SocketConn) RemoteAddr() net.Addr { return socketAddr{} }

func (conn *SocketConn) SetDeadline(deadline time.Time) error { return conn.SetReadDeadline(deadline) }

// SetReadDeadline wakes a blocked Read at deadline. Writes never block: a
// send only queues the message in the runtime.
func (conn *SocketConn) SetReadDeadline(deadline time.Time) error {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.deadline = deadline
	if conn.timer != nil {
		conn.timer.Stop()
		conn.timer = nil
	}
	if !deadline.IsZero() {
		conn.timer = time.AfterFunc(time.Until(deadline), conn.wake)
	}
	conn.wake()
	return nil
}

func (conn *SocketConn) SetWriteDeadline(time.Time) error { return nil }

var _ net.Conn = (*SocketConn)(nil)
