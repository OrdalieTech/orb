//go:build js && wasm

package main

import (
	"context"
	"os"
	"sync"
	"syscall/js"

	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/platforms/worker/peer"
)

// bridgePeer opens the object's Bridge peer on first use: the first stream a
// paired Orb opens, the first admin action, or the agent's first bridge_call.
type bridgePeer struct {
	name     string
	mu       sync.Mutex
	instance *worker.Instance
	self     *peer.Peer
}

func (b *bridgePeer) open(context.Context) (*peer.Peer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.self != nil {
		return b.self, nil
	}
	self, err := peer.Open(b.instance, b.name)
	if err == nil {
		b.self = self
	}
	return self, err
}

// register adds the shim's Bridge entry points to api:
//
//	bridgeAccept(socket)                       serve an accepted, non-hibernatable WebSocket
//	bridgeAdmin(method, paramsJSON, locator)   Promise<resultJSON>, the owner's admin call
func (b *bridgePeer) register(api js.Value) {
	api.Set("bridgeAccept", js.FuncOf(func(_ js.Value, args []js.Value) any {
		// The listeners must exist before this JavaScript turn ends.
		conn := worker.NewSocketConn(args[0])
		go func() {
			self, err := b.open(context.Background())
			if err != nil {
				_, _ = os.Stderr.WriteString("bridge: " + err.Error() + "\n")
				_ = conn.Close()
				return
			}
			if err = self.Serve(context.Background(), conn); err != nil {
				_, _ = os.Stderr.WriteString("bridge: stream: " + err.Error() + "\n")
				_ = conn.Close()
			}
		}()
		return nil
	}))
	api.Set("bridgeAdmin", js.FuncOf(func(_ js.Value, args []js.Value) any {
		method, params, locator := args[0].String(), args[1].String(), args[2].String()
		return promise(func() (string, error) {
			self, err := b.open(context.Background())
			if err != nil {
				return "", err
			}
			result, err := self.Admin(context.Background(), method, []byte(params), locator)
			return string(result), err
		})
	}))
}

// promise runs work on a goroutine and settles a JavaScript promise with it.
func promise(work func() (string, error)) js.Value {
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
