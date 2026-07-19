package jsbridge

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/grafana/sobek"
)

// ─── ctx.ui.custom ──────────────────────────────────────────

func uiCustom(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.Context) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		factory, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			panic(runtime.NewTypeError("custom component factory is not a function"))
		}
		options := decodeCustomOptions(runtime, vm, call.Argument(1))
		userInterface := contextValue.UI()
		return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
			value, resolved, err := userInterface.Custom(ctx, newJSCustomFactory(vm, factory), options)
			if err != nil {
				return nil, err
			}
			if !resolved {
				return promiseUndefined, nil
			}
			return value, nil
		})
	}
}

func newJSCustomFactory(vm *runtimeVM, factory sobek.Callable) extensions.CustomFactory {
	return func(host extensions.UIHost, theme extensions.Theme, keybindings extensions.Keybindings, done extensions.CustomDone) (extensions.Component, error) {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			doneValue := runtime.ToValue(func(inner sobek.FunctionCall) sobek.Value {
				done(inner.Argument(0))
				return sobek.Undefined()
			})
			result, err := factory(
				sobek.Undefined(),
				uiHostValue(runtime, host),
				themeValue(runtime, vm, theme),
				keybindingsValue(runtime, keybindings),
				doneValue,
			)
			if err != nil {
				return nil, err
			}
			if present(result) {
				if _, isPromise := result.Export().(*sobek.Promise); isPromise {
					if result, err = vm.awaitValue(vm.context(), runtime, result); err != nil {
						return nil, err
					}
				}
			}
			return decodeFocusableComponent(runtime, vm, result)
		})
		if err != nil {
			return nil, err
		}
		component, _ := value.(extensions.Component)
		if component == nil {
			return nil, fmt.Errorf("custom component factory returned no component")
		}
		return component, nil
	}
}

// jsFocusableComponent adds keyboard-input delivery for components that
// declare handleInput (custom dialogs receive focus upstream).
type jsFocusableComponent struct {
	*jsComponent
	handleInput sobek.Callable
}

func (component *jsFocusableComponent) HandleInput(data string) {
	_, _ = component.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		_, err := component.handleInput(component.self, runtime.ToValue(data))
		return nil, err
	})
}

func decodeFocusableComponent(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.Component, error) {
	base, err := decodeJSComponent(runtime, vm, value)
	if err != nil {
		return nil, err
	}
	js, _ := base.(*jsComponent)
	if js == nil {
		return base, nil
	}
	if handleInput, ok := sobek.AssertFunction(value.ToObject(runtime).Get("handleInput")); ok {
		return &jsFocusableComponent{jsComponent: js, handleInput: handleInput}, nil
	}
	return base, nil
}

// ─── overlay options and handle ─────────────────────────────

func decodeCustomOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.CustomOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.CustomOptions{}
	if overlay := object.Get("overlay"); present(overlay) {
		options.Overlay = overlay.ToBoolean()
	}
	if overlayOptions := object.Get("overlayOptions"); present(overlayOptions) {
		if dynamic, ok := sobek.AssertFunction(overlayOptions); ok {
			options.DynamicOverlayOptions = func() extensions.OverlayOptions {
				value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
					result, err := dynamic(sobek.Undefined())
					if err != nil {
						return nil, err
					}
					decoded := decodeOverlayOptions(runtime, vm, result)
					if decoded == nil {
						return extensions.OverlayOptions{Anchor: extensions.OverlayCenter}, nil
					}
					return *decoded, nil
				})
				if err != nil {
					return extensions.OverlayOptions{Anchor: extensions.OverlayCenter}
				}
				resolved, _ := value.(extensions.OverlayOptions)
				return resolved
			}
		} else if decoded := decodeOverlayOptions(runtime, vm, overlayOptions); decoded != nil {
			options.StaticOverlayOptions = decoded
		}
	}
	if onHandle := object.Get("onHandle"); present(onHandle) {
		if handler, ok := sobek.AssertFunction(onHandle); ok {
			options.OnHandle = func(handle extensions.OverlayHandle) {
				vm.postWithContext(vm.context(), func(runtime *sobek.Runtime) error {
					_, err := handler(sobek.Undefined(), overlayHandleValue(runtime, vm, handle))
					return err
				})
			}
		}
	}
	return options
}

func decodeOverlayOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.OverlayOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.OverlayOptions{Anchor: extensions.OverlayCenter}
	if anchor := object.Get("anchor"); present(anchor) {
		options.Anchor = extensions.OverlayAnchor(anchor.String())
	}
	if width := object.Get("width"); present(width) {
		options.Width = width.Export()
	}
	if minWidth := object.Get("minWidth"); present(minWidth) {
		options.MinWidth = int(minWidth.ToInteger())
	}
	if maxHeight := object.Get("maxHeight"); present(maxHeight) {
		options.MaxHeight = maxHeight.Export()
	}
	if offsetX := object.Get("offsetX"); present(offsetX) {
		options.OffsetX = int(offsetX.ToInteger())
	}
	if offsetY := object.Get("offsetY"); present(offsetY) {
		options.OffsetY = int(offsetY.ToInteger())
	}
	if row := object.Get("row"); present(row) {
		options.Row = row.Export()
	}
	if column := object.Get("col"); present(column) {
		options.Column = column.Export()
	}
	if margin := object.Get("margin"); present(margin) {
		options.Margin = margin.Export()
	}
	if visible, ok := sobek.AssertFunction(object.Get("visible")); ok {
		options.Visible = func(width, height int) bool {
			value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
				result, err := visible(sobek.Undefined(), runtime.ToValue(width), runtime.ToValue(height))
				if err != nil {
					return nil, err
				}
				return result.ToBoolean(), nil
			})
			if err != nil {
				return true
			}
			shown, _ := value.(bool)
			return shown
		}
	}
	if nonCapturing := object.Get("nonCapturing"); present(nonCapturing) {
		options.NonCapturing = nonCapturing.ToBoolean()
	}
	return options
}

func overlayHandleValue(runtime *sobek.Runtime, vm *runtimeVM, handle extensions.OverlayHandle) sobek.Value {
	object := runtime.NewObject()
	hostVoid := func(operation func()) {
		_, _ = vm.hostCall(vm.context(), runtime, func() (any, error) {
			operation()
			return nil, nil
		})
	}
	hostBool := func(operation func() bool) bool {
		value, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
			return operation(), nil
		})
		if err != nil {
			return false
		}
		result, _ := value.(bool)
		return result
	}
	must(runtime, object.Set("hide", func(sobek.FunctionCall) sobek.Value {
		hostVoid(handle.Hide)
		return sobek.Undefined()
	}))
	must(runtime, object.Set("setHidden", func(call sobek.FunctionCall) sobek.Value {
		hidden := call.Argument(0).ToBoolean()
		hostVoid(func() { handle.SetHidden(hidden) })
		return sobek.Undefined()
	}))
	must(runtime, object.Set("isHidden", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(hostBool(handle.IsHidden))
	}))
	must(runtime, object.Set("focus", func(sobek.FunctionCall) sobek.Value {
		hostVoid(handle.Focus)
		return sobek.Undefined()
	}))
	must(runtime, object.Set("unfocus", func(call sobek.FunctionCall) sobek.Value {
		var options []extensions.OverlayUnfocusOptions
		if argument := call.Argument(0); present(argument) {
			target := argument.ToObject(runtime).Get("target")
			if present(target) {
				component, err := decodeFocusableComponent(runtime, vm, target)
				if err == nil {
					options = append(options, extensions.OverlayUnfocusOptions{Target: component})
				}
			} else {
				options = append(options, extensions.OverlayUnfocusOptions{})
			}
		}
		hostVoid(func() { handle.Unfocus(options...) })
		return sobek.Undefined()
	}))
	must(runtime, object.Set("isFocused", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(hostBool(handle.IsFocused))
	}))
	return object
}

func keybindingsValue(runtime *sobek.Runtime, keybindings extensions.Keybindings) sobek.Value {
	if keybindings == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	must(runtime, object.Set("matches", func(call sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(keybindings.Matches(call.Argument(0).String(), call.Argument(1).String()))
	}))
	must(runtime, object.Set("keys", func(call sobek.FunctionCall) sobek.Value {
		return toJS(runtime, keybindings.Keys(call.Argument(0).String()))
	}))
	return object
}

// ─── editor replacement ─────────────────────────────────────

func uiSetEditorComponent(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.Context) func(sobek.FunctionCall) sobek.Value {
	return func(call sobek.FunctionCall) sobek.Value {
		argument := call.Argument(0)
		if !present(argument) {
			vm.editorFactory = nil
			contextValue.UI().SetEditorComponent(nil)
			return sobek.Undefined()
		}
		factory, ok := sobek.AssertFunction(argument)
		if !ok {
			panic(runtime.NewTypeError("editor factory is not a function"))
		}
		vm.editorFactory = argument
		contextValue.UI().SetEditorComponent(newJSEditorFactory(vm, factory))
		return sobek.Undefined()
	}
}

func uiGetEditorComponent(vm *runtimeVM) func(sobek.FunctionCall) sobek.Value {
	return func(sobek.FunctionCall) sobek.Value {
		if vm.editorFactory == nil {
			return sobek.Undefined()
		}
		return vm.editorFactory
	}
}

func newJSEditorFactory(vm *runtimeVM, factory sobek.Callable) extensions.EditorFactory {
	return func(host extensions.UIHost, theme extensions.Theme, keybindings extensions.Keybindings) extensions.EditorComponent {
		value, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			result, err := factory(
				sobek.Undefined(),
				uiHostValue(runtime, host),
				themeValue(runtime, vm, theme),
				keybindingsValue(runtime, keybindings),
			)
			if err != nil {
				return nil, err
			}
			return decodeJSEditorComponent(runtime, vm, result)
		})
		if err != nil {
			return nil
		}
		component, _ := value.(extensions.EditorComponent)
		return component
	}
}

// jsEditorComponent bridges a JS editor object (typically extending
// CustomEditor) onto the extension editor seam.
type jsEditorComponent struct {
	*jsComponent
	getText         sobek.Callable
	setText         sobek.Callable
	handleInput     sobek.Callable
	setAutocomplete sobek.Callable
}

func decodeJSEditorComponent(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.EditorComponent, error) {
	base, err := decodeJSComponent(runtime, vm, value)
	if err != nil {
		return nil, err
	}
	js, _ := base.(*jsComponent)
	if js == nil {
		return nil, fmt.Errorf("editor factory returned no component object")
	}
	object := value.ToObject(runtime)
	editor := &jsEditorComponent{jsComponent: js}
	if getText, ok := sobek.AssertFunction(object.Get("getText")); ok {
		editor.getText = getText
	}
	if setText, ok := sobek.AssertFunction(object.Get("setText")); ok {
		editor.setText = setText
	}
	if handleInput, ok := sobek.AssertFunction(object.Get("handleInput")); ok {
		editor.handleInput = handleInput
	}
	if editor.getText == nil || editor.setText == nil || editor.handleInput == nil {
		return nil, fmt.Errorf("editor component requires getText, setText, and handleInput")
	}
	if setAutocomplete, ok := sobek.AssertFunction(object.Get("setAutocompleteProvider")); ok {
		editor.setAutocomplete = setAutocomplete
	}
	return editor, nil
}

func (editor *jsEditorComponent) GetText() string {
	value, err := editor.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		result, err := editor.getText(editor.self)
		if err != nil {
			return nil, err
		}
		return result.String(), nil
	})
	if err != nil {
		return ""
	}
	text, _ := value.(string)
	return text
}

func (editor *jsEditorComponent) SetText(text string) {
	_, _ = editor.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		_, err := editor.setText(editor.self, runtime.ToValue(text))
		return nil, err
	})
}

func (editor *jsEditorComponent) HandleInput(data string) {
	_, _ = editor.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		_, err := editor.handleInput(editor.self, runtime.ToValue(data))
		return nil, err
	})
}

func (editor *jsEditorComponent) SetAutocompleteProvider(provider extensions.AutocompleteProvider) {
	if editor.setAutocomplete == nil {
		return
	}
	_, _ = editor.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		_, err := editor.setAutocomplete(editor.self, autocompleteProviderValue(runtime, editor.vm, provider))
		return nil, err
	})
}

// ─── CustomEditor base class ────────────────────────────────

// The class itself is JS so extensions can `extend` it natively; its state
// lives in the Go editor obtained from the host-registered base factory.
const customEditorClassJS = `(class CustomEditor {
	constructor(tui, theme, keybindings) {
		this.__base = __pi_customEditorBase(tui, theme, keybindings);
	}
	render(width) { return this.__base.render(width); }
	invalidate() { if (this.__base.invalidate) this.__base.invalidate(); }
	getText() { return this.__base.getText(); }
	setText(text) { this.__base.setText(text); }
	handleInput(data) { this.__base.handleInput(data); }
})`

func installCustomEditorBase(runtime *sobek.Runtime, vm *runtimeVM) (sobek.Value, error) {
	if err := runtime.Set("__pi_customEditorBase", func(sobek.FunctionCall) sobek.Value {
		factory := extensions.CustomEditorBase()
		if factory == nil {
			panic(runtime.NewTypeError("CustomEditor requires an interactive session"))
		}
		component := factory(nil, nil, nil)
		if component == nil {
			panic(runtime.NewTypeError("CustomEditor base editor is unavailable"))
		}
		return editorComponentValue(runtime, component)
	}); err != nil {
		return nil, err
	}
	return runtime.RunString(customEditorClassJS)
}

func editorComponentValue(runtime *sobek.Runtime, component extensions.EditorComponent) sobek.Value {
	object := runtime.NewObject()
	must(runtime, object.Set("render", func(call sobek.FunctionCall) sobek.Value {
		return toJS(runtime, component.Render(int(call.Argument(0).ToInteger())))
	}))
	must(runtime, object.Set("getText", func(sobek.FunctionCall) sobek.Value {
		return runtime.ToValue(component.GetText())
	}))
	must(runtime, object.Set("setText", func(call sobek.FunctionCall) sobek.Value {
		component.SetText(call.Argument(0).String())
		return sobek.Undefined()
	}))
	must(runtime, object.Set("handleInput", func(call sobek.FunctionCall) sobek.Value {
		component.HandleInput(call.Argument(0).String())
		return sobek.Undefined()
	}))
	if invalidator, ok := component.(interface{ Invalidate() }); ok {
		must(runtime, object.Set("invalidate", func(sobek.FunctionCall) sobek.Value {
			invalidator.Invalidate()
			return sobek.Undefined()
		}))
	}
	return object
}
