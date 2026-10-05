//go:build js && wasm

// orb-worker is Orb inside Durable Objects. The JavaScript shim
// (platforms/worker/deploy/worker.mjs) instantiates it once per isolate and
// names, in argv[1], a one-shot global holding its fetch helpers and the
// promise callbacks this program settles with the runtime's entry point:
// open starts one object's Orb, over the object's own storage, env bindings,
// frame sink and exit callback, in this shared runtime.
package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"sync/atomic"
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
	// The live heap is a few megabytes per object, so collecting at three
	// times it rather than twice saves a tenth of a turn's CPU; the soft
	// limit keeps the isolate's heap inside its 128 MB.
	debug.SetGCPercent(200)
	debug.SetMemoryLimit(96 << 20)
	boot := js.Global().Get(os.Args[1])
	js.Global().Delete(os.Args[1])
	http.DefaultTransport = worker.FetchTransport(boot.Get("fetch"))
	api := js.Global().Get("Object").New()
	api.Set("open", js.FuncOf(func(_ js.Value, args []js.Value) any { return open(args[0]) }))
	boot.Call("resolve", api)
	select {}
}

// open starts one object's Orb from {storage, env, name, emit, exit} and
// returns a promise of the object's entry points:
//
//	command(frame)  queue one command frame; a string reports a refusal
//	stats()         this runtime's memory statistics
//	dispose()       end the object's session once the shim drops the object;
//	                exit(code) follows and the entry points are released
//
// and the Bridge ones (bridgePeer.register).
func open(object js.Value) js.Value {
	env := object.Get("env")
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
	var funcs []js.Func
	set := func(name string, fn func(js.Value, []js.Value) any) {
		funcs = append(funcs, js.FuncOf(fn))
		api.Set(name, funcs[len(funcs)-1])
	}
	var stopped, disposed atomic.Bool
	set("command", func(_ js.Value, args []js.Value) any {
		if stopped.Load() || disposed.Load() {
			return "the object's Orb has stopped"
		}
		if len(args) != 1 || args[0].Type() != js.TypeString {
			return "expected one command frame"
		}
		select {
		case commands <- args[0].String():
			return nil
		default:
			return "command queue is full"
		}
	})
	set("stats", func(js.Value, []js.Value) any {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		return map[string]any{
			"heapAlloc": memory.HeapAlloc, "heapSys": memory.HeapSys, "sys": memory.Sys,
			"totalAlloc": memory.TotalAlloc, "numGC": memory.NumGC, "goroutines": runtime.NumGoroutine(),
		}
	})
	set("dispose", func(js.Value, []js.Value) any {
		if !disposed.Swap(true) {
			close(commands)
		}
		return nil
	})
	bridge := &bridgePeer{}
	if name := object.Get("name"); name.Type() == js.TypeString {
		bridge.name = name.String()
	}
	bridge.register(set)
	return promise(func() (any, error) {
		ctx := context.Background()
		instance, err := worker.Open(ctx, worker.Options{
			KV: worker.NewDurableKV(object.Get("storage")), Env: lookup,
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
			for _, fn := range funcs {
				fn.Release()
			}
			return nil, err
		}
		bridge.mu.Lock()
		bridge.instance = instance
		bridge.mu.Unlock()
		go func() {
			code := instance.Serve(ctx, &commandReader{commands: commands}, &frameWriter{emit: object.Get("emit")}, os.Stderr)
			stopped.Store(true)
			bridge.mu.Lock()
			bridge.instance = nil
			bridge.mu.Unlock()
			// Only a disposed object is past calling its entry points; one that
			// stopped by itself refuses commands until the shim boots it again.
			if disposed.Load() {
				for _, fn := range funcs {
					fn.Release()
				}
			}
			object.Call("exit", code)
		}()
		return api, nil
	})
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

// frameWriter hands each complete output frame, LF included, to the shim in
// one Uint8Array it reuses; the shim copies out the frame's bytes.
type frameWriter struct {
	emit    js.Value
	array   js.Value
	size    int
	partial []byte
}

var uint8Array = js.Global().Get("Uint8Array")

func (writer *frameWriter) emitFrame(frame []byte) {
	if len(frame) > writer.size {
		writer.size = max(len(frame), 2*writer.size, 1<<14)
		writer.array = uint8Array.New(writer.size)
	}
	js.CopyBytesToJS(writer.array, frame)
	writer.emit.Invoke(writer.array, len(frame))
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
