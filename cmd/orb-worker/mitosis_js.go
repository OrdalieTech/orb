//go:build js && wasm

package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"sync"
	"syscall/js"

	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/view"
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
	intents  chan string // the view's, consumed from the first until close
	closed   bool
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

// app starts the view every Orb app draws (mitosis/view) over this peer, as `orb app` does over a
// machine's Mitosis, so a tab follows and starts the conversations of every device of its group.
// Its tabs and the names given to machines stay in the object's store.
func (b *mitosisPeer) app(emit func(any)) (*view.App, error) {
	b.mu.Lock()
	instance := b.instance
	b.mu.Unlock()
	if instance == nil {
		return nil, errors.New("orb-worker: the object's Orb is not open")
	}
	ctx := context.Background()
	tabs, names := instance.Host.Store.Document("app-tabs.json"), instance.Host.Store.Document("app-names.json")
	saved, _ := tabs.Read(ctx)
	named, _ := names.Read(ctx)
	keep := func(d document.Document) func([]byte) {
		return func(data []byte) { _ = document.Replace(ctx, d, data) }
	}
	return view.New(ctx, view.Options{
		Call: func(ctx context.Context, method string, params, result any) error {
			self, err := b.open(ctx)
			if err != nil {
				return err
			}
			raw, err := self.Admin(ctx, method, mitosis.JSON(params), "")
			if err == nil && result != nil && len(raw) > 0 {
				err = json.Unmarshal(raw, result)
			}
			return err
		},
		Run: func(context.Context, ...string) (string, error) {
			return "", errors.New("a browser tab runs no orb command")
		},
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("a browser tab has no files to send") },
		WriteFile: func(name string, data []byte) (string, error) {
			clean := strings.TrimSpace(strings.ReplaceAll(name, "/", "_"))
			if clean == "" || clean == "." || clean == ".." {
				clean = "attachment"
			}
			rel := "attachments/" + clean
			if err := instance.Host.FS.CreateDir(ctx, worker.Workspace+"/attachments", true); err != nil {
				return "", err
			}
			if err := instance.Host.FS.WriteFile(ctx, worker.Workspace+"/"+rel, data); err != nil {
				return "", err
			}
			return rel, nil
		},
		Tabs:     saved, SaveTabs: keep(tabs),
		Names: named, SaveNames: keep(names),
		Name: "this tab", Emit: emit,
	}), nil
}

// close ends the peer with the object's Orb: its channels close and it dials no more.
func (b *mitosisPeer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.instance, b.closed = nil, true
	if b.intents != nil {
		close(b.intents)
		b.intents = nil
	}
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
//	app(intentJSON)                             one intent of the view (mitosisPeer.app), applied in
//	                                            order; a string reports a refusal. What the view sends
//	                                            comes out through emit beside the RPC frames, told
//	                                            apart by its "t".
func (b *mitosisPeer) register(set func(string, func(js.Value, []js.Value) any), emit js.Value) {
	set("app", func(_ js.Value, args []js.Value) any { return b.intent(args[0].String(), emit) })
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

// intent queues one intent of the view. The first starts the goroutine that applies them in
// order, so an object that never serves the page runs none; close ends it with the Orb.
func (b *mitosisPeer) intent(line string, emit js.Value) any {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return "this Orb is closed"
	}
	if b.intents == nil {
		b.intents = make(chan string, 256)
		go func(intents <-chan string) {
			out := &frameWriter{emit: emit}
			var app *view.App
			for line := range intents {
				if app == nil {
					var err error
					if app, err = b.app(func(m any) { raw, _ := json.Marshal(m); out.emitFrame(append(raw, '\n')) }); err != nil {
						_, _ = os.Stderr.WriteString("app: " + err.Error() + "\n")
						continue
					}
				}
				app.Do([]byte(line))
			}
		}(b.intents)
	}
	select {
	case b.intents <- line:
		return nil
	default:
		return "app queue is full"
	}
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
