package jsbridge

import (
	"context"

	"github.com/grafana/sobek"
)

type asyncLocalStorageKey struct{ _ byte }

type asyncLocalStorageValue struct {
	generation uint64
	store      sobek.Value
}

type asyncLocalStorage struct {
	key          *asyncLocalStorageKey
	generation   uint64
	defaultValue sobek.Value
}

func newAsyncHooksModule(runtime *sobek.Runtime, vm *runtimeVM) *sobek.Object {
	module := runtime.NewObject()
	constructor := func(call sobek.ConstructorCall) *sobek.Object {
		storage := &asyncLocalStorage{key: &asyncLocalStorageKey{}, defaultValue: sobek.Undefined()}
		name := ""
		if options := call.Argument(0); present(options) {
			object := options.ToObject(runtime)
			if value := object.Get("defaultValue"); value != nil && !sobek.IsUndefined(value) {
				storage.defaultValue = value
			}
			if value := object.Get("name"); present(value) {
				name = value.String()
			}
		}
		mustSet(runtime, call.This, "name", name)

		mustSet(runtime, call.This, "getStore", func(sobek.FunctionCall) sobek.Value {
			return storage.getStore(vm)
		})
		mustSet(runtime, call.This, "run", func(inner sobek.FunctionCall) sobek.Value {
			callback, ok := sobek.AssertFunction(inner.Argument(1))
			if !ok {
				panic(runtime.NewTypeError("The callback argument must be of type function"))
			}
			previous := vm.activeContext
			vm.activeContext = context.WithValue(vm.context(), storage.key, asyncLocalStorageValue{
				generation: storage.generation,
				store:      inner.Argument(0),
			})
			defer func() { vm.activeContext = previous }()
			value, err := callback(sobek.Undefined(), inner.Arguments[2:]...)
			if err != nil {
				panic(err)
			}
			return value
		})
		mustSet(runtime, call.This, "enterWith", func(inner sobek.FunctionCall) sobek.Value {
			vm.activeContext = context.WithValue(vm.context(), storage.key, asyncLocalStorageValue{
				generation: storage.generation,
				store:      inner.Argument(0),
			})
			return sobek.Undefined()
		})
		mustSet(runtime, call.This, "exit", func(inner sobek.FunctionCall) sobek.Value {
			callback, ok := sobek.AssertFunction(inner.Argument(0))
			if !ok {
				panic(runtime.NewTypeError("The callback argument must be of type function"))
			}
			previous := vm.activeContext
			vm.activeContext = context.WithValue(vm.context(), storage.key, asyncLocalStorageValue{
				generation: storage.generation + 1,
				store:      sobek.Undefined(),
			})
			defer func() { vm.activeContext = previous }()
			value, err := callback(sobek.Undefined(), inner.Arguments[1:]...)
			if err != nil {
				panic(err)
			}
			return value
		})
		mustSet(runtime, call.This, "disable", func(sobek.FunctionCall) sobek.Value {
			storage.generation++
			return sobek.Undefined()
		})
		return nil
	}
	mustSet(runtime, module, "AsyncLocalStorage", constructor)
	return module
}

func (storage *asyncLocalStorage) getStore(vm *runtimeVM) sobek.Value {
	if vm.activeContext != nil {
		if value, ok := vm.activeContext.Value(storage.key).(asyncLocalStorageValue); ok && value.generation == storage.generation {
			return value.store
		}
	}
	return storage.defaultValue
}
