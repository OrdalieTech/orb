package jsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/grafana/sobek"
)

func newExtensionAPI(runtime *sobek.Runtime, vm *runtimeVM, api extensions.API) (*sobek.Object, error) {
	object := runtime.NewObject()
	methods := map[string]any{
		"on": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerHandler(runtime, vm, api, extensions.EventType(call.Argument(0).String()), call.Argument(1)))
			return sobek.Undefined()
		},
		"registerTool": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerTool(runtime, vm, api, call.Argument(0)))
			return sobek.Undefined()
		},
		"registerCommand": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerCommand(runtime, vm, api, call.Argument(0).String(), call.Argument(1)))
			return sobek.Undefined()
		},
		"registerShortcut": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerShortcut(runtime, vm, api, call.Argument(0).String(), call.Argument(1)))
			return sobek.Undefined()
		},
		"registerFlag": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerFlag(runtime, api, call.Argument(0).String(), call.Argument(1)))
			return sobek.Undefined()
		},
		"getFlag": func(call sobek.FunctionCall) sobek.Value {
			value, ok := api.GetFlag(call.Argument(0).String())
			if !ok {
				return sobek.Undefined()
			}
			return runtime.ToValue(value)
		},
		"registerMessageRenderer": func(call sobek.FunctionCall) sobek.Value {
			renderer, ok := sobek.AssertFunction(call.Argument(1))
			if !ok {
				panic(runtime.NewTypeError("message renderer is not a function"))
			}
			api.RegisterMessageRenderer(call.Argument(0).String(), func(message extensions.CustomMessage, options extensions.MessageRenderOptions, theme extensions.Theme) extensions.Component {
				return renderComponent(vm, renderer, func(runtime *sobek.Runtime) []sobek.Value {
					return []sobek.Value{toJS(runtime, message), renderOptionsValue(runtime, options.Expanded), themeValue(runtime, vm, theme)}
				})
			})
			return sobek.Undefined()
		},
		"registerEntryRenderer": func(call sobek.FunctionCall) sobek.Value {
			renderer, ok := sobek.AssertFunction(call.Argument(1))
			if !ok {
				panic(runtime.NewTypeError("entry renderer is not a function"))
			}
			api.RegisterEntryRenderer(call.Argument(0).String(), func(entry any, options extensions.EntryRenderOptions, theme extensions.Theme) extensions.Component {
				return renderComponent(vm, renderer, func(runtime *sobek.Runtime) []sobek.Value {
					return []sobek.Value{toJS(runtime, entry), renderOptionsValue(runtime, options.Expanded), themeValue(runtime, vm, theme)}
				})
			})
			return sobek.Undefined()
		},
		"sendMessage": func(call sobek.FunctionCall) sobek.Value {
			var message extensions.CustomMessage
			must(runtime, decodeJSON(runtime, call.Argument(0), &message))
			options := decodeSendMessageOptions(runtime, call.Argument(1))
			must(runtime, api.SendMessage(vm.context(), message, options))
			return sobek.Undefined()
		},
		"sendUserMessage": func(call sobek.FunctionCall) sobek.Value {
			content, err := decodeUserContent(runtime, call.Argument(0))
			must(runtime, err)
			options := decodeSendUserMessageOptions(runtime, call.Argument(1))
			must(runtime, api.SendUserMessage(vm.context(), content, options))
			return sobek.Undefined()
		},
		"appendEntry": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, api.AppendEntry(vm.context(), call.Argument(0).String(), call.Argument(1).Export()))
			return sobek.Undefined()
		},
		"setSessionName": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, api.SetSessionName(vm.context(), call.Argument(0).String()))
			return sobek.Undefined()
		},
		"getSessionName": func(sobek.FunctionCall) sobek.Value {
			name, err := api.GetSessionName(vm.context())
			must(runtime, err)
			if name == nil {
				return sobek.Undefined()
			}
			return runtime.ToValue(*name)
		},
		"setLabel": func(call sobek.FunctionCall) sobek.Value {
			var label *string
			if present(call.Argument(1)) {
				value := call.Argument(1).String()
				label = &value
			}
			must(runtime, api.SetLabel(vm.context(), call.Argument(0).String(), label))
			return sobek.Undefined()
		},
		"exec": func(call sobek.FunctionCall) sobek.Value {
			command := call.Argument(0).String()
			var args []string
			must(runtime, decodeJSON(runtime, call.Argument(1), &args))
			options := decodeExecOptions(runtime, vm, call.Argument(2))
			ctx := vm.context()
			if options.Context != nil {
				ctx = options.Context
			}
			return vm.promise(runtime, ctx, func(ctx context.Context) (any, error) {
				return api.Exec(ctx, command, args, options)
			})
		},
		"getActiveTools": func(sobek.FunctionCall) sobek.Value {
			tools, err := api.GetActiveTools()
			must(runtime, err)
			return runtime.ToValue(tools)
		},
		"getAllTools": func(sobek.FunctionCall) sobek.Value {
			tools, err := api.GetAllTools()
			must(runtime, err)
			return toJS(runtime, tools)
		},
		"setActiveTools": func(call sobek.FunctionCall) sobek.Value {
			var names []string
			must(runtime, decodeJSON(runtime, call.Argument(0), &names))
			must(runtime, api.SetActiveTools(names))
			return sobek.Undefined()
		},
		"getCommands": func(sobek.FunctionCall) sobek.Value {
			commands, err := api.GetCommands()
			must(runtime, err)
			return toJS(runtime, commands)
		},
		"setModel": func(call sobek.FunctionCall) sobek.Value {
			var model ai.Model
			must(runtime, decodeJSON(runtime, call.Argument(0), &model))
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return api.SetModel(ctx, &model)
			})
		},
		"getThinkingLevel": func(sobek.FunctionCall) sobek.Value {
			level, err := api.GetThinkingLevel()
			must(runtime, err)
			return runtime.ToValue(string(level))
		},
		"setThinkingLevel": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, api.SetThinkingLevel(agent.ThinkingLevel(call.Argument(0).String())))
			return sobek.Undefined()
		},
		"registerProvider": func(call sobek.FunctionCall) sobek.Value {
			must(runtime, registerProvider(runtime, vm, api, call.Argument(0), call.Argument(1)))
			return sobek.Undefined()
		},
		"unregisterProvider": func(call sobek.FunctionCall) sobek.Value {
			name := call.Argument(0).String()
			must(runtime, callProviderRegistration(runtime, vm, func() { api.UnregisterProvider(name) }))
			return sobek.Undefined()
		},
	}
	for name, method := range methods {
		if err := object.Set(name, method); err != nil {
			return nil, err
		}
	}
	events, err := newEventsObject(runtime, vm, api.Events())
	if err != nil {
		return nil, err
	}
	if err := object.Set("events", events); err != nil {
		return nil, err
	}
	return object, nil
}

func must(runtime *sobek.Runtime, err error) {
	if err != nil {
		panic(runtime.NewGoError(err))
	}
}

// renderComponent invokes a JS message/entry renderer and bridges the
// returned component (upstream renderers run synchronously).
func renderComponent(vm *runtimeVM, renderer sobek.Callable, arguments func(*sobek.Runtime) []sobek.Value) extensions.Component {
	value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		result, err := renderer(sobek.Undefined(), arguments(runtime)...)
		if err != nil {
			return nil, err
		}
		if !present(result) {
			return nil, nil
		}
		return decodeJSComponent(runtime, vm, result)
	})
	if err != nil {
		return nil
	}
	component, _ := value.(extensions.Component)
	return component
}

func renderOptionsValue(runtime *sobek.Runtime, expanded bool) sobek.Value {
	object := runtime.NewObject()
	must(runtime, object.Set("expanded", expanded))
	return object
}

func present(value sobek.Value) bool {
	return value != nil && !sobek.IsUndefined(value) && !sobek.IsNull(value)
}

func toJS(runtime *sobek.Runtime, value any) sobek.Value {
	result, err := plainJS(runtime, value)
	must(runtime, err)
	return result
}

func plainJS(runtime *sobek.Runtime, value any) (sobek.Value, error) {
	wired, err := wireValue(value)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(wired)
	if err != nil {
		return nil, err
	}
	parse, ok := sobek.AssertFunction(runtime.Get("JSON").ToObject(runtime).Get("parse"))
	if !ok {
		return nil, fmt.Errorf("JSON.parse is unavailable")
	}
	return parse(sobek.Undefined(), runtime.ToValue(string(encoded)))
}

func decodeSendMessageOptions(runtime *sobek.Runtime, value sobek.Value) *extensions.SendMessageOptions {
	if !present(value) {
		return nil
	}
	var options extensions.SendMessageOptions
	must(runtime, decodeJSON(runtime, value, &options))
	return &options
}

func decodeSendUserMessageOptions(runtime *sobek.Runtime, value sobek.Value) *extensions.SendUserMessageOptions {
	if !present(value) {
		return nil
	}
	var options extensions.SendUserMessageOptions
	must(runtime, decodeJSON(runtime, value, &options))
	return &options
}

func decodeUserContent(runtime *sobek.Runtime, value sobek.Value) (ai.UserContent, error) {
	if !present(value) || value.ExportType() == nil {
		return ai.UserContent{}, fmt.Errorf("user content must be a string or content block array")
	}
	if value.ExportType().Kind() == reflect.String {
		return ai.NewUserText(value.String()), nil
	}
	encoded, err := stringifyJSON(runtime, value)
	if err != nil {
		return ai.UserContent{}, err
	}
	var content ai.UserContentBlocks
	if err := json.Unmarshal(encoded, &content); err != nil {
		return ai.UserContent{}, err
	}
	return ai.NewUserContent(content...), nil
}

func decodeExecOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.ExecOptions {
	options := &extensions.ExecOptions{}
	if !present(value) {
		return options
	}
	object := value.ToObject(runtime)
	if cwd := object.Get("cwd"); present(cwd) {
		options.CWD = cwd.String()
	}
	if timeout := object.Get("timeout"); present(timeout) {
		options.Timeout = timeout.ToInteger()
	}
	if signal := object.Get("signal"); present(signal) {
		if signalObject, ok := signal.(*sobek.Object); ok {
			options.Context = vm.signals[signalObject]
		}
	}
	return options
}

func newEventsObject(runtime *sobek.Runtime, vm *runtimeVM, bus extensions.EventBus) (*sobek.Object, error) {
	object := runtime.NewObject()
	if err := object.Set("on", func(call sobek.FunctionCall) sobek.Value {
		channel := call.Argument(0).String()
		handler, ok := sobek.AssertFunction(call.Argument(1))
		if !ok {
			panic(runtime.NewTypeError("event bus handler for %q is not a function", channel))
		}
		vm.nextEventID++
		id := vm.nextEventID
		vm.eventHandlers[channel] = append(vm.eventHandlers[channel], vmEventHandler{id: id, handler: handler})
		unsubscribe := bus.On(channel, func(ctx context.Context, data any) error {
			envelope, wrapped := data.(vmEventEnvelope)
			if wrapped && envelope.source == vm {
				return nil
			}
			if wrapped {
				data = envelope.data
			}
			_, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
				result, err := handler(sobek.Undefined(), toJS(runtime, data))
				if err != nil {
					return nil, nil
				}
				if _, promise := result.Export().(*sobek.Promise); promise {
					vm.postWithContext(ctx, func(runtime *sobek.Runtime) error {
						_, _ = vm.awaitValue(ctx, runtime, result)
						return nil
					})
				}
				return nil, nil
			})
			return err
		})
		return runtime.ToValue(func(sobek.FunctionCall) sobek.Value {
			handlers := vm.eventHandlers[channel]
			for index := range handlers {
				if handlers[index].id == id {
					handlers = append(handlers[:index], handlers[index+1:]...)
					break
				}
			}
			if len(handlers) == 0 {
				delete(vm.eventHandlers, channel)
			} else {
				vm.eventHandlers[channel] = handlers
			}
			unsubscribe()
			return sobek.Undefined()
		})
	}); err != nil {
		return nil, err
	}
	if err := object.Set("emit", func(call sobek.FunctionCall) sobek.Value {
		channel := call.Argument(0).String()
		data := call.Argument(1)
		for _, registered := range vm.eventHandlers[channel] {
			result, err := registered.handler(sobek.Undefined(), data)
			if err != nil {
				continue
			}
			if _, promise := result.Export().(*sobek.Promise); promise {
				vm.postWithContext(vm.context(), func(runtime *sobek.Runtime) error {
					_, _ = vm.awaitValue(vm.context(), runtime, result)
					return nil
				})
			}
		}
		ctx := vm.context()
		envelope := vmEventEnvelope{source: vm, data: data.Export()}
		_, err := vm.hostCall(ctx, runtime, func() (any, error) {
			bus.Emit(ctx, channel, envelope)
			return nil, nil
		})
		must(runtime, err)
		return sobek.Undefined()
	}); err != nil {
		return nil, err
	}
	return object, nil
}

type vmEventEnvelope struct {
	source *runtimeVM
	data   any
}
