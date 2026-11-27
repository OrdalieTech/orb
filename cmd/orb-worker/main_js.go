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
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync/atomic"
	"syscall/js"
	"time"

	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	"github.com/OrdalieTech/orb/ai"
	"github.com/OrdalieTech/orb/ai/providers"
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
	// models resolves to the models a new session of this object can start on, its persisted
	// default and reasoning levels; given provider, model id and optional level, it persists that
	// default in settings.json first so /new sessions start on it.
	set("models", func(_ js.Value, args []js.Value) any {
		return promise(func() (any, error) {
			instance := opened.Load()
			if instance == nil {
				return nil, errors.New("the object's Orb is not open")
			}
			if len(args) >= 2 {
				level := ""
				if len(args) >= 3 && args[2].Type() == js.TypeString {
					level = args[2].String()
				}
				if err := instance.SetDefaultModel(context.Background(), args[0].String(), args[1].String(), level); err != nil {
					return nil, err
				}
			} else if !instance.HasModelStore(context.Background()) {
				// First model list before the background catalog fetch completed: wait briefly so
				// newly released models (models.dev + live Codex) appear immediately when opening Ctrl+M.
				ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
				_ = instance.RefreshModels(ctx, false)
				cancel()
			} else {
				go func() { _ = instance.RefreshModels(context.Background(), false) }()
			}
			out, err := instance.DefaultModels()
			if err != nil {
				return nil, err
			}
			raw, err := json.Marshal(out)
			return string(raw), err
		})
	})
	// accounts resolves to this object's synced provider accounts as JSON; given a provider and
	// account id, it selects that account first.
	set("accounts", func(_ js.Value, args []js.Value) any {
		return promise(func() (any, error) {
			instance := opened.Load()
			if instance == nil {
				return nil, errors.New("the object's Orb is not open")
			}
			ctx := context.Background()
			store := peer.Accounts(instance.Host.Store)
			if len(args) == 2 {
				if err := store.Select(ctx, args[0].String(), args[1].String()); err != nil {
					return nil, err
				}
			}
			rows, err := store.Accounts(ctx)
			if err != nil {
				return nil, err
			}
			type item struct {
				ID           string `json:"id"`
				Provider     string `json:"provider"`
				ProviderName string `json:"provider_name"`
				Name         string `json:"name"`
				Auth         string `json:"auth"`
				Active       bool   `json:"active"`
			}
			out := make([]item, 0, len(rows))
			for _, r := range rows {
				pName := r.Provider
				if def, ok := providers.Get(ai.ProviderID(r.Provider)); ok && def.Name != "" {
					pName = def.Name
				}
				out = append(out, item{ID: r.ID, Provider: r.Provider, ProviderName: pName, Name: r.Name, Auth: string(r.Type), Active: r.Active})
			}
			raw, err := json.Marshal(out)
			return string(raw), err
		})
	})
	// input resolves to the question the agent waits on, as JSON (null when none), with what its
	// controller draws (the questions plugin's questions); given its id and an answer, it replies.
	set("input", func(_ js.Value, args []js.Value) any {
		return promise(func() (any, error) {
			instance := opened.Load()
			if instance == nil {
				return nil, errors.New("the object's Orb is not open")
			}
			if len(args) == 2 {
				return "null", instance.Session().ReplyInput(args[0].String(), args[1].String())
			}
			raw, err := json.Marshal(instance.Session().PendingInput())
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
	mitosis.register(set, object.Get("emit"))
	if mitosis.dial { // a tab's model calls the browser refuses go through a device of its group
		http.DefaultTransport = peer.Relay(worker.FetchTransport(), mitosis.open)
	}
	return promise(func() (any, error) {
		ctx := context.Background()
		instance, err := worker.Open(ctx, worker.Options{
			KV: worker.NewDurableKV(object.Get("storage")), Env: lookup,
			Settings: document("ORB_SETTINGS"), Models: document("ORB_MODELS"),
			Tools: tools(peer.AgentCalls(mitosis.open), object.Get("run")),
			Extensions: func(settings *config.SettingsManager, store host.Store) map[string]extensions.Factory {
				portable := portable(settings)
				var memOpts memoryextension.Options
				if cfg := settings.GetPluginSettings("memory"); cfg != nil {
					switch v := cfg["viewBytes"].(type) {
					case int:
						memOpts.ViewBytes = v
					case int64:
						memOpts.ViewBytes = int(v)
					case float64:
						memOpts.ViewBytes = int(v)
					}
					if s, ok := cfg["compactorModel"].(string); ok {
						memOpts.CompactorModel = strings.TrimSpace(s)
					}
				}
				portable["memory"] = memoryextension.Extension(peer.Memory(store), memOpts)
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
		go func() { _ = instance.RefreshModels(ctx, false) }()
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

// tools adds code mode to calls where the host runs code: run(code, names, call) resolves to the
// run as JSON, {out, value, error}, as a browser tab's sandboxed worker answers it
// (platforms/browser/web). The code reaches the session's other tools, named in names, through
// call(name, args), which runs one as a nested call and resolves to {text, failed}.
func tools(calls worker.ToolsFunc, run js.Value) worker.ToolsFunc {
	if run.Type() != js.TypeFunction {
		return calls
	}
	code := extensions.ToolDefinition{
		Name: "js", Label: "js", PromptSnippet: "Run JavaScript in a sandboxed worker",
		Description: "Run JavaScript in a sandboxed worker of this browser tab, for computing, transforming data, trying code, or calling several tools in one go. " +
			"The code is the body of an async function: return a value, await promises, console.log to print. " +
			"`await tools.<name>(args)` calls your other tools with the arguments you would give them and resolves to their text output (a failed call throws). " +
			"It has no DOM and no access to the page or its storage; fetch obeys the browser's CORS rules. A run stops after 2 min.",
		Parameters: ai.JSONSchema(`{"type":"object","properties":{"code":{"type":"string","description":"The body of an async function."}},"required":["code"]}`),
		Execute: func(ctx context.Context, _ string, args any, _ engine.AgentToolUpdateCallback, session extensions.Context) (engine.AgentToolResult, error) {
			var input struct {
				Code string `json:"code"`
			}
			raw, _ := json.Marshal(args)
			if err := json.Unmarshal(raw, &input); err != nil || strings.TrimSpace(input.Code) == "" {
				return engine.AgentToolResult{}, errors.New("code must not be empty")
			}
			nested, _ := session.(extensions.ToolContext)
			names := []string{}
			if nested != nil {
				for _, tool := range nested.Tools() {
					if tool.Name != "js" {
						names = append(names, tool.Name)
					}
				}
			}
			call := js.FuncOf(func(_ js.Value, args []js.Value) any {
				name, raw := args[0].String(), args[1].String()
				return promise(func() (any, error) {
					var params any
					if !slices.Contains(names, name) {
						return nil, fmt.Errorf("no tool %q here", name)
					}
					if err := json.Unmarshal([]byte(raw), &params); err != nil {
						return nil, err
					}
					outcome := nested.ExecuteTool(ctx, name, params, nil)
					var text []string
					for _, part := range outcome.Result.Content {
						if t, ok := part.(*ai.TextContent); ok {
							text = append(text, t.Text)
						}
					}
					answer, err := json.Marshal(map[string]any{"text": strings.Join(text, "\n"), "failed": outcome.IsError})
					return string(answer), err
				})
			})
			defer call.Release()
			listed, _ := json.Marshal(names)
			answer, err := worker.Await(run.Invoke(input.Code, string(listed), call))
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
