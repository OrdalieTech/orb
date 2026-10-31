//go:build !js || !wasm

package websocket

import (
	"context"
	"net"

	"github.com/coder/websocket"
)

func dial(ctx context.Context, address string) (net.Conn, error) {
	c, _, err := websocket.Dial(ctx, address, nil)
	if err != nil {
		return nil, err
	}
	// Dial's context bounds establishment; the returned stream lives until Close.
	stream := websocket.NetConn(context.Background(), c, websocket.MessageBinary)
	c.SetReadLimit(64 << 10) // TLS records, not whole RPC frames.
	return stream, nil
}
