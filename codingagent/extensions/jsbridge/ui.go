package jsbridge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/grafana/sobek"
)

// newUIObject builds ctx.ui with the upstream ExtensionUIContext member names
// and shapes (packages/coding-agent/src/core/extensions/types.ts). Custom
// components, overlays, and editor replacement (custom, setEditorComponent,
// getEditorComponent) are WP-542.
func newUIObject(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.Context) (*sobek.Object, error) {
	ui := runtime.NewObject()
	methods := map[string]any{
		"notify": func(call sobek.FunctionCall) sobek.Value {
			notificationType := extensions.NotificationType(call.Argument(1).String())
			if notificationType == "" || notificationType == "undefined" {
				notificationType = extensions.NotifyInfo
			}
			contextValue.UI().Notify(call.Argument(0).String(), notificationType)
			return sobek.Undefined()
		},
		"select": func(call sobek.FunctionCall) sobek.Value {
			title := call.Argument(0).String()
			var options []string
			must(runtime, decodeJSON(runtime, call.Argument(1), &options))
			dialogOptions := decodeDialogOptions(runtime, vm, call.Argument(2))
			userInterface := contextValue.UI()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				value, ok, err := userInterface.Select(ctx, title, options, dialogOptions)
				if err != nil {
					return nil, err
				}
				if !ok {
					return promiseUndefined, nil
				}
				return value, nil
			})
		},
		"confirm": func(call sobek.FunctionCall) sobek.Value {
			title := call.Argument(0).String()
			message := call.Argument(1).String()
			dialogOptions := decodeDialogOptions(runtime, vm, call.Argument(2))
			userInterface := contextValue.UI()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return userInterface.Confirm(ctx, title, message, dialogOptions)
			})
		},
		"input": func(call sobek.FunctionCall) sobek.Value {
			title := call.Argument(0).String()
			placeholder := optionalStringPointer(call.Argument(1))
			dialogOptions := decodeDialogOptions(runtime, vm, call.Argument(2))
			userInterface := contextValue.UI()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				value, ok, err := userInterface.Input(ctx, title, placeholder, dialogOptions)
				if err != nil {
					return nil, err
				}
				if !ok {
					return promiseUndefined, nil
				}
				return value, nil
			})
		},
		"editor": func(call sobek.FunctionCall) sobek.Value {
			title := call.Argument(0).String()
			prefill := optionalStringPointer(call.Argument(1))
			userInterface := contextValue.UI()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				value, ok, err := userInterface.Editor(ctx, title, prefill)
				if err != nil {
					return nil, err
				}
				if !ok {
					return promiseUndefined, nil
				}
				return value, nil
			})
		},
		"onTerminalInput": func(call sobek.FunctionCall) sobek.Value {
			handler, ok := sobek.AssertFunction(call.Argument(0))
			if !ok {
				panic(runtime.NewTypeError("terminal input handler is not a function"))
			}
			unsubscribe := contextValue.UI().OnTerminalInput(func(data string) *extensions.TerminalInputResult {
				value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
					result, err := handler(sobek.Undefined(), runtime.ToValue(data))
					if err != nil {
						return nil, err
					}
					if !present(result) {
						return nil, nil
					}
					object := result.ToObject(runtime)
					decoded := &extensions.TerminalInputResult{}
					if consume := object.Get("consume"); present(consume) {
						decoded.Consume = consume.ToBoolean()
					}
					if replacement := object.Get("data"); present(replacement) {
						replacementValue := replacement.String()
						decoded.Data = &replacementValue
					}
					return decoded, nil
				})
				if err != nil {
					return nil
				}
				result, _ := value.(*extensions.TerminalInputResult)
				return result
			})
			return runtime.ToValue(func(sobek.FunctionCall) sobek.Value {
				unsubscribe()
				return sobek.Undefined()
			})
		},
		"setStatus": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetStatus(call.Argument(0).String(), optionalStringPointer(call.Argument(1)))
			return sobek.Undefined()
		},
		"setWorkingMessage": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetWorkingMessage(optionalStringPointer(call.Argument(0)))
			return sobek.Undefined()
		},
		"setWorkingVisible": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetWorkingVisible(call.Argument(0).ToBoolean())
			return sobek.Undefined()
		},
		"setWorkingIndicator": func(call sobek.FunctionCall) sobek.Value {
			options, err := decodeWorkingIndicatorOptions(runtime, call.Argument(0))
			must(runtime, err)
			contextValue.UI().SetWorkingIndicator(options)
			return sobek.Undefined()
		},
		"setHiddenThinkingLabel": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetHiddenThinkingLabel(optionalStringPointer(call.Argument(0)))
			return sobek.Undefined()
		},
		"setWidget": func(call sobek.FunctionCall) sobek.Value {
			key := call.Argument(0).String()
			widget, err := decodeWidgetContent(runtime, vm, call.Argument(1))
			must(runtime, err)
			options := decodeWidgetOptions(runtime, call.Argument(2))
			userInterface := contextValue.UI()
			_, err = vm.hostCall(vm.context(), runtime, func() (any, error) {
				userInterface.SetWidget(key, widget, options)
				return nil, nil
			})
			must(runtime, err)
			return sobek.Undefined()
		},
		"setFooter": func(call sobek.FunctionCall) sobek.Value {
			var factory extensions.FooterFactory
			if value := call.Argument(0); present(value) {
				callback, ok := sobek.AssertFunction(value)
				if !ok {
					panic(runtime.NewTypeError("footer factory is not a function"))
				}
				factory = newJSFooterFactory(vm, callback)
			}
			userInterface := contextValue.UI()
			_, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				userInterface.SetFooter(factory)
				return nil, nil
			})
			must(runtime, err)
			return sobek.Undefined()
		},
		"setHeader": func(call sobek.FunctionCall) sobek.Value {
			var factory extensions.HeaderFactory
			if value := call.Argument(0); present(value) {
				callback, ok := sobek.AssertFunction(value)
				if !ok {
					panic(runtime.NewTypeError("header factory is not a function"))
				}
				factory = newJSHeaderFactory(vm, callback)
			}
			userInterface := contextValue.UI()
			_, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				userInterface.SetHeader(factory)
				return nil, nil
			})
			must(runtime, err)
			return sobek.Undefined()
		},
		"setTitle": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetTitle(call.Argument(0).String())
			return sobek.Undefined()
		},
		"pasteToEditor": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().PasteToEditor(call.Argument(0).String())
			return sobek.Undefined()
		},
		"setEditorText": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetEditorText(call.Argument(0).String())
			return sobek.Undefined()
		},
		"getEditorText": func(sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(contextValue.UI().GetEditorText())
		},
		"addAutocompleteProvider": func(call sobek.FunctionCall) sobek.Value {
			callback, ok := sobek.AssertFunction(call.Argument(0))
			if !ok {
				panic(runtime.NewTypeError("autocomplete provider factory is not a function"))
			}
			factory := newJSAutocompleteFactory(vm, callback)
			userInterface := contextValue.UI()
			_, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				userInterface.AddAutocompleteProvider(factory)
				return nil, nil
			})
			must(runtime, err)
			return sobek.Undefined()
		},
		"getAllThemes": func(sobek.FunctionCall) sobek.Value {
			infos := contextValue.UI().GetAllThemes()
			result := make([]any, 0, len(infos))
			for _, info := range infos {
				entry := map[string]any{"name": info.Name}
				if info.Path != nil {
					entry["path"] = *info.Path
				}
				result = append(result, entry)
			}
			return toJS(runtime, result)
		},
		"getTheme": func(call sobek.FunctionCall) sobek.Value {
			theme := contextValue.UI().GetTheme(call.Argument(0).String())
			if theme == nil {
				return sobek.Undefined()
			}
			return themeValue(runtime, vm, theme)
		},
		"setTheme": func(call sobek.FunctionCall) sobek.Value {
			argument := call.Argument(0)
			var input any
			if object, isObject := argument.(*sobek.Object); isObject {
				if theme, known := vm.themes[object]; known {
					input = theme
				} else {
					input = argument.Export()
				}
			} else {
				input = argument.String()
			}
			result := contextValue.UI().SetTheme(input)
			object := runtime.NewObject()
			must(runtime, object.Set("success", result.Success))
			if result.Error != "" {
				must(runtime, object.Set("error", result.Error))
			}
			return object
		},
		"custom":             uiCustom(runtime, vm, contextValue),
		"setEditorComponent": uiSetEditorComponent(runtime, vm, contextValue),
		"getEditorComponent": uiGetEditorComponent(vm),
		"getToolsExpanded": func(sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(contextValue.UI().GetToolsExpanded())
		},
		"setToolsExpanded": func(call sobek.FunctionCall) sobek.Value {
			contextValue.UI().SetToolsExpanded(call.Argument(0).ToBoolean())
			return sobek.Undefined()
		},
	}
	for name, method := range methods {
		if err := ui.Set(name, method); err != nil {
			return nil, err
		}
	}
	if err := defineGetter(runtime, ui, "theme", func() sobek.Value {
		return themeValue(runtime, vm, contextValue.UI().Theme())
	}); err != nil {
		return nil, err
	}
	return ui, nil
}

func optionalStringPointer(value sobek.Value) *string {
	if !present(value) {
		return nil
	}
	text := value.String()
	return &text
}

func decodeDialogOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.DialogOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.DialogOptions{}
	if timeout := object.Get("timeout"); present(timeout) {
		timeoutValue := timeout.ToInteger()
		options.Timeout = &timeoutValue
	}
	if signal := object.Get("signal"); present(signal) {
		if signalObject, ok := signal.(*sobek.Object); ok {
			options.Signal = vm.signals[signalObject]
		}
	}
	return options
}

func decodeWorkingIndicatorOptions(runtime *sobek.Runtime, value sobek.Value) (*extensions.WorkingIndicatorOptions, error) {
	if !present(value) {
		return nil, nil
	}
	object := value.ToObject(runtime)
	options := &extensions.WorkingIndicatorOptions{}
	if frames := object.Get("frames"); present(frames) {
		var decoded []string
		if err := decodeJSON(runtime, frames, &decoded); err != nil {
			return nil, err
		}
		if decoded == nil {
			decoded = []string{}
		}
		options.Frames = decoded
	}
	if interval := object.Get("intervalMs"); present(interval) {
		options.IntervalMS = interval.ToInteger()
	}
	return options, nil
}

func decodeWidgetOptions(runtime *sobek.Runtime, value sobek.Value) *extensions.WidgetOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.WidgetOptions{}
	if placement := object.Get("placement"); present(placement) {
		options.Placement = extensions.WidgetPlacement(placement.String())
	}
	return options
}

func decodeWidgetContent(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (*extensions.Widget, error) {
	if !present(value) {
		return nil, nil
	}
	if factory, ok := sobek.AssertFunction(value); ok {
		return &extensions.Widget{Factory: newJSComponentFactory(vm, factory)}, nil
	}
	var lines []string
	if err := decodeJSON(runtime, value, &lines); err != nil {
		return nil, err
	}
	return &extensions.Widget{Lines: lines}, nil
}

// --- JS components handed to the Go UI seam ---

type jsComponent struct {
	vm      *runtimeVM
	self    sobek.Value
	render  sobek.Callable
	dispose sobek.Callable
}

func decodeJSComponent(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.Component, error) {
	if !present(value) {
		return nil, nil
	}
	object, ok := value.(*sobek.Object)
	if !ok {
		return nil, fmt.Errorf("extension component is not an object")
	}
	render, ok := sobek.AssertFunction(object.Get("render"))
	if !ok {
		return nil, fmt.Errorf("extension component has no render function")
	}
	component := &jsComponent{vm: vm, self: object, render: render}
	if dispose, hasDispose := sobek.AssertFunction(object.Get("dispose")); hasDispose {
		component.dispose = dispose
	}
	return component, nil
}

func (component *jsComponent) Render(width int) []string {
	value, err := component.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		result, err := component.render(component.self, runtime.ToValue(width))
		if err != nil {
			return nil, err
		}
		var lines []string
		if err := decodeJSON(runtime, result, &lines); err != nil {
			return nil, err
		}
		return lines, nil
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "jsbridge component render:", err)
		return nil
	}
	lines, _ := value.([]string)
	return lines
}

// Dispose mirrors upstream's component.dispose?.() call.
func (component *jsComponent) Dispose() {
	if component.dispose == nil {
		return
	}
	_, _ = component.vm.do(context.Background(), func(*sobek.Runtime) (any, error) {
		_, err := component.dispose(component.self)
		return nil, err
	})
}

func newJSComponentFactory(vm *runtimeVM, factory sobek.Callable) extensions.ComponentFactory {
	return func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			result, err := factory(sobek.Undefined(), uiHostValue(runtime, host), themeValue(runtime, vm, theme))
			if err != nil {
				return nil, err
			}
			return decodeJSComponent(runtime, vm, result)
		})
		if err != nil {
			return nil
		}
		component, _ := value.(extensions.Component)
		return component
	}
}

func newJSFooterFactory(vm *runtimeVM, factory sobek.Callable) extensions.FooterFactory {
	return func(host extensions.UIHost, theme extensions.Theme, data extensions.FooterDataProvider) extensions.Component {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			result, err := factory(
				sobek.Undefined(),
				uiHostValue(runtime, host),
				themeValue(runtime, vm, theme),
				footerDataValue(runtime, data),
			)
			if err != nil {
				return nil, err
			}
			return decodeJSComponent(runtime, vm, result)
		})
		if err != nil {
			return nil
		}
		component, _ := value.(extensions.Component)
		return component
	}
}

func newJSHeaderFactory(vm *runtimeVM, factory sobek.Callable) extensions.HeaderFactory {
	return func(host extensions.UIHost, theme extensions.Theme) extensions.Component {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			result, err := factory(sobek.Undefined(), uiHostValue(runtime, host), themeValue(runtime, vm, theme))
			if err != nil {
				return nil, err
			}
			return decodeJSComponent(runtime, vm, result)
		})
		if err != nil {
			return nil
		}
		component, _ := value.(extensions.Component)
		return component
	}
}

// uiHostValue exposes the TUI members upstream component factories use
// (tui.requestRender, tui.terminal.columns/rows) over the UIHost seam.
func uiHostValue(runtime *sobek.Runtime, host extensions.UIHost) sobek.Value {
	if host == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	must(runtime, object.Set("requestRender", func(sobek.FunctionCall) sobek.Value {
		host.Invalidate()
		return sobek.Undefined()
	}))
	terminal := runtime.NewObject()
	must(runtime, defineGetter(runtime, terminal, "columns", func() sobek.Value { return runtime.ToValue(host.Width()) }))
	must(runtime, defineGetter(runtime, terminal, "rows", func() sobek.Value { return runtime.ToValue(host.Height()) }))
	must(runtime, object.Set("terminal", terminal))
	return object
}

func themeValue(runtime *sobek.Runtime, vm *runtimeVM, theme extensions.Theme) sobek.Value {
	if theme == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	vm.themes[object] = theme
	twoArgument := map[string]func(string, string) string{"fg": theme.FG, "bg": theme.BG}
	for name, method := range twoArgument {
		fn := method
		must(runtime, object.Set(name, func(call sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(fn(call.Argument(0).String(), call.Argument(1).String()))
		}))
	}
	oneArgument := map[string]func(string) string{
		"bold": theme.Bold, "italic": theme.Italic, "underline": theme.Underline,
		"inverse": theme.Inverse, "strikethrough": theme.Strikethrough,
		"getFgAnsi": theme.FGANSI, "getBgAnsi": theme.BGANSI,
	}
	for name, method := range oneArgument {
		fn := method
		must(runtime, object.Set(name, func(call sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(fn(call.Argument(0).String()))
		}))
	}
	must(runtime, object.Set("getColorMode", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(theme.ColorMode())
	}))
	must(runtime, object.Set("getThinkingBorderColor", func(call sobek.FunctionCall) sobek.Value {
		border := theme.ThinkingBorderColor(agent.ThinkingLevel(call.Argument(0).String()))
		return runtime.ToValue(func(inner sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(border(inner.Argument(0).String()))
		})
	}))
	must(runtime, object.Set("getBashModeBorderColor", func(sobek.FunctionCall) sobek.Value {
		border := theme.BashModeBorderColor()
		return runtime.ToValue(func(inner sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(border(inner.Argument(0).String()))
		})
	}))
	return object
}

// footerDataValue mirrors upstream's ReadonlyFooterDataProvider. The Go seam
// exposes no provider count or branch subscription, so those members report
// zero and a no-op unsubscribe.
func footerDataValue(runtime *sobek.Runtime, provider extensions.FooterDataProvider) sobek.Value {
	if provider == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	must(runtime, object.Set("getGitBranch", func(sobek.FunctionCall) sobek.Value {
		branch := provider.GitBranch()
		if branch == "" {
			return sobek.Null()
		}
		return runtime.ToValue(branch)
	}))
	must(runtime, object.Set("getExtensionStatuses", func(sobek.FunctionCall) sobek.Value {
		statuses := provider.Statuses()
		keys := make([]string, 0, len(statuses))
		for key := range statuses {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		entries := make([]any, 0, len(keys))
		for _, key := range keys {
			entries = append(entries, runtime.NewArray(key, statuses[key]))
		}
		mapConstructor, ok := sobek.AssertConstructor(runtime.Get("Map"))
		if !ok {
			panic(runtime.NewTypeError("Map constructor is unavailable"))
		}
		value, err := mapConstructor(nil, runtime.NewArray(entries...))
		must(runtime, err)
		return value
	}))
	must(runtime, object.Set("getAvailableProviderCount", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(0)
	}))
	must(runtime, object.Set("onBranchChange", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(func(sobek.FunctionCall) sobek.Value { return sobek.Undefined() })
	}))
	return object
}

// --- autocomplete provider bridging ---

func newJSAutocompleteFactory(vm *runtimeVM, factory sobek.Callable) extensions.AutocompleteProviderFactory {
	return func(current extensions.AutocompleteProvider) extensions.AutocompleteProvider {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			result, err := factory(sobek.Undefined(), autocompleteProviderValue(runtime, vm, current))
			if err != nil {
				return nil, err
			}
			return decodeJSAutocompleteProvider(runtime, vm, result)
		})
		if err != nil {
			return current
		}
		provider, ok := value.(extensions.AutocompleteProvider)
		if !ok {
			return current
		}
		return provider
	}
}

func autocompleteProviderValue(runtime *sobek.Runtime, vm *runtimeVM, provider extensions.AutocompleteProvider) sobek.Value {
	if provider == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	must(runtime, object.Set("triggerCharacters", toJS(runtime, nonNilSlice(provider.TriggerCharacters()))))
	must(runtime, object.Set("getSuggestions", func(call sobek.FunctionCall) sobek.Value {
		request := decodeAutocompleteRequest(runtime, vm, call)
		return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
			result, err := provider.GetSuggestions(ctx, request)
			if err != nil {
				return nil, err
			}
			if result == nil {
				return nil, nil
			}
			return map[string]any{"items": nonNilSlice(result.Items), "prefix": result.Prefix}, nil
		})
	}))
	must(runtime, object.Set("applyCompletion", func(call sobek.FunctionCall) sobek.Value {
		request := decodeAutocompleteRequest(runtime, vm, call)
		var item extensions.AutocompleteItem
		must(runtime, decodeJSON(runtime, call.Argument(3), &item))
		prefix := call.Argument(4).String()
		lines, cursorLine, cursorCol := provider.ApplyCompletion(request, item, prefix)
		return toJS(runtime, map[string]any{"lines": nonNilSlice(lines), "cursorLine": cursorLine, "cursorCol": cursorCol})
	}))
	must(runtime, object.Set("shouldTriggerFileCompletion", func(call sobek.FunctionCall) sobek.Value {
		request := decodeAutocompleteRequest(runtime, vm, call)
		return runtime.ToValue(provider.ShouldTriggerFileCompletion(request))
	}))
	return object
}

func decodeAutocompleteRequest(runtime *sobek.Runtime, vm *runtimeVM, call sobek.FunctionCall) extensions.AutocompleteRequest {
	request := extensions.AutocompleteRequest{Signal: context.Background()}
	must(runtime, decodeJSON(runtime, call.Argument(0), &request.Lines))
	request.CursorLine = int(call.Argument(1).ToInteger())
	request.CursorCol = int(call.Argument(2).ToInteger())
	if options := call.Argument(3); present(options) {
		if object, ok := options.(*sobek.Object); ok {
			if signal := object.Get("signal"); present(signal) {
				if signalObject, isObject := signal.(*sobek.Object); isObject {
					if ctx := vm.signals[signalObject]; ctx != nil {
						request.Signal = ctx
					}
				}
			}
			if force := object.Get("force"); present(force) {
				request.Force = force.ToBoolean()
			}
		}
	}
	return request
}

type jsAutocompleteProvider struct {
	vm              *runtimeVM
	self            *sobek.Object
	triggers        []string
	getSuggestions  sobek.Callable
	applyCompletion sobek.Callable
	shouldTrigger   sobek.Callable
}

func decodeJSAutocompleteProvider(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.AutocompleteProvider, error) {
	object, ok := value.(*sobek.Object)
	if !ok {
		return nil, errors.New("autocomplete provider is not an object")
	}
	provider := &jsAutocompleteProvider{vm: vm, self: object}
	if triggers := object.Get("triggerCharacters"); present(triggers) {
		if err := decodeJSON(runtime, triggers, &provider.triggers); err != nil {
			return nil, err
		}
	}
	if provider.getSuggestions, ok = sobek.AssertFunction(object.Get("getSuggestions")); !ok {
		return nil, errors.New("autocomplete provider has no getSuggestions function")
	}
	if provider.applyCompletion, ok = sobek.AssertFunction(object.Get("applyCompletion")); !ok {
		return nil, errors.New("autocomplete provider has no applyCompletion function")
	}
	if shouldTrigger, hasShouldTrigger := sobek.AssertFunction(object.Get("shouldTriggerFileCompletion")); hasShouldTrigger {
		provider.shouldTrigger = shouldTrigger
	}
	return provider, nil
}

func (provider *jsAutocompleteProvider) TriggerCharacters() []string {
	return append([]string(nil), provider.triggers...)
}

func (provider *jsAutocompleteProvider) GetSuggestions(
	ctx context.Context,
	request extensions.AutocompleteRequest,
) (*extensions.AutocompleteResult, error) {
	value, err := provider.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		options := runtime.NewObject()
		signal, err := newAbortSignal(runtime, provider.vm, requestSignal(request))
		if err != nil {
			return nil, err
		}
		if err := options.Set("signal", signal); err != nil {
			return nil, err
		}
		if err := options.Set("force", request.Force); err != nil {
			return nil, err
		}
		result, err := provider.getSuggestions(
			provider.self,
			toJS(runtime, nonNilSlice(request.Lines)),
			runtime.ToValue(request.CursorLine),
			runtime.ToValue(request.CursorCol),
			options,
		)
		if err != nil {
			return nil, err
		}
		result, err = provider.vm.awaitValue(ctx, runtime, result)
		if err != nil || !present(result) {
			return nil, err
		}
		decoded := &extensions.AutocompleteResult{}
		if err := decodeJSON(runtime, result, decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	})
	if err != nil || value == nil {
		return nil, err
	}
	return value.(*extensions.AutocompleteResult), nil
}

func (provider *jsAutocompleteProvider) ApplyCompletion(
	request extensions.AutocompleteRequest,
	item extensions.AutocompleteItem,
	prefix string,
) ([]string, int, int) {
	value, err := provider.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		result, err := provider.applyCompletion(
			provider.self,
			toJS(runtime, nonNilSlice(request.Lines)),
			runtime.ToValue(request.CursorLine),
			runtime.ToValue(request.CursorCol),
			toJS(runtime, item),
			runtime.ToValue(prefix),
		)
		if err != nil {
			return nil, err
		}
		var decoded struct {
			Lines      []string `json:"lines"`
			CursorLine int      `json:"cursorLine"`
			CursorCol  int      `json:"cursorCol"`
		}
		if err := decodeJSON(runtime, result, &decoded); err != nil {
			return nil, err
		}
		return decoded, nil
	})
	if err != nil {
		return request.Lines, request.CursorLine, request.CursorCol
	}
	decoded := value.(struct {
		Lines      []string `json:"lines"`
		CursorLine int      `json:"cursorLine"`
		CursorCol  int      `json:"cursorCol"`
	})
	return decoded.Lines, decoded.CursorLine, decoded.CursorCol
}

func (provider *jsAutocompleteProvider) ShouldTriggerFileCompletion(request extensions.AutocompleteRequest) bool {
	if provider.shouldTrigger == nil {
		return false
	}
	value, err := provider.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		result, err := provider.shouldTrigger(
			provider.self,
			toJS(runtime, nonNilSlice(request.Lines)),
			runtime.ToValue(request.CursorLine),
			runtime.ToValue(request.CursorCol),
		)
		if err != nil {
			return nil, err
		}
		return result.ToBoolean(), nil
	})
	if err != nil {
		return false
	}
	triggered, _ := value.(bool)
	return triggered
}

func requestSignal(request extensions.AutocompleteRequest) context.Context {
	if request.Signal != nil {
		return request.Signal
	}
	return context.Background()
}

// installAbortController provides the Node global upstream extensions use to
// dismiss dialogs programmatically; abort cancels the backing context so
// bridged DialogOptions.Signal observes it.
func installAbortController(runtime *sobek.Runtime, vm *runtimeVM) error {
	return runtime.Set("AbortController", func(call sobek.ConstructorCall) *sobek.Object {
		ctx, cancel := context.WithCancelCause(context.Background())
		signal, err := newAbortSignal(runtime, vm, ctx)
		must(runtime, err)
		must(runtime, call.This.Set("signal", signal))
		must(runtime, call.This.Set("abort", func(inner sobek.FunctionCall) sobek.Value {
			reason := errors.New("This operation was aborted") //nolint:staticcheck // upstream AbortController reason message

			if present(inner.Argument(0)) {
				reason = errors.New(inner.Argument(0).String())
			}
			cancel(reason)
			return sobek.Undefined()
		}))
		return nil
	})
}
