//go:build js && wasm

package websocket

import (
	"context"
	"errors"
	"net"
	"syscall/js"
)

// dial wraps the browser's WebSocket in SocketConn instead of coder/websocket, whose
// browser client deadlocks every JavaScript callback when an error event arrives while its
// Close awaits the close event.
func dial(ctx context.Context, address string) (net.Conn, error) {
	socket := js.Global().Get("WebSocket").New(address)
	conn := NewSocketConn(socket)
	settled := make(chan error, 1)
	listeners := map[string]js.Func{}
	for event, err := range map[string]error{"open": nil, "close": errors.New("websocket: connection failed")} {
		listeners[event] = js.FuncOf(func(js.Value, []js.Value) any {
			select {
			case settled <- err:
			default:
			}
			return nil
		})
		socket.Call("addEventListener", event, listeners[event])
	}
	defer func() {
		for event, listener := range listeners {
			socket.Call("removeEventListener", event, listener)
			listener.Release()
		}
	}()
	select {
	case err := <-settled:
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		return conn, nil
	case <-ctx.Done():
		_ = conn.Close()
		return nil, ctx.Err()
	}
}
