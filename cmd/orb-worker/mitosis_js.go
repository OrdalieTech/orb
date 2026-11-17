//go:build js && wasm

package main

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"syscall/js"

	"github.com/OrdalieTech/orb/platforms/websocket"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/platforms/worker/peer"
)

// mitosisPeer opens the object's Mitosis peer on first use: the first stream a
// paired Orb opens, the first admin action, or the agent's first mitosis_call.
type mitosisPeer struct {
	name     string
	dial     bool
	mu       sync.Mutex
	instance *worker.Instance
	self     *peer.Peer
	stop     context.CancelFunc
}

func (b *mitosisPeer) open(context.Context) (*peer.Peer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.self != nil {
		return b.self, nil
	}
	if b.instance == nil {
		return nil, errors.New("orb-worker: the object's Orb has stopped")
	}
	self, err := peer.Open(b.instance, b.name)
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	b.self, b.stop = self, stop
	if !b.dial {
		go self.Follow(ctx)
		return self, nil
	}
	self.Dial(ctx, func(ctx context.Context, _, locator string) (net.Conn, error) {
		if !strings.HasPrefix(locator, "ws://") && !strings.HasPrefix(locator, "wss://") {
			return nil, errors.New("orb-worker: a browser reaches a peer only at a ws:// or wss:// locator")
		}
		return websocket.Dial(ctx, locator)
	})
	return self, nil
}

// close ends the peer with the object's Orb: its channels close and it dials no more.
func (b *mitosisPeer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instance = nil
	if b.self != nil {
		b.stop()
		_ = b.self.Close()
		b.self = nil
	}
}

// register adds the shim's Mitosis entry points through set:
//
//	mitosisAccept(socket)                       serve an accepted, non-hibernatable WebSocket
//	mitosisAdmin(method, paramsJSON, locator)   Promise<resultJSON>, the owner's admin call
func (b *mitosisPeer) register(set func(string, func(js.Value, []js.Value) any)) {
	set("mitosisAccept", func(_ js.Value, args []js.Value) any {
		// The listeners must exist before this JavaScript turn ends.
		conn := websocket.NewSocketConn(args[0])
		go func() {
			self, err := b.open(context.Background())
			if err != nil {
				_, _ = os.Stderr.WriteString("mitosis: " + err.Error() + "\n")
				_ = conn.Close()
				return
			}
			if err = self.Serve(context.Background(), conn); err != nil {
				_, _ = os.Stderr.WriteString("mitosis: stream: " + err.Error() + "\n")
				_ = conn.Close()
			}
		}()
		return nil
	})
	set("mitosisAdmin", func(_ js.Value, args []js.Value) any {
		method, params, locator := args[0].String(), args[1].String(), args[2].String()
		return promise(func() (any, error) {
			self, err := b.open(context.Background())
			if err != nil {
				return nil, err
			}
			result, err := self.Admin(context.Background(), method, []byte(params), locator)
			return string(result), err
		})
	})
}

// promise runs work on a goroutine and settles a JavaScript promise with it.
func promise(work func() (any, error)) js.Value {
	var executor js.Func
	executor = js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		executor.Release()
		go func() {
			result, err := work()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(result)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}
