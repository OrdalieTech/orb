//go:build js && wasm

// orb-worker is Orb inside Durable Objects. The JavaScript shim
// (platforms/worker/deploy/worker.mjs) instantiates it once per isolate and
// names, in argv[1], a one-shot global holding its helpers (worker.Bind) and
// the promise callbacks this program settles with the runtime's entry point:
// open starts one object's Orb, over the object's own storage, env bindings,
// frame sink and exit callback, in this shared runtime.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"strings"
	"sync/atomic"
	"syscall/js"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/engine"
	"github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/platforms/worker/peer"
	memoryextension "github.com/OrdalieTech/orb/plugins/memory/extension"
	"github.com/OrdalieTech/orb/plugins/memtree"
	"github.com/OrdalieTech/orb/plugins/questions"
	"github.com/OrdalieTech/orb/plugins/tasks"
	"github.com/OrdalieTech/orb/plugins/titles"
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
	worker.Bind(boot.Get("helpers"))
	http.DefaultTransport = worker.FetchTransport()
	api := js.Global().Get("Object").New()
	api.Set("open", js.FuncOf(func(_ js.Value, args []js.Value) any { return open(args[0]) }))
	boot.Call("resolve", api)
	select {}
}

// open starts one object's Orb from {storage, env, name, emit, exit, dial} and
// returns a promise of the object's entry points:
//
//	command(frame)  queue one command frame; a string reports a refusal
//	stats()         this runtime's memory statistics
//	dispose()       end the object's session once the shim drops the object;
//	                exit(code) follows and the entry points are released
//
// and the Mitosis ones (mitosisPeer.register).
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
	var opened atomic.Pointer[worker.Instance]
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
	// plugins resolves to which of the plugins an object can run are on, as JSON; given a name and a
	// state, it sets that one first, for the sessions started after.
	set("plugins", func(_ js.Value, args []js.Value) any {
		return promise(func() (any, error) {
			instance := opened.Load()
			if instance == nil {
				return nil, errors.New("the object's Orb is not open")
			}
			settings, err := instance.Settings()
			if err != nil {
				return nil, err
			}
			names := portable(settings)
			if len(args) == 2 {
				if _, ok := names[args[0].String()]; !ok {
					return nil, errors.New("no such plugin here")
				}
				settings.SetPluginEnabled(args[0].String(), args[1].Bool())
				if errs := settings.DrainErrors(); len(errs) > 0 {
					return nil, errs[0].Err
				}
			}
			on, state := settings.GetPlugins(), map[string]bool{}
			for name := range names {
				state[name] = on[name]
			}
			raw, err := json.Marshal(state)
			return string(raw), err
		})
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
	// dial: a browser tab, which reaches its peers itself, where a Durable Object only accepts.
	mitosis := &mitosisPeer{dial: object.Get("dial").Truthy()}
	if name := object.Get("name"); name.Type() == js.TypeString {
		mitosis.name = name.String()
	}
	mitosis.register(set)
	return promise(func() (any, error) {
		ctx := context.Background()
		instance, err := worker.Open(ctx, worker.Options{
			KV: worker.NewDurableKV(object.Get("storage")), Env: lookup,
			Settings: document("ORB_SETTINGS"), Models: document("ORB_MODELS"),
			Tools: tools(peer.AgentCalls(mitosis.open), object.Get("run")),
			Extensions: func(settings *config.SettingsManager, store host.Store) map[string]extensions.Factory {
				portable := portable(settings)
				portable["memory"] = memoryextension.Extension(peer.Memory(store))
				chosen := map[string]extensions.Factory{}
				for name, on := range settings.GetPlugins() {
					if on && portable[name] != nil {
						chosen["builtin:"+name] = portable[name]
					}
				}
				return chosen
			},
		})
		if err != nil {
			for _, fn := range funcs {
				fn.Release()
			}
			return nil, err
		}
		opened.Store(instance)
		mitosis.mu.Lock()
		mitosis.instance = instance
		mitosis.mu.Unlock()
		go func() {
			code := instance.Serve(ctx, &commandReader{commands: commands}, &frameWriter{emit: object.Get("emit")}, os.Stderr)
			stopped.Store(true)
			mitosis.close()
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

// portable is the bundled plugins an object runs, as its settings enable them: those that need no
// processes, files of their own or terminal (docs/plugins.md, where plugins run). Memory, over the
// object's store, is wired where the store is known; its name is here so it can be listed and set.
func portable(settings *config.SettingsManager) map[string]extensions.Factory {
	return map[string]extensions.Factory{
		"titles": titles.Extension(), "tasks": tasks.Extension(tasks.Draw{}), "questions": questions.Extension(questions.Draw{}),
		"memtree": memtree.Extension(memtree.OptionsFrom(settings.GetPluginSettings("memtree"))),
		"memory":  nil,
	}
}

// tools adds code mode to calls where the host runs code: run(code) resolves to the run as JSON,
// {out, value, error}, as a browser tab's sandboxed worker answers it (platforms/browser/web).
func tools(calls worker.ToolsFunc, run js.Value) worker.ToolsFunc {
	if run.Type() != js.TypeFunction {
		return calls
	}
	code := extensions.ToolDefinition{
		Name: "js", Label: "js", PromptSnippet: "Run JavaScript in a sandboxed worker",
		Description: "Run JavaScript in a sandboxed worker of this browser tab, for computing, transforming data or trying code. " +
			"The code is the body of an async function: return a value, await promises, console.log to print. " +
			"It has no DOM and no access to the page, its storage or the files; fetch obeys the browser's CORS rules. A run stops after 30 s.",
		Parameters: ai.JSONSchema(`{"type":"object","properties":{"code":{"type":"string","description":"The body of an async function."}},"required":["code"]}`),
		Execute: func(_ context.Context, _ string, args any, _ engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			var input struct {
				Code string `json:"code"`
			}
			raw, _ := json.Marshal(args)
			if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Code) == "" {
				return engine.AgentToolResult{}, errors.New("code must not be empty")
			}
			answer, err := worker.Await(run.Invoke(input.Code))
			if err != nil {
				return engine.AgentToolResult{}, err
			}
			var result struct {
				Out   []string `json:"out"`
				Value *string  `json:"value"`
				Error string   `json:"error"`
			}
			if err := json.Unmarshal([]byte(answer.String()), &result); err != nil {
				return engine.AgentToolResult{}, err
			}
			lines := result.Out
			if result.Value != nil {
				lines = append(lines, "→ "+*result.Value)
			}
			if result.Error != "" {
				lines = append(lines, result.Error)
			}
			text := strings.Join(lines, "\n")
			if text == "" {
				text = "(no output)"
			}
			if len(text) > 50_000 {
				text = text[:50_000] + "\n… (cut at 50 KB)"
			}
			return engine.AgentToolResult{Content: ai.ToolResultContent{&ai.TextContent{Text: text}}, IsError: result.Error != ""}, nil
		},
	}
	return func(settings *config.SettingsManager) []extensions.ToolDefinition {
		return append(calls(settings), code)
	}
}
