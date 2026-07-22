package jsbridge

import (
	"context"
	"strings"
	"sync"

	"github.com/OrdalieTech/pigo/ai"
	aiauth "github.com/OrdalieTech/pigo/ai/auth"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/grafana/sobek"
)

func newContextObject(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.Context) (*sobek.Object, error) {
	object := runtime.NewObject()
	if contextValue == nil {
		return object, nil
	}
	var signalValue, sessionManagerValue, modelRegistryValue sobek.Value
	getters := map[string]func() sobek.Value{
		"cwd":   func() sobek.Value { return runtime.ToValue(contextValue.CWD()) },
		"mode":  func() sobek.Value { return runtime.ToValue(string(contextValue.Mode())) },
		"hasUI": func() sobek.Value { return runtime.ToValue(contextValue.HasUI()) },
		"sessionManager": func() sobek.Value {
			if sessionManagerValue == nil {
				sessionManagerValue = newSessionManagerObject(runtime, contextValue.SessionManager())
			}
			return sessionManagerValue
		},
		"modelRegistry": func() sobek.Value {
			if modelRegistryValue == nil {
				modelRegistryValue = newModelRegistryObject(runtime, vm, contextValue.ModelRegistry())
			}
			return modelRegistryValue
		},
		"model": func() sobek.Value {
			model := contextValue.Model()
			if model == nil {
				return sobek.Undefined()
			}
			return toJS(runtime, model)
		},
		"signal": func() sobek.Value {
			if signalValue == nil {
				var err error
				signalValue, err = newAbortSignal(runtime, vm, contextValue.Signal())
				must(runtime, err)
			}
			return signalValue
		},
	}
	for name, getter := range getters {
		if err := defineGetter(runtime, object, name, getter); err != nil {
			return nil, err
		}
	}
	methods := map[string]any{
		"isIdle":             func() bool { return contextValue.IsIdle() },
		"isProjectTrusted":   func() bool { return contextValue.IsProjectTrusted() },
		"abort":              func() { contextValue.Abort() },
		"hasPendingMessages": func() bool { return contextValue.HasPendingMessages() },
		"shutdown":           func() { contextValue.Shutdown() },
		"getContextUsage": func(sobek.FunctionCall) sobek.Value {
			usage := contextValue.GetContextUsage()
			if usage == nil {
				return sobek.Undefined()
			}
			return toJS(runtime, usage)
		},
		"compact": func(call sobek.FunctionCall) sobek.Value {
			contextValue.Compact(decodeCompactOptions(runtime, vm, call.Argument(0)))
			return sobek.Undefined()
		},
		"getSystemPrompt": func() string { return contextValue.GetSystemPrompt() },
	}
	for name, method := range methods {
		if err := object.Set(name, method); err != nil {
			return nil, err
		}
	}
	ui, err := newUIObject(runtime, vm, contextValue)
	if err != nil {
		return nil, err
	}
	if err := object.Set("ui", ui); err != nil {
		return nil, err
	}
	return object, nil
}

func defineGetter(runtime *sobek.Runtime, object *sobek.Object, name string, getter func() sobek.Value) error {
	return object.DefineAccessorProperty(
		name,
		runtime.ToValue(func(sobek.FunctionCall) sobek.Value { return getter() }),
		nil,
		sobek.FLAG_TRUE,
		sobek.FLAG_TRUE,
	)
}

func newSessionManagerObject(runtime *sobek.Runtime, manager extensions.ReadonlySessionManager) sobek.Value {
	if manager == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	methods := map[string]any{
		"isPersisted":   func() bool { return manager.IsPersisted() },
		"getCwd":        func() string { return manager.GetCWD() },
		"getSessionDir": func() string { return manager.GetSessionDir() },
		"getSessionId":  func() string { return manager.GetSessionID() },
		"getSessionFile": func(sobek.FunctionCall) sobek.Value {
			value := manager.GetSessionFile()
			if value == "" {
				return sobek.Undefined()
			}
			return runtime.ToValue(value)
		},
		"getLeafId": func(sobek.FunctionCall) sobek.Value {
			value := manager.GetLeafID()
			if value == nil {
				return sobek.Null()
			}
			return runtime.ToValue(*value)
		},
		"getLeafEntry": func(sobek.FunctionCall) sobek.Value {
			value := manager.GetLeafEntry()
			if value == nil {
				return sobek.Undefined()
			}
			return toJS(runtime, value)
		},
		"getEntry": func(call sobek.FunctionCall) sobek.Value {
			value := manager.GetEntry(call.Argument(0).String())
			if value == nil {
				return sobek.Undefined()
			}
			return toJS(runtime, value)
		},
		"getEntries": func(sobek.FunctionCall) sobek.Value { return toJS(runtime, nonNilSlice(manager.GetEntries())) },
		"getHeader": func(sobek.FunctionCall) sobek.Value {
			value := manager.GetHeader()
			if value == nil {
				return sobek.Null()
			}
			return toJS(runtime, value)
		},
		"getSessionName": func(sobek.FunctionCall) sobek.Value {
			value := manager.GetSessionName()
			if value == nil {
				return sobek.Undefined()
			}
			return runtime.ToValue(*value)
		},
		"getLabel": func(call sobek.FunctionCall) sobek.Value {
			value := manager.GetLabel(call.Argument(0).String())
			if value == nil {
				return sobek.Undefined()
			}
			return runtime.ToValue(*value)
		},
		"getChildren": func(call sobek.FunctionCall) sobek.Value {
			return toJS(runtime, nonNilSlice(manager.GetChildren(call.Argument(0).String())))
		},
		"getBranch": func(call sobek.FunctionCall) sobek.Value {
			if present(call.Argument(0)) {
				return toJS(runtime, nonNilSlice(manager.GetBranch(call.Argument(0).String())))
			}
			return toJS(runtime, nonNilSlice(manager.GetBranch()))
		},
		"getTree":             func(sobek.FunctionCall) sobek.Value { return toJS(runtime, nonNilSlice(manager.GetTree())) },
		"buildContextEntries": func(sobek.FunctionCall) sobek.Value { return toJS(runtime, nonNilSlice(manager.BuildContextEntries())) },
		"buildSessionContext": func(sobek.FunctionCall) sobek.Value { return toJS(runtime, manager.BuildSessionContext()) },
	}
	for name, method := range methods {
		must(runtime, object.Set(name, method))
	}
	return object
}

func newModelRegistryObject(runtime *sobek.Runtime, vm *runtimeVM, registry extensions.ModelRegistry) sobek.Value {
	if registry == nil {
		return sobek.Undefined()
	}
	object := runtime.NewObject()
	methods := map[string]any{
		"refresh": func(sobek.FunctionCall) sobek.Value {
			return vm.promiseVoid(runtime, vm.context(), func(context.Context) error {
				return registry.Reload()
			})
		},
		"getError": func(sobek.FunctionCall) sobek.Value {
			if registry.Error() == "" {
				return sobek.Undefined()
			}
			return runtime.ToValue(registry.Error())
		},
		"getAll": func(sobek.FunctionCall) sobek.Value { return toJS(runtime, nonNilSlice(registry.Models())) },
		"getAvailable": func(sobek.FunctionCall) sobek.Value {
			value, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				return registry.AvailableWithError(nil)
			})
			must(runtime, err)
			models := value.([]ai.Model)
			return toJS(runtime, nonNilSlice(models))
		},
		"find": func(call sobek.FunctionCall) sobek.Value {
			model, ok := registry.Find(call.Argument(0).String(), call.Argument(1).String())
			if !ok {
				return sobek.Undefined()
			}
			return toJS(runtime, model)
		},
		"hasConfiguredAuth": func(call sobek.FunctionCall) sobek.Value {
			provider := call.Argument(0).String()
			if object, ok := call.Argument(0).(*sobek.Object); ok {
				provider = object.Get("provider").String()
			}
			value, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				return registry.HasConfiguredAuth(provider, nil), nil
			})
			must(runtime, err)
			return runtime.ToValue(value.(bool))
		},
		"getProviderAuthStatus": func(call sobek.FunctionCall) sobek.Value {
			provider := call.Argument(0).String()
			value, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				return registry.GetProviderAuthStatus(provider, nil), nil
			})
			must(runtime, err)
			return toJS(runtime, value)
		},
		"getApiKeyAndHeaders": func(call sobek.FunctionCall) sobek.Value {
			var model ai.Model
			must(runtime, decodeJSON(runtime, call.Argument(0), &model))
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				resolved, err := registry.ResolveProviderAuth(ctx, string(model.Provider), nil)
				if err != nil {
					return map[string]any{"ok": false, "error": err.Error()}, nil
				}
				var key *string
				var env map[string]string
				var authHeaders ai.ProviderHeaders
				if resolved != nil {
					key = resolved.Auth.APIKey
					env = resolved.Env
					authHeaders = resolved.Auth.Headers
				}
				headers, err := registry.ResolveModelHeaders(ctx, model, env, key)
				if err != nil {
					message := err.Error()
					if message == "authHeader requires a resolved API key" {
						message = `No API key found for "` + string(model.Provider) + `"`
					}
					return map[string]any{"ok": false, "error": message}, nil
				}
				result := map[string]any{"ok": true}
				if key != nil {
					result["apiKey"] = *key
				}
				mergedHeaders := mergeRegistryHeaders(authHeaders, headers)
				if mergedHeaders != nil {
					result["headers"] = mergedHeaders
				}
				if len(env) > 0 {
					result["env"] = env
				}
				return result, nil
			})
		},
		"getApiKeyForProvider": func(call sobek.FunctionCall) sobek.Value {
			provider := call.Argument(0).String()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				resolved, err := registry.ResolveProviderAuth(ctx, provider, nil)
				if err != nil {
					return promiseUndefined, nil
				}
				if resolved == nil || resolved.Auth.APIKey == nil {
					return promiseUndefined, nil
				}
				return *resolved.Auth.APIKey, nil
			})
		},
		"getProviderAuth": func(call sobek.FunctionCall) sobek.Value {
			provider := call.Argument(0).String()
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				resolved, err := registry.ResolveProviderAuth(ctx, provider, nil)
				if err != nil {
					return nil, err
				}
				if resolved == nil {
					return promiseUndefined, nil
				}
				return resolved, nil
			})
		},
		"getProvider": func(call sobek.FunctionCall) sobek.Value {
			provider, ok := registry.Provider(call.Argument(0).String())
			if !ok {
				return sobek.Undefined()
			}
			return registeredProviderValue(runtime, vm, provider)
		},
		"getProviderDisplayName": func(call sobek.FunctionCall) sobek.Value {
			return runtime.ToValue(registry.ProviderDisplayName(call.Argument(0).String()))
		},
		"isUsingOAuth": func(call sobek.FunctionCall) sobek.Value {
			model := call.Argument(0)
			provider := model.String()
			if object, ok := model.(*sobek.Object); ok {
				provider = object.Get("provider").String()
			}
			return runtime.ToValue(registry.IsUsingOAuth(provider))
		},
		"getRegisteredProviderConfig": func(call sobek.FunctionCall) sobek.Value {
			config, ok := registry.RegisteredProviderConfig(call.Argument(0).String())
			if !ok {
				return sobek.Undefined()
			}
			return registeredProviderConfigValue(runtime, vm, config)
		},
		"getRegisteredNativeProvider": func(call sobek.FunctionCall) sobek.Value {
			provider, ok := registry.RegisteredNativeProvider(call.Argument(0).String())
			if !ok {
				return sobek.Undefined()
			}
			return registeredProviderValue(runtime, vm, provider)
		},
		"getRegisteredProviderIds": func() []string { return registry.RegisteredProviderIDs() },
	}
	for name, method := range methods {
		must(runtime, object.Set(name, method))
	}
	return object
}

func registeredProviderValue(runtime *sobek.Runtime, vm *runtimeVM, provider extensions.Provider) sobek.Value {
	if registered, ok := provider.RegistrationValue.(registrationValue); ok && registered.vm == vm {
		return registered.value
	}
	object := runtime.NewObject()
	must(runtime, object.Set("id", provider.ID))
	must(runtime, object.Set("name", provider.Name))
	if provider.BaseURL != "" {
		must(runtime, object.Set("baseUrl", provider.BaseURL))
	}
	if provider.Headers != nil {
		must(runtime, object.Set("headers", toJS(runtime, provider.Headers)))
	}
	must(runtime, object.Set("auth", providerAuthValue(runtime, provider.Auth)))
	if provider.GetModels != nil {
		must(runtime, object.Set("getModels", func(sobek.FunctionCall) sobek.Value {
			value, err := vm.hostCall(vm.context(), runtime, func() (any, error) {
				return provider.GetModels()
			})
			must(runtime, err)
			models := value.([]ai.Model)
			return toJS(runtime, models)
		}))
	}
	return object
}

func mergeRegistryHeaders(base ai.ProviderHeaders, override *map[string]string) map[string]string {
	if base == nil && override == nil {
		return nil
	}
	merged := make(map[string]string, len(base))
	for name, value := range base {
		if value != nil {
			merged[name] = *value
		}
	}
	if override != nil {
		for name, value := range *override {
			for existing := range merged {
				if strings.EqualFold(existing, name) {
					delete(merged, existing)
				}
			}
			merged[name] = value
		}
	}
	return merged
}

func registeredProviderConfigValue(runtime *sobek.Runtime, vm *runtimeVM, config extensions.ProviderConfig) sobek.Value {
	object := runtime.NewObject()
	values := map[string]any{
		"name": config.Name, "baseUrl": config.BaseURL, "apiKey": config.APIKey, "api": string(config.API),
		"headers": config.Headers, "authHeader": config.AuthHeader, "models": config.Models,
	}
	for name, value := range values {
		if config.Defined[name] {
			if registered, ok := config.RegistrationValues[name].(registrationValue); ok && registered.vm == vm {
				must(runtime, object.Set(name, registered.value))
			} else {
				must(runtime, object.Set(name, toJS(runtime, value)))
			}
		}
	}
	for _, name := range []string{"refreshModels", "oauth", "streamSimple"} {
		if registered, ok := config.RegistrationValues[name].(registrationValue); ok && registered.vm == vm {
			must(runtime, object.Set(name, registered.value))
		}
	}
	return object
}

func providerAuthValue(runtime *sobek.Runtime, auth aiauth.ProviderAuth) sobek.Value {
	object := runtime.NewObject()
	if auth.APIKey != nil {
		apiKey := runtime.NewObject()
		must(runtime, apiKey.Set("name", auth.APIKey.Name()))
		must(runtime, object.Set("apiKey", apiKey))
	}
	if auth.OAuth != nil {
		oauth := runtime.NewObject()
		must(runtime, oauth.Set("name", auth.OAuth.Name()))
		must(runtime, object.Set("oauth", oauth))
	}
	return object
}

func decodeCompactOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.CompactOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	dispatchContext := vm.context()
	// Compaction outlives the dispatching event, whose context is cancelled
	// once the handler returns; upstream still awaits the callbacks. Keep the
	// dispatch context while it is alive (host calls inside the callback stay
	// attributed to it) and fall back to the VM lifetime context afterwards.
	callbackContext := func() context.Context {
		if dispatchContext.Err() != nil {
			return vm.rootCtx
		}
		return dispatchContext
	}
	options := &extensions.CompactOptions{CustomInstructions: stringProperty(object, "customInstructions")}
	if callback, ok := sobek.AssertFunction(object.Get("onComplete")); ok {
		options.OnComplete = func(result session.CompactionResult) {
			vm.post(callbackContext(), true, func(runtime *sobek.Runtime) error {
				value, err := callback(sobek.Undefined(), toJS(runtime, result))
				if err != nil {
					return err
				}
				_, err = vm.awaitValue(context.Background(), runtime, value)
				return err
			})
		}
	}
	if callback, ok := sobek.AssertFunction(object.Get("onError")); ok {
		options.OnError = func(callbackError error) {
			vm.post(callbackContext(), true, func(runtime *sobek.Runtime) error {
				value, err := callback(sobek.Undefined(), runtime.NewGoError(callbackError))
				if err != nil {
					return err
				}
				_, err = vm.awaitValue(context.Background(), runtime, value)
				return err
			})
		}
	}
	return options
}

func newCommandContextObject(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.CommandContext) (*sobek.Object, error) {
	object, err := newContextObject(runtime, vm, contextValue)
	if err != nil {
		return nil, err
	}
	methods := map[string]any{
		"getSystemPromptOptions": func(sobek.FunctionCall) sobek.Value {
			return toJS(runtime, contextValue.GetSystemPromptOptions())
		},
		"waitForIdle": func(sobek.FunctionCall) sobek.Value {
			return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
				return contextValue.WaitForIdle(ctx)
			})
		},
		"newSession": func(call sobek.FunctionCall) sobek.Value {
			options := decodeNewSessionOptions(runtime, vm, call.Argument(0))
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return contextValue.NewSession(ctx, options)
			})
		},
		"fork": func(call sobek.FunctionCall) sobek.Value {
			entryID := call.Argument(0).String()
			options := decodeForkOptions(runtime, vm, call.Argument(1))
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return contextValue.Fork(ctx, entryID, options)
			})
		},
		"navigateTree": func(call sobek.FunctionCall) sobek.Value {
			targetID := call.Argument(0).String()
			var options *extensions.NavigateTreeOptions
			if present(call.Argument(1)) {
				options = &extensions.NavigateTreeOptions{}
				must(runtime, decodeJSON(runtime, call.Argument(1), options))
			}
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return contextValue.NavigateTree(ctx, targetID, options)
			})
		},
		"switchSession": func(call sobek.FunctionCall) sobek.Value {
			path := call.Argument(0).String()
			options := decodeSwitchSessionOptions(runtime, vm, call.Argument(1))
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				return contextValue.SwitchSession(ctx, path, options)
			})
		},
		"reload": func(sobek.FunctionCall) sobek.Value {
			return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
				return contextValue.Reload(ctx)
			})
		},
	}
	for name, method := range methods {
		if err := object.Set(name, method); err != nil {
			return nil, err
		}
	}
	return object, nil
}

func decodeNewSessionOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.NewSessionOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.NewSessionOptions{ParentSession: stringProperty(object, "parentSession")}
	if callback, ok := sobek.AssertFunction(object.Get("setup")); ok {
		options.Setup = func(manager *session.SessionManager) error {
			_, err := vm.callback(context.Background(), func(runtime *sobek.Runtime) (any, error) {
				result, err := callback(sobek.Undefined(), newWritableSessionManagerObject(runtime, manager))
				if err != nil {
					return nil, err
				}
				_, err = vm.awaitValue(context.Background(), runtime, result)
				return nil, err
			})
			return err
		}
	}
	if callback, ok := sobek.AssertFunction(object.Get("withSession")); ok {
		options.WithSession = replacedSessionCallback(vm, callback)
	}
	return options
}

func decodeForkOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.ForkOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.ForkOptions{Position: extensions.ForkPosition(stringProperty(object, "position"))}
	if callback, ok := sobek.AssertFunction(object.Get("withSession")); ok {
		options.WithSession = replacedSessionCallback(vm, callback)
	}
	return options
}

func decodeSwitchSessionOptions(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) *extensions.SwitchSessionOptions {
	if !present(value) {
		return nil
	}
	object := value.ToObject(runtime)
	options := &extensions.SwitchSessionOptions{}
	if callback, ok := sobek.AssertFunction(object.Get("withSession")); ok {
		options.WithSession = replacedSessionCallback(vm, callback)
	}
	return options
}

func replacedSessionCallback(
	vm *runtimeVM,
	callback sobek.Callable,
) func(context.Context, extensions.ReplacedSessionContext) error {
	return func(ctx context.Context, contextValue extensions.ReplacedSessionContext) error {
		_, err := vm.callback(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsContext, err := newReplacedSessionContextObject(runtime, vm, contextValue)
			if err != nil {
				return nil, err
			}
			result, err := callback(sobek.Undefined(), jsContext)
			if err != nil {
				return nil, err
			}
			_, err = vm.awaitValue(ctx, runtime, result)
			return nil, err
		})
		return err
	}
}

func newReplacedSessionContextObject(runtime *sobek.Runtime, vm *runtimeVM, contextValue extensions.ReplacedSessionContext) (*sobek.Object, error) {
	object, err := newCommandContextObject(runtime, vm, contextValue)
	if err != nil {
		return nil, err
	}
	if err := object.Set("sendMessage", func(call sobek.FunctionCall) sobek.Value {
		var message extensions.CustomMessage
		must(runtime, decodeJSON(runtime, call.Argument(0), &message))
		options := decodeSendMessageOptions(runtime, call.Argument(1))
		return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
			return contextValue.SendMessage(ctx, message, options)
		})
	}); err != nil {
		return nil, err
	}
	if err := object.Set("sendUserMessage", func(call sobek.FunctionCall) sobek.Value {
		content, decodeErr := decodeUserContent(runtime, call.Argument(0))
		must(runtime, decodeErr)
		options := decodeSendUserMessageOptions(runtime, call.Argument(1))
		return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
			return contextValue.SendUserMessage(ctx, content, options)
		})
	}); err != nil {
		return nil, err
	}
	return object, nil
}

func newWritableSessionManagerObject(runtime *sobek.Runtime, manager *session.SessionManager) sobek.Value {
	object := newSessionManagerObject(runtime, manager).ToObject(runtime)
	methods := map[string]any{
		"appendMessage": func(call sobek.FunctionCall) sobek.Value {
			id, err := manager.AppendMessage(call.Argument(0).Export())
			must(runtime, err)
			return runtime.ToValue(id)
		},
		"appendCustomEntry": func(call sobek.FunctionCall) sobek.Value {
			id, err := manager.AppendCustomEntry(call.Argument(0).String(), call.Argument(1).Export())
			must(runtime, err)
			return runtime.ToValue(id)
		},
		"appendSessionInfo": func(call sobek.FunctionCall) sobek.Value {
			id, err := manager.AppendSessionInfo(call.Argument(0).String())
			must(runtime, err)
			return runtime.ToValue(id)
		},
	}
	for name, method := range methods {
		must(runtime, object.Set(name, method))
	}
	return object
}

type abortSignalState struct {
	mu        sync.Mutex
	aborted   bool
	reason    sobek.Value
	onabort   sobek.Value
	listeners []sobek.Value
}

func newAbortSignal(runtime *sobek.Runtime, vm *runtimeVM, ctx context.Context) (sobek.Value, error) {
	signal, _, err := newAbortSignalWithState(runtime, vm, ctx)
	return signal, err
}

func newAbortSignalWithState(runtime *sobek.Runtime, vm *runtimeVM, ctx context.Context) (sobek.Value, *abortSignalState, error) {
	if ctx == nil {
		return sobek.Undefined(), nil, nil
	}
	object := runtime.NewObject()
	state := &abortSignalState{aborted: ctx.Err() != nil}
	vm.signals[object] = ctx
	if err := defineGetter(runtime, object, "aborted", func() sobek.Value {
		state.mu.Lock()
		aborted := state.aborted || ctx.Err() != nil
		state.mu.Unlock()
		return runtime.ToValue(aborted)
	}); err != nil {
		return nil, nil, err
	}
	if err := defineGetter(runtime, object, "reason", func() sobek.Value {
		state.mu.Lock()
		reason := state.reason
		state.mu.Unlock()
		if present(reason) {
			return reason
		}
		if ctx.Err() == nil {
			return sobek.Undefined()
		}
		return runtime.NewGoError(context.Cause(ctx))
	}); err != nil {
		return nil, nil, err
	}
	if err := object.DefineAccessorProperty(
		"onabort",
		runtime.ToValue(func(sobek.FunctionCall) sobek.Value {
			state.mu.Lock()
			value := state.onabort
			state.mu.Unlock()
			if value == nil {
				return sobek.Null()
			}
			return value
		}),
		runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			value := call.Argument(0)
			if _, ok := sobek.AssertFunction(value); !ok && !sobek.IsNull(value) && !sobek.IsUndefined(value) {
				return sobek.Undefined()
			}
			state.mu.Lock()
			state.onabort = value
			state.mu.Unlock()
			return sobek.Undefined()
		}),
		sobek.FLAG_TRUE,
		sobek.FLAG_FALSE,
	); err != nil {
		return nil, nil, err
	}
	if err := object.Set("throwIfAborted", func(sobek.FunctionCall) sobek.Value {
		if ctx.Err() != nil {
			state.mu.Lock()
			reason := state.reason
			state.mu.Unlock()
			if present(reason) {
				panic(reason)
			}
			panic(runtime.NewGoError(context.Cause(ctx)))
		}
		return sobek.Undefined()
	}); err != nil {
		return nil, nil, err
	}
	if err := object.Set("addEventListener", func(call sobek.FunctionCall) sobek.Value {
		if call.Argument(0).String() != "abort" {
			return sobek.Undefined()
		}
		if _, ok := sobek.AssertFunction(call.Argument(1)); !ok {
			return sobek.Undefined()
		}
		state.mu.Lock()
		state.listeners = append(state.listeners, call.Argument(1))
		state.mu.Unlock()
		return sobek.Undefined()
	}); err != nil {
		return nil, nil, err
	}
	if err := object.Set("removeEventListener", func(call sobek.FunctionCall) sobek.Value {
		if call.Argument(0).String() != "abort" {
			return sobek.Undefined()
		}
		state.mu.Lock()
		filtered := state.listeners[:0]
		for _, listener := range state.listeners {
			if !listener.SameAs(call.Argument(1)) {
				filtered = append(filtered, listener)
			}
		}
		state.listeners = filtered
		state.mu.Unlock()
		return sobek.Undefined()
	}); err != nil {
		return nil, nil, err
	}
	if done := ctx.Done(); done != nil && ctx.Err() == nil {
		go func() {
			select {
			case <-done:
			case <-vm.done:
				return
			}
			vm.post(ctx, true, func(runtime *sobek.Runtime) error {
				delete(vm.signals, object)
				state.mu.Lock()
				state.aborted = true
				listeners := append([]sobek.Value(nil), state.listeners...)
				onabort := state.onabort
				state.mu.Unlock()
				event := runtime.NewObject()
				must(runtime, event.Set("type", "abort"))
				must(runtime, event.Set("target", object))
				must(runtime, event.Set("currentTarget", object))
				if handler, ok := sobek.AssertFunction(onabort); ok {
					if _, err := handler(object, event); err != nil {
						return err
					}
				}
				for _, listener := range listeners {
					if handler, ok := sobek.AssertFunction(listener); ok {
						if _, err := handler(object, event); err != nil {
							return err
						}
					}
				}
				return nil
			})
		}()
	}
	return object, state, nil
}
