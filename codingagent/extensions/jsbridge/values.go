package jsbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/OrdalieTech/pigo/agent"
	"github.com/OrdalieTech/pigo/agent/harness"
	"github.com/OrdalieTech/pigo/ai"
	"github.com/OrdalieTech/pigo/codingagent/extensions"
	"github.com/OrdalieTech/pigo/codingagent/session"
	"github.com/OrdalieTech/pigo/codingagent/tools"
	"github.com/grafana/sobek"
)

func eventValue(runtime *sobek.Runtime, vm *runtimeVM, event extensions.Event) (sobek.Value, error) {
	value, deferred, err := eventWireValue(event)
	if err != nil {
		return nil, err
	}
	object, ok := value.(map[string]any)
	if !ok {
		object = make(map[string]any)
	}
	object["type"] = string(event.Type())
	normalizeEventObject(event, object)
	jsEvent, err := plainJS(runtime, object)
	if err != nil {
		return nil, err
	}
	for name, deferredValue := range deferred {
		if err := defineLazyMutable(runtime, jsEvent.ToObject(runtime), name, deferredValue); err != nil {
			return nil, err
		}
	}
	if toolResultDetailsUndefined(event) {
		if err := jsEvent.ToObject(runtime).Set("details", sobek.Undefined()); err != nil {
			return nil, err
		}
	}
	if signal := eventSignal(event); signal != nil {
		abortSignal, signalErr := newAbortSignal(runtime, vm, signal)
		if signalErr != nil {
			return nil, signalErr
		}
		if err := jsEvent.ToObject(runtime).Set("signal", abortSignal); err != nil {
			return nil, err
		}
	}
	return jsEvent, nil
}

func eventWireValue(event extensions.Event) (any, map[string]any, error) {
	deferred := make(map[string]any)
	shallow := event
	switch typed := event.(type) {
	case extensions.SessionBeforeCompactEvent:
		copy := typed
		copy.Preparation = harness.CompactionPreparation{}
		copy.BranchEntries = nil
		shallow = copy
		preparation, preparationErr := wireValue(typed.Preparation)
		if preparationErr != nil {
			return nil, nil, preparationErr
		}
		if object, ok := preparation.(map[string]any); ok {
			delete(object, "retainedTail")
		}
		deferred["preparation"] = preparation
		deferred["branchEntries"] = nonNilSlice(typed.BranchEntries)
	case extensions.SessionCompactEvent:
		copy := typed
		copy.CompactionEntry = session.SessionEntry{}
		shallow = copy
		deferred["compactionEntry"] = typed.CompactionEntry
	case extensions.SessionBeforeTreeEvent:
		copy := typed
		copy.Preparation = extensions.TreePreparation{}
		shallow = copy
		preparation, preparationErr := wireValue(typed.Preparation)
		if preparationErr != nil {
			return nil, nil, preparationErr
		}
		if object, ok := preparation.(map[string]any); ok {
			for _, name := range []string{"oldLeafId", "commonAncestorId", "customInstructions", "label"} {
				if object[name] == nil {
					delete(object, name)
				}
			}
			if typed.Preparation.EntriesToSummarize == nil {
				object["entriesToSummarize"] = []any{}
			}
		}
		deferred["preparation"] = preparation
	case extensions.SessionTreeEvent:
		copy := typed
		copy.SummaryEntry = nil
		shallow = copy
		if typed.SummaryEntry != nil {
			deferred["summaryEntry"] = typed.SummaryEntry
		}
	case extensions.ContextEvent:
		copy := typed
		copy.Messages = nil
		shallow = copy
		deferred["messages"] = nonNilSlice(typed.Messages)
	case extensions.AgentEndEvent:
		copy := typed
		copy.Messages = nil
		shallow = copy
		deferred["messages"] = nonNilSlice(typed.Messages)
	case extensions.TurnEndEvent:
		copy := typed
		copy.Message = nil
		copy.ToolResults = nil
		shallow = copy
		deferred["message"] = typed.Message
		deferred["toolResults"] = nonNilSlice(typed.ToolResults)
	case extensions.MessageStartEvent:
		copy := typed
		copy.Message = nil
		shallow = copy
		deferred["message"] = typed.Message
	case extensions.MessageUpdateEvent:
		copy := typed
		copy.Message = nil
		copy.AssistantMessageEvent = nil
		shallow = copy
		deferred["message"] = typed.Message
		deferred["assistantMessageEvent"] = typed.AssistantMessageEvent
	case extensions.MessageEndEvent:
		copy := typed
		copy.Message = nil
		shallow = copy
		deferred["message"] = typed.Message
	case extensions.ModelSelectEvent:
		copy := typed
		copy.Model = nil
		copy.PreviousModel = nil
		shallow = copy
		if typed.Model != nil {
			deferred["model"] = typed.Model
		}
		if typed.PreviousModel != nil {
			deferred["previousModel"] = typed.PreviousModel
		}
	}
	value, err := wireValue(shallow)
	return value, deferred, err
}

func nonNilSlice[T any](value []T) []T {
	if value == nil {
		return []T{}
	}
	return value
}

func defineLazyMutable(runtime *sobek.Runtime, object *sobek.Object, name string, value any) error {
	var cached sobek.Value
	return object.DefineAccessorProperty(
		name,
		runtime.ToValue(func(sobek.FunctionCall) sobek.Value {
			if cached == nil {
				cached = toJS(runtime, value)
			}
			return cached
		}),
		runtime.ToValue(func(call sobek.FunctionCall) sobek.Value {
			cached = call.Argument(0)
			return sobek.Undefined()
		}),
		sobek.FLAG_TRUE,
		sobek.FLAG_TRUE,
	)
}

func normalizeEventObject(event extensions.Event, object map[string]any) {
	deleteNil := func(names ...string) {
		for _, name := range names {
			if object[name] == nil {
				delete(object, name)
			}
		}
	}
	switch typed := event.(type) {
	case extensions.SessionStartEvent:
		deleteNil("previousSessionFile")
	case extensions.SessionInfoChangedEvent:
		deleteNil("name")
	case extensions.SessionBeforeSwitchEvent:
		deleteNil("targetSessionFile")
	case extensions.SessionBeforeCompactEvent:
		deleteNil("customInstructions")
		if typed.BranchEntries == nil {
			object["branchEntries"] = []any{}
		}
	case extensions.SessionShutdownEvent:
		deleteNil("targetSessionFile")
	case extensions.SessionBeforeTreeEvent:
		if preparation, ok := object["preparation"].(map[string]any); ok {
			for _, name := range []string{"customInstructions", "label"} {
				if preparation[name] == nil {
					delete(preparation, name)
				}
			}
			if typed.Preparation.EntriesToSummarize == nil {
				preparation["entriesToSummarize"] = []any{}
			}
		}
	case extensions.SessionTreeEvent:
		deleteNil("summaryEntry", "fromExtension")
	case extensions.ContextEvent:
		if typed.Messages == nil {
			object["messages"] = []any{}
		}
	case extensions.BeforeAgentStartEvent:
		deleteNil("images")
	case extensions.AgentEndEvent:
		if typed.Messages == nil {
			object["messages"] = []any{}
		}
	case extensions.TurnEndEvent:
		if typed.ToolResults == nil {
			object["toolResults"] = []any{}
		}
	case extensions.ModelSelectEvent:
		deleteNil("previousModel")
	case extensions.ToolResultEvent:
		if typed.Input == nil {
			object["input"] = map[string]any{}
		}
		if typed.Content == nil {
			object["content"] = []any{}
		}
		delete(object, "details")
		deleteNil("usage")
	case extensions.InputEvent:
		deleteNil("images", "streamingBehavior")
	}
}

func toolResultDetailsUndefined(event extensions.Event) bool {
	switch typed := event.(type) {
	case extensions.ToolResultEvent:
		return typed.Details == nil
	case *extensions.ToolResultEvent:
		return typed != nil && typed.Details == nil
	default:
		return false
	}
}

func eventSignal(event extensions.Event) context.Context {
	switch typed := event.(type) {
	case extensions.SessionBeforeCompactEvent:
		return typed.Signal
	case *extensions.SessionBeforeCompactEvent:
		return typed.Signal
	case extensions.SessionBeforeTreeEvent:
		return typed.Signal
	case *extensions.SessionBeforeTreeEvent:
		return typed.Signal
	default:
		return nil
	}
}

func wireValue(value any) (any, error) {
	return wireReflect(reflect.ValueOf(value))
}

func wireReflect(value reflect.Value) (any, error) {
	if !value.IsValid() {
		return nil, nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, nil
		}
		return wireReflect(value.Elem())
	}
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil, nil
		}
		if value.CanInterface() {
			if marshaler, ok := value.Interface().(json.Marshaler); ok {
				return marshalToAny(marshaler)
			}
		}
		return wireReflect(value.Elem())
	}
	if value.CanInterface() {
		if marshaler, ok := value.Interface().(json.Marshaler); ok {
			return marshalToAny(marshaler)
		}
	}
	switch value.Kind() {
	case reflect.Bool:
		return value.Bool(), nil
	case reflect.String:
		return value.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return value.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return value.Uint(), nil
	case reflect.Float32, reflect.Float64:
		return value.Float(), nil
	case reflect.Slice, reflect.Array:
		if value.Kind() == reflect.Slice && value.IsNil() {
			return nil, nil
		}
		items := make([]any, value.Len())
		for index := range items {
			item, err := wireReflect(value.Index(index))
			if err != nil {
				return nil, err
			}
			items[index] = item
		}
		return items, nil
	case reflect.Map:
		if value.IsNil() {
			return nil, nil
		}
		object := make(map[string]any, value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			item, err := wireReflect(iterator.Value())
			if err != nil {
				return nil, err
			}
			object[fmt.Sprint(iterator.Key().Interface())] = item
		}
		return object, nil
	case reflect.Struct:
		typeInfo := value.Type()
		object := make(map[string]any)
		for index := 0; index < value.NumField(); index++ {
			fieldInfo := typeInfo.Field(index)
			if fieldInfo.PkgPath != "" {
				continue
			}
			if fieldInfo.Anonymous {
				item, err := wireReflect(value.Field(index))
				if err != nil {
					return nil, err
				}
				if embedded, ok := item.(map[string]any); ok {
					for name, embeddedValue := range embedded {
						object[name] = embeddedValue
					}
					continue
				}
			}
			name, omitEmpty, skip := wireFieldName(fieldInfo)
			if skip || (omitEmpty && value.Field(index).IsZero()) {
				continue
			}
			item, err := wireReflect(value.Field(index))
			if err != nil {
				return nil, err
			}
			object[name] = item
		}
		return object, nil
	default:
		if value.CanInterface() {
			return value.Interface(), nil
		}
		return nil, nil
	}
}

func marshalToAny(marshaler json.Marshaler) (any, error) {
	encoded, err := marshaler.MarshalJSON()
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}

func wireFieldName(field reflect.StructField) (name string, omitEmpty bool, skip bool) {
	if tag, ok := field.Tag.Lookup("json"); ok {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", false, true
		}
		if parts[0] != "" {
			name = parts[0]
		}
		for _, option := range parts[1:] {
			omitEmpty = omitEmpty || option == "omitempty"
		}
	}
	if name == "" {
		name = lowerCamel(field.Name)
	}
	return name, omitEmpty, false
}

func lowerCamel(name string) string {
	replacer := strings.NewReplacer(
		"CWD", "Cwd", "ID", "Id", "URL", "Url", "API", "Api", "UI", "Ui", "OAuth", "Oauth",
	)
	name = replacer.Replace(name)
	runes := []rune(name)
	if len(runes) > 0 {
		runes[0] = unicode.ToLower(runes[0])
	}
	return string(runes)
}

func stringifyJSON(runtime *sobek.Runtime, value sobek.Value) ([]byte, error) {
	jsonObject := runtime.Get("JSON").ToObject(runtime)
	stringify, ok := sobek.AssertFunction(jsonObject.Get("stringify"))
	if !ok {
		return nil, fmt.Errorf("JSON.stringify is unavailable")
	}
	encoded, err := stringify(jsonObject, value)
	if err != nil {
		return nil, err
	}
	if sobek.IsUndefined(encoded) {
		return nil, nil
	}
	content := []byte(encoded.String())
	if !json.Valid(content) {
		return nil, fmt.Errorf("value is not valid JSON")
	}
	return content, nil
}

func decodeJSON(runtime *sobek.Runtime, value sobek.Value, target any) error {
	encoded, err := stringifyJSON(runtime, value)
	if err != nil {
		return err
	}
	if len(encoded) == 0 {
		return nil
	}
	return json.Unmarshal(encoded, target)
}

func syncMutableEvent(runtime *sobek.Runtime, event extensions.Event, value sobek.Value) error {
	object := value.ToObject(runtime)
	switch typed := event.(type) {
	case extensions.ToolCallEvent:
		var input map[string]any
		if err := decodeJSON(runtime, object.Get("input"), &input); err != nil {
			return err
		}
		clear(typed.Input)
		for name, item := range input {
			typed.Input[name] = item
		}
	case extensions.BeforeProviderHeadersEvent:
		var headers map[string]*string
		if err := decodeJSON(runtime, object.Get("headers"), &headers); err != nil {
			return err
		}
		clear(typed.Headers)
		for name, item := range headers {
			typed.Headers[name] = item
		}
	}
	return nil
}

func decodeEventResult(
	runtime *sobek.Runtime,
	vm *runtimeVM,
	event extensions.Event,
	eventValue sobek.Value,
	value sobek.Value,
) (any, error) {
	if event.Type() == extensions.EventBeforeProviderRequest {
		if value == nil || sobek.IsUndefined(value) {
			return nil, nil
		}
		return extensions.ProviderRequestResult{Payload: value.Export(), Replace: true}, nil
	}
	if event.Type() == extensions.EventContext {
		if value == nil || sobek.IsUndefined(value) || sobek.IsNull(value) || !present(value.ToObject(runtime).Get("messages")) {
			return decodeContextMessages(runtime, eventValue.ToObject(runtime).Get("messages"))
		}
	}
	if value == nil || sobek.IsUndefined(value) || sobek.IsNull(value) {
		return nil, nil
	}
	switch event.Type() {
	case extensions.EventProjectTrust:
		var result extensions.ProjectTrustResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventResourcesDiscover:
		var result extensions.ResourcesDiscoverResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventSessionBeforeSwitch:
		var result extensions.SessionBeforeSwitchResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventSessionBeforeFork:
		var result extensions.SessionBeforeForkResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventSessionBeforeCompact:
		var result extensions.SessionBeforeCompactResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventSessionBeforeTree:
		var result extensions.SessionBeforeTreeResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventContext:
		return decodeContextResult(runtime, value)
	case extensions.EventBeforeAgentStart:
		var result extensions.BeforeAgentStartResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventMessageEnd:
		object := value.ToObject(runtime)
		if !present(object.Get("message")) {
			return extensions.MessageEndResult{}, nil
		}
		encoded, err := stringifyJSON(runtime, object.Get("message"))
		if err != nil {
			return nil, err
		}
		message, err := ai.UnmarshalMessage(encoded)
		if err == nil {
			return extensions.MessageEndResult{Message: message}, nil
		}
		var custom any
		if customErr := json.Unmarshal(encoded, &custom); customErr != nil {
			return nil, err
		}
		return extensions.MessageEndResult{Message: custom}, nil
	case extensions.EventToolCall:
		var result extensions.ToolCallResult
		return result, decodeJSON(runtime, value, &result)
	case extensions.EventToolResult:
		return decodeToolResultPatch(runtime, value)
	case extensions.EventUserBash:
		return decodeUserBashResult(runtime, vm, value)
	case extensions.EventInput:
		var result extensions.InputResult
		return result, decodeJSON(runtime, value, &result)
	default:
		return value.Export(), nil
	}
}

func decodeUserBashResult(runtime *sobek.Runtime, vm *runtimeVM, value sobek.Value) (extensions.UserBashResult, error) {
	object := value.ToObject(runtime)
	result := extensions.UserBashResult{}
	if resultValue := object.Get("result"); present(resultValue) {
		if err := decodeJSON(runtime, resultValue, &result.Result); err != nil {
			return result, err
		}
	}
	if operationsValue := object.Get("operations"); present(operationsValue) {
		owner := operationsValue.ToObject(runtime)
		execute, ok := sobek.AssertFunction(owner.Get("exec"))
		if !ok {
			return result, fmt.Errorf("user_bash operations.exec is not a function")
		}
		result.Operations = &jsBashOperations{vm: vm, owner: owner, execute: execute}
	}
	return result, nil
}

type jsBashOperations struct {
	vm      *runtimeVM
	owner   *sobek.Object
	execute sobek.Callable
}

func (operations *jsBashOperations) Exec(
	ctx context.Context,
	command string,
	cwd string,
	options tools.BashExecOptions,
) (tools.BashExecResult, error) {
	value, err := operations.vm.do(ctx, func(runtime *sobek.Runtime) (any, error) {
		jsOptions := runtime.NewObject()
		if options.Timeout != nil {
			must(runtime, jsOptions.Set("timeout", *options.Timeout))
		}
		if options.Env != nil {
			must(runtime, jsOptions.Set("env", toJS(runtime, options.Env)))
		}
		signal, signalErr := newAbortSignal(runtime, operations.vm, ctx)
		if signalErr != nil {
			return nil, signalErr
		}
		must(runtime, jsOptions.Set("signal", signal))
		must(runtime, jsOptions.Set("onData", func(call sobek.FunctionCall) sobek.Value {
			if options.OnData != nil {
				options.OnData(jsBytes(call.Argument(0)))
			}
			return sobek.Undefined()
		}))
		result, callErr := operations.execute(
			operations.owner,
			runtime.ToValue(command),
			runtime.ToValue(cwd),
			jsOptions,
		)
		if callErr != nil {
			return nil, callErr
		}
		result, callErr = operations.vm.awaitValue(ctx, runtime, result)
		if callErr != nil {
			return nil, callErr
		}
		var decoded struct {
			ExitCode *int `json:"exitCode"`
		}
		if err := decodeJSON(runtime, result, &decoded); err != nil {
			return nil, err
		}
		return tools.BashExecResult{ExitCode: decoded.ExitCode}, nil
	})
	if err != nil {
		return tools.BashExecResult{}, err
	}
	return value.(tools.BashExecResult), nil
}

func jsBytes(value sobek.Value) []byte {
	switch exported := value.Export().(type) {
	case string:
		return []byte(exported)
	case []byte:
		return append([]byte(nil), exported...)
	case sobek.ArrayBuffer:
		return append([]byte(nil), exported.Bytes()...)
	default:
		return []byte(value.String())
	}
}

func decodeContextResult(runtime *sobek.Runtime, value sobek.Value) (extensions.ContextResult, error) {
	object := value.ToObject(runtime)
	return decodeContextMessages(runtime, object.Get("messages"))
}

func decodeContextMessages(runtime *sobek.Runtime, value sobek.Value) (extensions.ContextResult, error) {
	encoded, err := stringifyJSON(runtime, value)
	if err != nil || len(encoded) == 0 {
		return extensions.ContextResult{}, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(encoded, &items); err != nil {
		return extensions.ContextResult{}, err
	}
	messages := make(agent.AgentMessages, 0, len(items))
	for _, item := range items {
		message, messageErr := ai.UnmarshalMessage(item)
		if messageErr == nil {
			messages = append(messages, message)
			continue
		}
		var custom any
		if err := json.Unmarshal(item, &custom); err != nil {
			return extensions.ContextResult{}, messageErr
		}
		messages = append(messages, custom)
	}
	return extensions.ContextResult{Messages: messages}, nil
}

func decodeToolResultPatch(runtime *sobek.Runtime, value sobek.Value) (extensions.ToolResultResult, error) {
	object := value.ToObject(runtime)
	result := extensions.ToolResultResult{}
	if objectHas(object, "content") {
		content, err := decodeToolContent(runtime, object.Get("content"))
		if err != nil {
			return result, err
		}
		result.Content = &content
	}
	if objectHas(object, "details") {
		details := object.Get("details").Export()
		result.Details = &details
	}
	if objectHas(object, "isError") {
		isError := object.Get("isError").ToBoolean()
		result.IsError = &isError
	}
	if objectHas(object, "usage") {
		var usage ai.Usage
		if err := decodeJSON(runtime, object.Get("usage"), &usage); err != nil {
			return result, err
		}
		result.Usage = &usage
	}
	return result, nil
}

func objectHas(object *sobek.Object, name string) bool {
	for _, property := range object.GetOwnPropertyNames() {
		if property == name && !sobek.IsUndefined(object.Get(name)) {
			return true
		}
	}
	return false
}

func decodeToolContent(runtime *sobek.Runtime, value sobek.Value) (ai.ToolResultContent, error) {
	encoded, err := stringifyJSON(runtime, value)
	if err != nil {
		return nil, err
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(encoded, &blocks); err != nil {
		return nil, err
	}
	content := make(ai.ToolResultContent, 0, len(blocks))
	for _, block := range blocks {
		var header struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &header); err != nil {
			return nil, err
		}
		switch header.Type {
		case "text":
			var text ai.TextContent
			if err := json.Unmarshal(block, &text); err != nil {
				return nil, err
			}
			content = append(content, &text)
		case "image":
			var image ai.ImageContent
			if err := json.Unmarshal(block, &image); err != nil {
				return nil, err
			}
			content = append(content, &image)
		default:
			content = append(content, &ai.UnknownContentBlock{Raw: append(json.RawMessage(nil), block...)})
		}
	}
	return content, nil
}

func stringProperty(object *sobek.Object, name string) string {
	value := object.Get(name)
	if value == nil || sobek.IsUndefined(value) || sobek.IsNull(value) {
		return ""
	}
	return value.String()
}
