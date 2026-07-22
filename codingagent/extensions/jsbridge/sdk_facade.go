package jsbridge

import (
	"context"
	"fmt"
	"strconv"

	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/grafana/sobek"
)

type sdkFacade struct {
	vm       *runtimeVM
	loaders  map[*sobek.Object]*codingagent.DefaultResourceLoader
	managers map[*sobek.Object]*session.SessionManager
}

func installSDKFacade(runtime *sobek.Runtime, vm *runtimeVM, codingModule *sobek.Object) error {
	facade := &sdkFacade{
		vm:       vm,
		loaders:  make(map[*sobek.Object]*codingagent.DefaultResourceLoader),
		managers: make(map[*sobek.Object]*session.SessionManager),
	}
	if err := codingModule.Set("DefaultResourceLoader", facade.defaultResourceLoaderConstructor(runtime)); err != nil {
		return err
	}
	managerClass, err := runtime.RunString("(class SessionManager {})")
	if err != nil {
		return err
	}
	if err := managerClass.ToObject(runtime).Set("inMemory", facade.inMemorySessionManager(runtime)); err != nil {
		return err
	}
	if err := codingModule.Set("SessionManager", managerClass); err != nil {
		return err
	}
	return codingModule.Set("createAgentSession", func(call sobek.FunctionCall) sobek.Value {
		return facade.createAgentSession(runtime, call.Argument(0))
	})
}

func (facade *sdkFacade) defaultResourceLoaderConstructor(runtime *sobek.Runtime) any {
	return func(call sobek.ConstructorCall) *sobek.Object {
		options, err := decodeResourceLoaderOptions(runtime, call.Argument(0))
		must(runtime, err)
		loader, err := codingagent.NewDefaultResourceLoader(options)
		must(runtime, err)

		facade.loaders[call.This] = loader
		must(runtime, call.This.Set("reload", func(sobek.FunctionCall) sobek.Value {
			return facade.vm.promiseVoid(runtime, facade.vm.context(), func(ctx context.Context) error {
				return loader.Reload(ctx, nil)
			})
		}))
		return nil
	}
}

func decodeResourceLoaderOptions(runtime *sobek.Runtime, value sobek.Value) (codingagent.DefaultResourceLoaderOptions, error) {
	options := codingagent.DefaultResourceLoaderOptions{}
	if !present(value) {
		return options, nil
	}
	object := value.ToObject(runtime)
	options.CWD = stringProperty(object, "cwd")
	options.AgentDir = stringProperty(object, "agentDir")
	if systemPrompt := object.Get("systemPrompt"); present(systemPrompt) {
		text := systemPrompt.String()
		options.SystemPrompt = &text
	}
	if skillPaths := object.Get("additionalSkillPaths"); present(skillPaths) {
		if err := decodeJSON(runtime, skillPaths, &options.AdditionalSkillPaths); err != nil {
			return options, fmt.Errorf("DefaultResourceLoader additionalSkillPaths: %w", err)
		}
	}
	options.NoExtensions = booleanProperty(object, "noExtensions")
	options.NoSkills = booleanProperty(object, "noSkills")
	options.NoPromptTemplates = booleanProperty(object, "noPromptTemplates")
	options.NoThemes = booleanProperty(object, "noThemes")
	options.NoContextFiles = booleanProperty(object, "noContextFiles")
	return options, nil
}

func booleanProperty(object *sobek.Object, name string) bool {
	value := object.Get(name)
	return present(value) && value.ToBoolean()
}

func (facade *sdkFacade) inMemorySessionManager(runtime *sobek.Runtime) any {
	return func(call sobek.FunctionCall) sobek.Value {
		cwd := facade.vm.cwd
		if value := call.Argument(0); present(value) {
			cwd = value.String()
		}
		manager, err := session.InMemory(cwd)
		must(runtime, err)
		object := newWritableSessionManagerObject(runtime, manager).ToObject(runtime)
		facade.managers[object] = manager
		return object
	}
}

func (facade *sdkFacade) createAgentSession(runtime *sobek.Runtime, value sobek.Value) sobek.Value {
	options, err := facade.decodeAgentSessionOptions(runtime, value)
	must(runtime, err)
	ctx := facade.vm.context()
	promise, resolve, reject := runtime.NewPromise()
	go func() {
		result, createErr := codingagent.NewAgentSession(options)
		if !facade.vm.post(ctx, true, func(runtime *sobek.Runtime) error {
			if createErr != nil {
				return reject(runtime.NewGoError(createErr))
			}
			object, objectErr := facade.agentSessionResultValue(runtime, result)
			if objectErr != nil {
				result.Session.Dispose()
				return reject(runtime.NewGoError(objectErr))
			}
			return resolve(object)
		}) && result != nil && result.Session != nil {
			result.Session.Dispose()
		}
	}()
	return runtime.ToValue(promise)
}

func (facade *sdkFacade) decodeAgentSessionOptions(runtime *sobek.Runtime, value sobek.Value) (codingagent.AgentSessionOptions, error) {
	options := codingagent.AgentSessionOptions{}
	if !present(value) {
		return options, nil
	}
	object := value.ToObject(runtime)
	options.CWD = stringProperty(object, "cwd")
	options.AgentDir = stringProperty(object, "agentDir")
	if modelValue := object.Get("model"); present(modelValue) {
		var model ai.Model
		if err := decodeJSON(runtime, modelValue, &model); err != nil {
			return options, fmt.Errorf("createAgentSession model: %w", err)
		}
		options.Model = &model
	}
	if thinkingLevel := object.Get("thinkingLevel"); present(thinkingLevel) {
		options.ThinkingLevel = ai.ModelThinkingLevel(thinkingLevel.String())
	}
	if tools := object.Get("tools"); present(tools) {
		if err := decodeJSON(runtime, tools, &options.Tools); err != nil {
			return options, fmt.Errorf("createAgentSession tools: %w", err)
		}
	}
	if excludeTools := object.Get("excludeTools"); present(excludeTools) {
		if err := decodeJSON(runtime, excludeTools, &options.ExcludeTools); err != nil {
			return options, fmt.Errorf("createAgentSession excludeTools: %w", err)
		}
	}
	if noTools := object.Get("noTools"); present(noTools) {
		options.NoTools = noTools.String()
	}
	if loaderValue := object.Get("resourceLoader"); present(loaderValue) {
		loaderObject, ok := loaderValue.(*sobek.Object)
		if !ok || facade.loaders[loaderObject] == nil {
			return options, fmt.Errorf("createAgentSession resourceLoader was not created by DefaultResourceLoader")
		}
		options.ResourceLoader = facade.loaders[loaderObject]
	}
	if managerValue := object.Get("sessionManager"); present(managerValue) {
		managerObject, ok := managerValue.(*sobek.Object)
		if !ok || facade.managers[managerObject] == nil {
			return options, fmt.Errorf("createAgentSession sessionManager was not created by SessionManager.inMemory")
		}
		options.SessionManager = facade.managers[managerObject]
	}
	if customTools := object.Get("customTools"); present(customTools) {
		array := customTools.ToObject(runtime)
		length := int(array.Get("length").ToInteger())
		options.CustomTools = make([]extensions.ToolDefinition, 0, length)
		for index := 0; index < length; index++ {
			definition, err := decodeToolDefinition(runtime, facade.vm, array.Get(strconv.Itoa(index)))
			if err != nil {
				return options, fmt.Errorf("createAgentSession customTools[%d]: %w", index, err)
			}
			options.CustomTools = append(options.CustomTools, definition)
		}
	}
	// modelRegistry was removed from createAgentSession before upstream v0.81;
	// JavaScript's extra object property is deliberately ignored.
	return options, nil
}

func (facade *sdkFacade) agentSessionResultValue(runtime *sobek.Runtime, result *codingagent.AgentSessionResult) (sobek.Value, error) {
	if result == nil || result.Session == nil {
		return nil, fmt.Errorf("createAgentSession returned no session")
	}
	sessionObject := runtime.NewObject()
	agentObject := runtime.NewObject()
	if err := agentObject.Set("abort", func(sobek.FunctionCall) sobek.Value {
		result.Session.Abort()
		return sobek.Undefined()
	}); err != nil {
		return nil, err
	}
	if err := agentObject.Set("waitForIdle", func(sobek.FunctionCall) sobek.Value {
		return facade.vm.promiseVoid(runtime, facade.vm.context(), result.Session.WaitForIdle)
	}); err != nil {
		return nil, err
	}
	if err := sessionObject.Set("agent", agentObject); err != nil {
		return nil, err
	}
	if err := sessionObject.Set("subscribe", func(call sobek.FunctionCall) sobek.Value {
		listener, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			panic(runtime.NewTypeError("session listener is not a function"))
		}
		unsubscribe := result.Session.Subscribe(func(event any) {
			_, _ = facade.vm.callback(facade.vm.rootCtx, func(runtime *sobek.Runtime) (any, error) {
				jsEvent, err := sessionEventValue(runtime, event)
				if err != nil {
					return nil, err
				}
				_, err = listener(sobek.Undefined(), jsEvent)
				return nil, err
			})
		})
		return runtime.ToValue(func(sobek.FunctionCall) sobek.Value {
			unsubscribe()
			return sobek.Undefined()
		})
	}); err != nil {
		return nil, err
	}
	if err := sessionObject.Set("prompt", func(call sobek.FunctionCall) sobek.Value {
		text := call.Argument(0).String()
		return facade.vm.promiseVoid(runtime, facade.vm.context(), func(ctx context.Context) error {
			return result.Session.Prompt(ctx, text)
		})
	}); err != nil {
		return nil, err
	}
	if err := sessionObject.Set("dispose", func(sobek.FunctionCall) sobek.Value {
		result.Session.Dispose()
		return sobek.Undefined()
	}); err != nil {
		return nil, err
	}
	resultObject := runtime.NewObject()
	if err := resultObject.Set("session", sessionObject); err != nil {
		return nil, err
	}
	if result.ModelFallbackMessage != "" {
		if err := resultObject.Set("modelFallbackMessage", result.ModelFallbackMessage); err != nil {
			return nil, err
		}
	}
	return resultObject, nil
}

func sessionEventValue(runtime *sobek.Runtime, event any) (sobek.Value, error) {
	encoded, err := codingagent.MarshalSessionEvent(event)
	if err != nil {
		return nil, err
	}
	jsonObject := runtime.Get("JSON").ToObject(runtime)
	parse, ok := sobek.AssertFunction(jsonObject.Get("parse"))
	if !ok {
		return nil, fmt.Errorf("JSON.parse is unavailable")
	}
	return parse(jsonObject, runtime.ToValue(string(encoded)))
}
