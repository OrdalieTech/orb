package jsbridge

import (
	"context"
	"fmt"

	"github.com/OrdalieTech/pi-go/agent"
	"github.com/OrdalieTech/pi-go/ai"
	"github.com/OrdalieTech/pi-go/codingagent/extensions"
	"github.com/OrdalieTech/pi-go/internal/jsonschema"
	"github.com/grafana/sobek"
)

func registerHandler(
	runtime *sobek.Runtime,
	vm *runtimeVM,
	api extensions.API,
	eventType extensions.EventType,
	value sobek.Value,
) error {
	handler, ok := sobek.AssertFunction(value)
	if !ok {
		return fmt.Errorf("handler for %q is not a function", eventType)
	}
	api.On(eventType, func(ctx context.Context, event extensions.Event, contextValue extensions.Context) (any, error) {
		return vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsEvent, err := eventValue(runtime, vm, event)
			if err != nil {
				return nil, err
			}
			jsContext, err := newContextObject(runtime, vm, contextValue)
			if err != nil {
				return nil, err
			}
			result, err := handler(sobek.Undefined(), jsEvent, jsContext)
			if err != nil {
				return nil, err
			}
			result, err = vm.awaitValue(ctx, runtime, result)
			if err != nil {
				return nil, err
			}
			if err := syncMutableEvent(runtime, event, jsEvent); err != nil {
				return nil, err
			}
			return decodeEventResult(runtime, vm, event, jsEvent, result)
		})
	})
	return nil
}

func registerTool(runtime *sobek.Runtime, vm *runtimeVM, api extensions.API, value sobek.Value) error {
	if !present(value) {
		return fmt.Errorf("registerTool requires a tool definition")
	}
	object := value.ToObject(runtime)
	execute, ok := sobek.AssertFunction(object.Get("execute"))
	if !ok {
		return fmt.Errorf("tool %q has no execute function", object.Get("name").String())
	}
	parameters, err := stringifyJSON(runtime, object.Get("parameters"))
	if err != nil {
		return fmt.Errorf("tool %q parameters: %w", object.Get("name").String(), err)
	}
	definition := extensions.ToolDefinition{
		Name:          stringProperty(object, "name"),
		Label:         stringProperty(object, "label"),
		Description:   stringProperty(object, "description"),
		PromptSnippet: stringProperty(object, "promptSnippet"),
		Parameters:    jsonschema.Schema(parameters),
		RenderShell:   extensions.RenderShell(stringProperty(object, "renderShell")),
		ExecutionMode: agent.ToolExecutionMode(stringProperty(object, "executionMode")),
	}
	if guidelines := object.Get("promptGuidelines"); present(guidelines) {
		if err := decodeJSON(runtime, guidelines, &definition.PromptGuidelines); err != nil {
			return fmt.Errorf("tool %q promptGuidelines: %w", definition.Name, err)
		}
	}
	if prepare, ok := sobek.AssertFunction(object.Get("prepareArguments")); ok {
		definition.PrepareArguments = func(args any) (any, error) {
			return vm.do(context.Background(), func(runtime *sobek.Runtime) (any, error) {
				result, err := prepare(object, toJS(runtime, args))
				if err != nil {
					return nil, err
				}
				if _, async := result.Export().(*sobek.Promise); async {
					return nil, fmt.Errorf("tool %q prepareArguments must return synchronously", definition.Name)
				}
				return result.Export(), nil
			})
		}
	}
	definition.Execute = func(
		ctx context.Context,
		toolCallID string,
		params any,
		onUpdate agent.AgentToolUpdateCallback,
		contextValue extensions.Context,
	) (agent.AgentToolResult, error) {
		result, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsContext, err := newContextObject(runtime, vm, contextValue)
			if err != nil {
				return nil, err
			}
			signal, err := newAbortSignal(runtime, vm, ctx)
			if err != nil {
				return nil, err
			}
			updateValue := sobek.Value(sobek.Undefined())
			if onUpdate != nil {
				updateValue = runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
					update, decodeErr := decodeToolResult(runtime, call.Argument(0))
					must(runtime, decodeErr)
					onUpdate(update)
					return sobek.Undefined()
				})
			}
			value, err := execute(
				object,
				runtime.ToValue(toolCallID),
				toJS(runtime, params),
				signal,
				updateValue,
				jsContext,
			)
			if err != nil {
				return nil, err
			}
			value, err = vm.awaitValue(ctx, runtime, value)
			if err != nil {
				return nil, err
			}
			return decodeToolResult(runtime, value)
		})
		if err != nil {
			return agent.AgentToolResult{}, err
		}
		return result.(agent.AgentToolResult), nil
	}
	api.RegisterTool(definition)
	return nil
}

func decodeToolResult(runtime *sobek.Runtime, value sobek.Value) (agent.AgentToolResult, error) {
	if !present(value) {
		return agent.AgentToolResult{}, nil
	}
	object := value.ToObject(runtime)
	result := agent.AgentToolResult{}
	if content := object.Get("content"); present(content) {
		decoded, err := decodeToolContent(runtime, content)
		if err != nil {
			return result, err
		}
		result.Content = decoded
	}
	if details := object.Get("details"); present(details) || objectHas(object, "details") {
		result.Details = details.Export()
	}
	if terminate := object.Get("terminate"); present(terminate) {
		value := terminate.ToBoolean()
		result.Terminate = &value
	}
	if names := object.Get("addedToolNames"); present(names) {
		var decoded []string
		if err := decodeJSON(runtime, names, &decoded); err != nil {
			return result, err
		}
		result.AddedToolNames = &decoded
	}
	return result, nil
}

func registerCommand(
	runtime *sobek.Runtime,
	vm *runtimeVM,
	api extensions.API,
	name string,
	value sobek.Value,
) error {
	if !present(value) {
		return fmt.Errorf("registerCommand requires a command definition")
	}
	object := value.ToObject(runtime)
	handler, ok := sobek.AssertFunction(object.Get("handler"))
	if !ok {
		return fmt.Errorf("command %q has no handler function", name)
	}
	command := extensions.Command{Description: stringProperty(object, "description")}
	if completions, ok := sobek.AssertFunction(object.Get("getArgumentCompletions")); ok {
		command.GetArgumentCompletions = func(ctx context.Context, prefix string) ([]extensions.AutocompleteItem, error) {
			value, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
				result, err := completions(object, runtime.ToValue(prefix))
				if err != nil {
					return nil, err
				}
				result, err = vm.awaitValue(ctx, runtime, result)
				if err != nil || !present(result) {
					return nil, err
				}
				var items []extensions.AutocompleteItem
				if err := decodeJSON(runtime, result, &items); err != nil {
					return nil, err
				}
				return items, nil
			})
			if err != nil || value == nil {
				return nil, err
			}
			return value.([]extensions.AutocompleteItem), nil
		}
	}
	command.Handler = func(ctx context.Context, arguments string, contextValue extensions.CommandContext) error {
		_, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsContext, err := newCommandContextObject(runtime, vm, contextValue)
			if err != nil {
				return nil, err
			}
			result, err := handler(object, runtime.ToValue(arguments), jsContext)
			if err != nil {
				return nil, err
			}
			_, err = vm.awaitValue(ctx, runtime, result)
			return nil, err
		})
		return err
	}
	api.RegisterCommand(name, command)
	return nil
}

func registerShortcut(runtime *sobek.Runtime, vm *runtimeVM, api extensions.API, shortcut string, value sobek.Value) error {
	if !present(value) {
		return fmt.Errorf("registerShortcut requires a shortcut definition")
	}
	object := value.ToObject(runtime)
	handler, ok := sobek.AssertFunction(object.Get("handler"))
	if !ok {
		return fmt.Errorf("shortcut %q has no handler function", shortcut)
	}
	definition := extensions.Shortcut{Description: stringProperty(object, "description")}
	definition.Handler = func(ctx context.Context, contextValue extensions.Context) error {
		_, err := vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
			jsContext, err := newContextObject(runtime, vm, contextValue)
			if err != nil {
				return nil, err
			}
			result, err := handler(object, jsContext)
			if err != nil {
				return nil, err
			}
			_, err = vm.awaitValue(ctx, runtime, result)
			return nil, err
		})
		return err
	}
	api.RegisterShortcut(shortcut, definition)
	return nil
}

func registerFlag(runtime *sobek.Runtime, api extensions.API, name string, value sobek.Value) error {
	if !present(value) {
		return fmt.Errorf("registerFlag requires a flag definition")
	}
	object := value.ToObject(runtime)
	flag := extensions.Flag{
		Description: stringProperty(object, "description"),
		Type:        extensions.FlagType(stringProperty(object, "type")),
	}
	if defaultValue := object.Get("default"); present(defaultValue) {
		flag.Default = defaultValue.Export()
	}
	api.RegisterFlag(name, flag)
	return nil
}

func decodeAssistantEvent(runtime *sobek.Runtime, value sobek.Value) (ai.AssistantMessageEvent, error) {
	encoded, err := stringifyJSON(runtime, value)
	if err != nil {
		return nil, err
	}
	return ai.UnmarshalAssistantMessageEvent(encoded)
}
