package jsbridge

import (
	"context"
	"fmt"
	"iter"
	"time"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	aiauth "github.com/OrdalieTech/pi-go/ai/auth"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/grafana/sobek"
)

func registerProvider(
	runtime *sobek.Runtime,
	vm *runtimeVM,
	api extensions.API,
	providerOrName sobek.Value,
	configValue sobek.Value,
) error {
	if !present(providerOrName) {
		return fmt.Errorf("registerProvider requires a provider or provider name")
	}
	if _, ok := providerOrName.Export().(string); ok {
		if !present(configValue) {
			return fmt.Errorf("provider config is required when registering by name")
		}
		config, err := decodeProviderConfig(runtime, vm, configValue)
		if err != nil {
			return err
		}
		return callProviderRegistration(runtime, vm, func() { api.RegisterProviderConfig(providerOrName.String(), config) })
	}
	object := providerOrName.ToObject(runtime)
	provider := extensions.Provider{
		ID:                stringProperty(object, "id"),
		Name:              stringProperty(object, "name"),
		BaseURL:           stringProperty(object, "baseUrl"),
		RegistrationValue: registrationValue{vm: vm, value: object},
	}
	if headers := object.Get("headers"); present(headers) {
		if err := decodeJSON(runtime, headers, &provider.Headers); err != nil {
			return err
		}
	}
	if provider.ID == "" {
		return fmt.Errorf("native provider has no id")
	}
	auth, err := decodeNativeProviderAuth(runtime, vm, object.Get("auth"))
	if err != nil {
		return err
	}
	provider.Auth = auth
	if provider.Name == "" {
		return fmt.Errorf("native provider %q requires name", provider.ID)
	}
	getModels, ok := sobek.AssertFunction(object.Get("getModels"))
	if !ok {
		return fmt.Errorf("native provider %q requires getModels", provider.ID)
	}
	provider.GetModels = func() ([]ai.Model, error) {
		result, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			return callNativeGetModels(runtime, object, getModels)
		})
		if err != nil {
			return nil, err
		}
		return result.([]ai.Model), nil
	}
	if callback, ok := sobek.AssertFunction(object.Get("refreshModels")); ok {
		provider.RefreshModels = func(refreshContext extensions.RefreshModelsContext) error {
			ctx := refreshContext.Signal
			if ctx == nil {
				ctx = context.Background()
			}
			_, err := vm.callback(ctx, func(runtime *sobek.Runtime) (any, error) {
				value, err := callback(object, newRefreshModelsContextObject(runtime, vm, refreshContext))
				if err != nil {
					return nil, err
				}
				_, err = vm.awaitValue(ctx, runtime, value)
				return nil, err
			})
			return err
		}
	}
	if callback, ok := sobek.AssertFunction(object.Get("filterModels")); ok {
		provider.FilterModels = func(models []ai.Model, credential *aiauth.Credential) ([]ai.Model, error) {
			result, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
				credentialValue := sobek.Value(sobek.Undefined())
				if credential != nil {
					credentialValue = toJS(runtime, credential)
				}
				value, err := callback(object, toJS(runtime, models), credentialValue)
				if err != nil {
					return nil, err
				}
				if _, async := value.Export().(*sobek.Promise); async {
					return nil, fmt.Errorf("native provider filterModels must return synchronously")
				}
				var filtered []ai.Model
				if err := decodeJSON(runtime, value, &filtered); err != nil {
					return nil, err
				}
				return filtered, nil
			})
			if err != nil {
				return nil, err
			}
			return result.([]ai.Model), nil
		}
	}
	if callback, ok := sobek.AssertFunction(object.Get("stream")); ok {
		provider.Stream = streamCallback(vm, object, callback)
	}
	if callback, ok := sobek.AssertFunction(object.Get("streamSimple")); ok {
		provider.StreamSimple = streamCallback(vm, object, callback)
	}
	return callProviderRegistration(runtime, vm, func() { api.RegisterProvider(provider) })
}

func callProviderRegistration(runtime *sobek.Runtime, vm *runtimeVM, register func()) error {
	_, err := vm.hostCall(vm.context(), runtime, func() (_ any, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("%v", recovered)
			}
		}()
		register()
		return nil, nil
	})
	return err
}

func callNativeGetModels(runtime *sobek.Runtime, owner *sobek.Object, callback sobek.Callable) ([]ai.Model, error) {
	value, err := callback(owner)
	if err != nil {
		return nil, err
	}
	if _, async := value.Export().(*sobek.Promise); async {
		return nil, fmt.Errorf("native provider getModels must return synchronously")
	}
	var models []ai.Model
	if err := decodeJSON(runtime, value, &models); err != nil {
		return nil, err
	}
	return models, nil
}

func decodeProviderConfig(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.ProviderConfig, error) {
	object := value.ToObject(runtime)
	config := extensions.ProviderConfig{
		Name:               stringProperty(object, "name"),
		BaseURL:            stringProperty(object, "baseUrl"),
		APIKey:             stringProperty(object, "apiKey"),
		API:                ai.API(stringProperty(object, "api")),
		Defined:            make(map[string]bool),
		RegistrationValues: make(map[string]any),
	}
	for _, name := range []string{"name", "baseUrl", "apiKey", "api", "headers", "authHeader", "models", "refreshModels", "oauth", "streamSimple"} {
		if value := object.Get(name); value != nil && !sobek.IsUndefined(value) {
			config.Defined[name] = true
			config.RegistrationValues[name] = registrationValue{vm: vm, value: value}
		}
	}
	if headers := object.Get("headers"); present(headers) {
		if err := decodeJSON(runtime, headers, &config.Headers); err != nil {
			return config, err
		}
	}
	if authHeader := object.Get("authHeader"); present(authHeader) {
		value := authHeader.ToBoolean()
		config.AuthHeader = &value
	}
	if models := object.Get("models"); present(models) {
		if err := decodeJSON(runtime, models, &config.Models); err != nil {
			return config, fmt.Errorf("provider models: %w", err)
		}
	}
	if callback, ok := sobek.AssertFunction(object.Get("refreshModels")); ok {
		config.RefreshModels = refreshModelsCallback(vm, object, callback)
	}
	if oauthValue := object.Get("oauth"); present(oauthValue) {
		oauth, err := decodeOAuthProvider(runtime, vm, oauthValue)
		if err != nil {
			return config, err
		}
		config.OAuth = oauth
	}
	if callback, ok := sobek.AssertFunction(object.Get("streamSimple")); ok {
		config.Stream = streamCallback(vm, object, callback)
	}
	return config, nil
}

func decodeNativeProviderAuth(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (aiauth.ProviderAuth, error) {
	if !present(value) {
		return aiauth.ProviderAuth{}, fmt.Errorf("native provider requires auth")
	}
	object := value.ToObject(runtime)
	result := aiauth.ProviderAuth{}
	if apiKeyValue := object.Get("apiKey"); present(apiKeyValue) {
		authObject := apiKeyValue.ToObject(runtime)
		resolve, ok := sobek.AssertFunction(authObject.Get("resolve"))
		if !ok {
			return result, fmt.Errorf("native provider apiKey auth requires resolve")
		}
		method := &jsAPIKeyAuth{vm: vm, owner: authObject, name: stringProperty(authObject, "name"), resolve: resolve}
		method.login, _ = sobek.AssertFunction(authObject.Get("login"))
		method.check, _ = sobek.AssertFunction(authObject.Get("check"))
		result.APIKey = method
	}
	if oauthValue := object.Get("oauth"); present(oauthValue) {
		authObject := oauthValue.ToObject(runtime)
		login, loginOK := sobek.AssertFunction(authObject.Get("login"))
		refresh, refreshOK := sobek.AssertFunction(authObject.Get("refresh"))
		toAuth, toAuthOK := sobek.AssertFunction(authObject.Get("toAuth"))
		if !loginOK || !refreshOK || !toAuthOK {
			return result, fmt.Errorf("native provider oauth auth requires login, refresh, and toAuth")
		}
		result.OAuth = &jsOAuthAuth{vm: vm, owner: authObject, name: stringProperty(authObject, "name"), login: login, refresh: refresh, toAuth: toAuth}
	}
	if result.APIKey == nil && result.OAuth == nil {
		return result, fmt.Errorf("native provider auth requires apiKey or oauth")
	}
	return result, nil
}

type jsAPIKeyAuth struct {
	vm      *runtimeVM
	owner   *sobek.Object
	name    string
	login   sobek.Callable
	check   sobek.Callable
	resolve sobek.Callable
}

func (method *jsAPIKeyAuth) Name() string { return method.name }

func (method *jsAPIKeyAuth) Check(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthCheck, error) {
	if method.check == nil {
		resolved, err := method.Resolve(ctx, authContext, credential)
		if err != nil || resolved == nil {
			return nil, err
		}
		return &aiauth.AuthCheck{Source: resolved.Source, Type: aiauth.CredentialAPIKey}, nil
	}
	result, err := method.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		input := runtime.NewObject()
		must(runtime, input.Set("ctx", newProviderAuthContext(runtime, method.vm, ctx, authContext)))
		if credential == nil {
			must(runtime, input.Set("credential", sobek.Undefined()))
		} else {
			must(runtime, input.Set("credential", toJS(runtime, credential)))
		}
		value, err := method.check(method.owner, input)
		if err != nil {
			return nil, err
		}
		value, err = method.vm.awaitValue(ctx, runtime, value)
		if err != nil || !present(value) {
			return nil, err
		}
		var check aiauth.AuthCheck
		if err := decodeJSON(runtime, value, &check); err != nil {
			return nil, err
		}
		return &check, nil
	})
	if err != nil || result == nil {
		return nil, err
	}
	return result.(*aiauth.AuthCheck), nil
}

func (method *jsAPIKeyAuth) Resolve(ctx context.Context, authContext aiauth.AuthContext, credential *aiauth.Credential) (*aiauth.AuthResult, error) {
	result, err := method.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		input := runtime.NewObject()
		must(runtime, input.Set("ctx", newProviderAuthContext(runtime, method.vm, ctx, authContext)))
		if credential == nil {
			must(runtime, input.Set("credential", sobek.Undefined()))
		} else {
			must(runtime, input.Set("credential", toJS(runtime, credential)))
		}
		value, err := method.resolve(method.owner, input)
		if err != nil {
			return nil, err
		}
		value, err = method.vm.awaitValue(ctx, runtime, value)
		if err != nil || !present(value) {
			return nil, err
		}
		var resolved aiauth.AuthResult
		if err := decodeJSON(runtime, value, &resolved); err != nil {
			return nil, err
		}
		return &resolved, nil
	})
	if err != nil || result == nil {
		return nil, err
	}
	return result.(*aiauth.AuthResult), nil
}

func (method *jsAPIKeyAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	if method.login == nil {
		return nil, fmt.Errorf("api-key login is unavailable")
	}
	result, err := method.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		value, err := method.login(method.owner, newAuthInteraction(runtime, method.vm, ctx, interaction))
		if err != nil {
			return nil, err
		}
		value, err = method.vm.awaitValue(ctx, runtime, value)
		if err != nil {
			return nil, err
		}
		var credential aiauth.Credential
		if err := decodeJSON(runtime, value, &credential); err != nil {
			return nil, err
		}
		return &credential, nil
	})
	if err != nil {
		return nil, err
	}
	return result.(*aiauth.Credential), nil
}

type jsOAuthAuth struct {
	vm      *runtimeVM
	owner   *sobek.Object
	name    string
	login   sobek.Callable
	refresh sobek.Callable
	toAuth  sobek.Callable
}

func (method *jsOAuthAuth) Name() string { return method.name }

func (method *jsOAuthAuth) Login(ctx context.Context, interaction aiauth.AuthInteraction) (*aiauth.Credential, error) {
	result, err := method.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		value, err := method.login(method.owner, newAuthInteraction(runtime, method.vm, ctx, interaction))
		if err != nil {
			return nil, err
		}
		return decodeCredentialPromise(ctx, runtime, method.vm, value)
	})
	if err != nil {
		return nil, err
	}
	return result.(*aiauth.Credential), nil
}

func (method *jsOAuthAuth) Refresh(ctx context.Context, credential *aiauth.Credential) (*aiauth.Credential, error) {
	result, err := method.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		signal, signalErr := newAbortSignal(runtime, method.vm, ctx)
		if signalErr != nil {
			return nil, signalErr
		}
		value, err := method.refresh(method.owner, toJS(runtime, credential), signal)
		if err != nil {
			return nil, err
		}
		return decodeCredentialPromise(ctx, runtime, method.vm, value)
	})
	if err != nil {
		return nil, err
	}
	return result.(*aiauth.Credential), nil
}

func (method *jsOAuthAuth) ToAuth(credential *aiauth.Credential) (aiauth.ModelAuth, error) {
	result, err := method.vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
		value, err := method.toAuth(method.owner, toJS(runtime, credential))
		if err != nil {
			return nil, err
		}
		value, err = method.vm.awaitValue(context.Background(), runtime, value)
		if err != nil {
			return nil, err
		}
		var auth aiauth.ModelAuth
		if err := decodeJSON(runtime, value, &auth); err != nil {
			return nil, err
		}
		return auth, nil
	})
	if err != nil {
		return aiauth.ModelAuth{}, err
	}
	return result.(aiauth.ModelAuth), nil
}

func decodeCredentialPromise(ctx context.Context, runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (*aiauth.Credential, error) {
	value, err := vm.awaitValue(ctx, runtime, value)
	if err != nil {
		return nil, err
	}
	var credential aiauth.Credential
	if err := decodeJSON(runtime, value, &credential); err != nil {
		return nil, err
	}
	return &credential, nil
}

func newProviderAuthContext(runtime *sobek.Runtime, vm *runtimeVM, ctx context.Context, authContext aiauth.AuthContext) sobek.Value {
	object := runtime.NewObject()
	must(runtime, object.Set("env", func(call sobek.FunctionCall) sobek.Value {
		name := call.Argument(0).String()
		return vm.promise(runtime, ctx, func(context.Context) (any, error) {
			value, ok := authContext.Env(ctx, name)
			if !ok {
				return promiseUndefined, nil
			}
			return value, nil
		})
	}))
	must(runtime, object.Set("fileExists", func(call sobek.FunctionCall) sobek.Value {
		path := call.Argument(0).String()
		return vm.promise(runtime, ctx, func(context.Context) (any, error) {
			return authContext.FileExists(ctx, path), nil
		})
	}))
	return object
}

func newAuthInteraction(runtime *sobek.Runtime, vm *runtimeVM, ctx context.Context, interaction aiauth.AuthInteraction) sobek.Value {
	object := runtime.NewObject()
	signal, err := newAbortSignal(runtime, vm, ctx)
	must(runtime, err)
	must(runtime, object.Set("signal", signal))
	must(runtime, object.Set("prompt", func(call sobek.FunctionCall) sobek.Value {
		var prompt aiauth.AuthPrompt
		must(runtime, decodeJSON(runtime, call.Argument(0), &prompt))
		return vm.promise(runtime, ctx, func(context.Context) (any, error) { return interaction.Prompt(ctx, prompt) })
	}))
	must(runtime, object.Set("notify", func(call sobek.FunctionCall) sobek.Value {
		var event aiauth.AuthEvent
		must(runtime, decodeJSON(runtime, call.Argument(0), &event))
		interaction.Notify(event)
		return sobek.Undefined()
	}))
	return object
}

func refreshModelsCallback(
	vm *runtimeVM,
	owner *sobek.Object,
	callback sobek.Callable,
) func(extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
	return func(refreshContext extensions.RefreshModelsContext) ([]extensions.ProviderModelConfig, error) {
		ctx := refreshContext.Signal
		if ctx == nil {
			ctx = context.Background()
		}
		result, err := vm.callback(ctx, func(runtime *sobek.Runtime) (any, error) {
			value, err := callback(owner, newRefreshModelsContextObject(runtime, vm, refreshContext))
			if err != nil {
				return nil, err
			}
			value, err = vm.awaitValue(ctx, runtime, value)
			if err != nil {
				return nil, err
			}
			var models []extensions.ProviderModelConfig
			if err := decodeJSON(runtime, value, &models); err != nil {
				return nil, err
			}
			return models, nil
		})
		if err != nil {
			return nil, err
		}
		return result.([]extensions.ProviderModelConfig), nil
	}
}

func newRefreshModelsContextObject(runtime *sobek.Runtime, vm *runtimeVM, value extensions.RefreshModelsContext) sobek.Value {
	object := runtime.NewObject()
	credential := sobek.Value(sobek.Undefined())
	if value.Credential != nil {
		credential = toJS(runtime, value.Credential)
	}
	must(runtime, object.Set("credential", credential))
	must(runtime, object.Set("allowNetwork", value.AllowNetwork))
	must(runtime, object.Set("force", value.Force))
	signal, err := newAbortSignal(runtime, vm, value.Signal)
	must(runtime, err)
	must(runtime, object.Set("signal", signal))
	store := runtime.NewObject()
	if value.Store != nil {
		must(runtime, store.Set("read", func(sobek.FunctionCall) sobek.Value {
			return vm.promise(runtime, vm.context(), func(ctx context.Context) (any, error) {
				entry, err := value.Store.Read(ctx)
				if entry == nil && err == nil {
					return promiseUndefined, nil
				}
				return entry, err
			})
		}))
		must(runtime, store.Set("write", func(call sobek.FunctionCall) sobek.Value {
			var entry extensions.ProviderModelsStoreEntry
			must(runtime, decodeJSON(runtime, call.Argument(0), &entry))
			return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
				return value.Store.Write(ctx, entry)
			})
		}))
		must(runtime, store.Set("delete", func(sobek.FunctionCall) sobek.Value {
			return vm.promiseVoid(runtime, vm.context(), func(ctx context.Context) error {
				return value.Store.Delete(ctx)
			})
		}))
	}
	must(runtime, object.Set("store", store))
	return object
}

func decodeOAuthProvider(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (*extensions.OAuthProvider, error) {
	object := value.ToObject(runtime)
	login, loginOK := sobek.AssertFunction(object.Get("login"))
	refresh, refreshOK := sobek.AssertFunction(object.Get("refreshToken"))
	getAPIKey, keyOK := sobek.AssertFunction(object.Get("getApiKey"))
	if !loginOK || !refreshOK || !keyOK {
		return nil, fmt.Errorf("oauth provider requires login, refreshToken, and getApiKey callbacks")
	}
	oauth := &extensions.OAuthProvider{Name: stringProperty(object, "name")}
	oauth.Login = func(ctx context.Context, callbacks extensions.OAuthLoginCallbacks) (extensions.OAuthCredentials, error) {
		result, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			value, err := login(object, newOAuthCallbacksObject(runtime, vm, callbacks))
			if err != nil {
				return nil, err
			}
			value, err = vm.awaitValue(ctx, runtime, value)
			if err != nil {
				return nil, err
			}
			var credentials extensions.OAuthCredentials
			if err := decodeJSON(runtime, value, &credentials); err != nil {
				return nil, err
			}
			return credentials, nil
		})
		if err != nil {
			return extensions.OAuthCredentials{}, err
		}
		return result.(extensions.OAuthCredentials), nil
	}
	oauth.RefreshToken = func(ctx context.Context, credentials extensions.OAuthCredentials) (extensions.OAuthCredentials, error) {
		result, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			value, err := refresh(object, toJS(runtime, credentials))
			if err != nil {
				return nil, err
			}
			value, err = vm.awaitValue(ctx, runtime, value)
			if err != nil {
				return nil, err
			}
			var refreshed extensions.OAuthCredentials
			if err := decodeJSON(runtime, value, &refreshed); err != nil {
				return nil, err
			}
			return refreshed, nil
		})
		if err != nil {
			return extensions.OAuthCredentials{}, err
		}
		return result.(extensions.OAuthCredentials), nil
	}
	oauth.GetAPIKey = func(credentials extensions.OAuthCredentials) (string, error) {
		result, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
			value, err := getAPIKey(object, toJS(runtime, credentials))
			if err != nil {
				return nil, err
			}
			return value.String(), nil
		})
		if err != nil {
			return "", err
		}
		return result.(string), nil
	}
	if modify, ok := sobek.AssertFunction(object.Get("modifyModels")); ok {
		oauth.ModifyModels = func(models []ai.Model, credentials extensions.OAuthCredentials) ([]ai.Model, error) {
			result, err := vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
				value, err := modify(object, toJS(runtime, models), toJS(runtime, credentials))
				if err != nil {
					return nil, err
				}
				var modified []ai.Model
				if err := decodeJSON(runtime, value, &modified); err != nil {
					return nil, err
				}
				return modified, nil
			})
			if err != nil {
				return nil, err
			}
			return result.([]ai.Model), nil
		}
	}
	return oauth, nil
}

func newOAuthCallbacksObject(runtime *sobek.Runtime, vm *runtimeVM, callbacks extensions.OAuthLoginCallbacks) sobek.Value {
	object := runtime.NewObject()
	signal, err := newAbortSignal(runtime, vm, callbacks.Signal)
	must(runtime, err)
	must(runtime, object.Set("signal", signal))
	must(runtime, object.Set("onAuth", func(call sobek.FunctionCall) sobek.Value {
		if callbacks.OnAuth == nil {
			return sobek.Undefined()
		}
		var info extensions.OAuthAuthInfo
		must(runtime, decodeJSON(runtime, call.Argument(0), &info))
		callbacks.OnAuth(info)
		return sobek.Undefined()
	}))
	must(runtime, object.Set("onDeviceCode", func(call sobek.FunctionCall) sobek.Value {
		if callbacks.OnDeviceCode == nil {
			return sobek.Undefined()
		}
		var info extensions.OAuthDeviceCodeInfo
		must(runtime, decodeJSON(runtime, call.Argument(0), &info))
		callbacks.OnDeviceCode(info)
		return sobek.Undefined()
	}))
	must(runtime, object.Set("onPrompt", func(call sobek.FunctionCall) sobek.Value {
		var prompt extensions.OAuthPrompt
		must(runtime, decodeJSON(runtime, call.Argument(0), &prompt))
		return vm.promise(runtime, callbacks.Signal, func(context.Context) (any, error) {
			if callbacks.OnPrompt == nil {
				return "", nil
			}
			return callbacks.OnPrompt(prompt)
		})
	}))
	must(runtime, object.Set("onProgress", func(call sobek.FunctionCall) sobek.Value {
		if callbacks.OnProgress != nil {
			callbacks.OnProgress(call.Argument(0).String())
		}
		return sobek.Undefined()
	}))
	must(runtime, object.Set("onManualCodeInput", func(sobek.FunctionCall) sobek.Value {
		return vm.promise(runtime, callbacks.Signal, func(context.Context) (any, error) {
			if callbacks.OnManualCodeInput == nil {
				return "", nil
			}
			return callbacks.OnManualCodeInput()
		})
	}))
	must(runtime, object.Set("onSelect", func(call sobek.FunctionCall) sobek.Value {
		var prompt extensions.OAuthSelectPrompt
		must(runtime, decodeJSON(runtime, call.Argument(0), &prompt))
		return vm.promise(runtime, callbacks.Signal, func(context.Context) (any, error) {
			if callbacks.OnSelect == nil {
				return promiseUndefined, nil
			}
			selected, err := callbacks.OnSelect(prompt)
			if selected == nil && err == nil {
				return promiseUndefined, nil
			}
			return selected, err
		})
	}))
	return object
}

type jsStreamIterator struct{ object *sobek.Object }

func streamCallback(vm *runtimeVM, owner *sobek.Object, callback sobek.Callable) agent.StreamFn {
	return func(
		ctx context.Context,
		model *ai.Model,
		requestContext ai.Context,
		options *ai.SimpleStreamOptions,
	) (ai.AssistantMessageEventStream, error) {
		created, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsOptions := sobek.Value(sobek.Undefined())
			if options != nil {
				jsOptions = toJS(runtime, options)
				signal, signalErr := newAbortSignal(runtime, vm, ctx)
				if signalErr != nil {
					return nil, signalErr
				}
				must(runtime, jsOptions.ToObject(runtime).Set("signal", signal))
			}
			value, err := callback(owner, toJS(runtime, model), toJS(runtime, requestContext), jsOptions)
			if err != nil {
				return nil, err
			}
			value, err = vm.awaitValue(ctx, runtime, value)
			if err != nil {
				return nil, err
			}
			iterator, err := asyncIterator(runtime, value)
			if err != nil {
				return nil, err
			}
			return jsStreamIterator{object: iterator}, nil
		})
		if err != nil {
			return nil, err
		}
		iterator := created.(jsStreamIterator)
		return iter.Seq2[ai.AssistantMessageEvent, error](func(yield func(ai.AssistantMessageEvent, error) bool) {
			completed := false
			defer func() {
				if !completed {
					closeStreamIterator(vm, iterator)
				}
			}()
			for {
				next, nextErr := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
					call, ok := sobek.AssertFunction(iterator.object.Get("next"))
					if !ok {
						return nil, fmt.Errorf("provider stream iterator has no next function")
					}
					value, err := call(iterator.object)
					if err != nil {
						return nil, err
					}
					value, err = vm.awaitValue(ctx, runtime, value)
					if err != nil {
						return nil, err
					}
					object := value.ToObject(runtime)
					if object.Get("done").ToBoolean() {
						return nil, nil
					}
					return decodeAssistantEvent(runtime, object.Get("value"))
				})
				if nextErr != nil {
					yield(nil, nextErr)
					return
				}
				if next == nil {
					completed = true
					return
				}
				if !yield(next.(ai.AssistantMessageEvent), nil) {
					return
				}
			}
		}), nil
	}
}

func closeStreamIterator(vm *runtimeVM, iterator jsStreamIterator) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _ = vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		closeIterator, ok := sobek.AssertFunction(iterator.object.Get("return"))
		if !ok {
			return nil, nil
		}
		value, err := closeIterator(iterator.object)
		if err != nil {
			return nil, err
		}
		_, err = vm.awaitValue(ctx, runtime, value)
		return nil, err
	})
}

func asyncIterator(runtime *sobek.Runtime, value sobek.Value) (*sobek.Object, error) {
	object := value.ToObject(runtime)
	if _, ok := sobek.AssertFunction(object.Get("next")); ok {
		return object, nil
	}
	symbolValue := runtime.Get("Symbol").ToObject(runtime).Get("asyncIterator")
	symbol, ok := symbolValue.(*sobek.Symbol)
	if !ok {
		return nil, fmt.Errorf("Symbol.asyncIterator is unavailable")
	}
	iteratorMethod, ok := sobek.AssertFunction(object.GetSymbol(symbol))
	if !ok {
		return nil, fmt.Errorf("provider stream is not async iterable")
	}
	iterator, err := iteratorMethod(object)
	if err != nil {
		return nil, err
	}
	return iterator.ToObject(runtime), nil
}
