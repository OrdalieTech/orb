//go:build js && wasm

// orb-worker is Orb inside a Durable Object. The JavaScript shim
// (platforms/worker/deploy/worker.mjs) instantiates it once per object and
// names, in argv[1], a one-shot global holding the object's storage, its env
// bindings, a frame sink and the promise callbacks this program settles.
package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"runtime"
	"syscall/js"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/platforms/worker/peer"
	"github.com/OrdalieTech/orb/plugins/memtree"
)

func main() {
	if len(os.Args) < 2 {
		panic("orb-worker: the shim must name its boot slot in argv[1]")
	}
	boot := js.Global().Get(os.Args[1])
	js.Global().Delete(os.Args[1])
	env := boot.Get("env")
	lookup := func(name string) (string, bool) {
		switch value := env.Get(name); value.Type() {
		case js.TypeString:
			return value.String(), true
		case js.TypeObject:
			// wrangler.jsonc vars may be JSON values.
			return js.Global().Get("JSON").Call("stringify", value).String(), true
		default:
			return "", false
		}
	}
	document := func(name string) []byte {
		if value, ok := lookup(name); ok {
			return []byte(value)
		}
		return nil
	}

	commands := make(chan string, 256)
	api := js.Global().Get("Object").New()
	api.Set("command", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return "expected one command frame"
		}
		select {
		case commands <- args[0].String():
			return nil
		default:
			return "command queue is full"
		}
	}))
	api.Set("stats", js.FuncOf(func(js.Value, []js.Value) any {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		return map[string]any{
			"heapAlloc": memory.HeapAlloc, "heapSys": memory.HeapSys, "sys": memory.Sys,
			"totalAlloc": memory.TotalAlloc, "numGC": memory.NumGC, "goroutines": runtime.NumGoroutine(),
		}
	}))

	bridge := &bridgePeer{}
	if name := boot.Get("name"); name.Type() == js.TypeString {
		bridge.name = name.String()
	}
	bridge.register(api)
	go func() {
		ctx := context.Background()
		instance, err := worker.Open(ctx, worker.Options{
			KV: worker.NewDurableKV(boot.Get("storage")), Env: lookup,
			Settings: document("ORB_SETTINGS"), Models: document("ORB_MODELS"),
			Tools: peer.AgentCalls(bridge.open),
			Extensions: func(settings *config.SettingsManager) map[string]extensions.Factory {
				if !settings.GetPlugins()["memtree"] {
					return nil
				}
				return map[string]extensions.Factory{"builtin:memtree": memtree.Extension(memtree.OptionsFrom(settings.GetPluginSettings("memtree")))}
			},
		})
		if err != nil {
			boot.Call("reject", err.Error())
			return
		}
		bridge.mu.Lock()
		bridge.instance = instance
		bridge.mu.Unlock()
		boot.Call("resolve", api)
		code := instance.Serve(ctx, &commandReader{commands: commands}, &frameWriter{emit: boot.Get("emit")}, os.Stderr)
		boot.Call("exit", code)
	}()
	select {}
}

// commandReader turns queued command frames into the LF-delimited stream
// agent/rpc reads.
type commandReader struct {
	commands <-chan string
	pending  []byte
}

func (reader *commandReader) Read(buffer []byte) (int, error) {
	if len(reader.pending) == 0 {
		command, open := <-reader.commands
		if !open {
			return 0, io.EOF
		}
		reader.pending = append([]byte(command), '\n')
	}
	written := copy(buffer, reader.pending)
	reader.pending = reader.pending[written:]
	return written, nil
}

// frameWriter hands each complete output frame, LF included, to the shim as
// a Uint8Array, which it streams as it is.
type frameWriter struct {
	emit    js.Value
	partial []byte
}

var uint8Array = js.Global().Get("Uint8Array")

func (writer *frameWriter) emitFrame(frame []byte) {
	array := uint8Array.New(len(frame))
	js.CopyBytesToJS(array, frame)
	writer.emit.Invoke(array)
}

func (writer *frameWriter) Write(data []byte) (int, error) {
	// agent/rpc writes each frame whole, LF included.
	if end := bytes.IndexByte(data, '\n'); len(writer.partial) == 0 && end == len(data)-1 {
		writer.emitFrame(data)
		return len(data), nil
	}
	writer.partial = append(writer.partial, data...)
	for {
		end := bytes.IndexByte(writer.partial, '\n')
		if end < 0 {
			return len(data), nil
		}
		writer.emitFrame(writer.partial[:end+1])
		writer.partial = writer.partial[end+1:]
	}
}
