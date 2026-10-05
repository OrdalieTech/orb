//go:build js && wasm

package worker

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"syscall/js"
)

// maxBatch is the Durable Object limit on keys per get, put or delete call.
const maxBatch = 128

var (
	jsArray      = js.Global().Get("Array")
	jsObject     = js.Global().Get("Object")
	jsUint8Array = js.Global().Get("Uint8Array")
)

// NewDurableKV binds the KV port to a Durable Object's ctx.storage, or to any
// object whose promise-returning get, put, delete and list methods follow it.
// Values are stored as Uint8Array.
func NewDurableKV(storage js.Value) KV { return durableKV{storage} }

type durableKV struct{ storage js.Value }

// helpers are the shim's (deploy/worker.mjs). Each settles a wait on
// JavaScript by calling settle once with the wait's id and its results.
var helpers struct{ await, fetch, read, release js.Value }

// Bind hands the package the shim's helpers, through which the Durable Object
// storage and FetchTransport wait on JavaScript.
func Bind(shim js.Value) {
	helpers.await, helpers.fetch, helpers.read, helpers.release = shim.Get("await"), shim.Get("fetch"), shim.Get("read"), shim.Get("release")
}

// settlements are the waits in flight. One callback settles them all: a pair
// of js.Func per wait cost a wrapper, a finalizer and two releases each.
var settlements struct {
	sync.Mutex
	next    int
	waiting map[int]chan []js.Value
}

var settle = js.FuncOf(func(_ js.Value, args []js.Value) any {
	id := args[0].Int()
	settlements.Lock()
	done := settlements.waiting[id]
	delete(settlements.waiting, id)
	settlements.Unlock()
	done <- args[1:]
	return nil
})

// wait registers a wait: its id, and where its results arrive.
func wait() (int, <-chan []js.Value) {
	done := make(chan []js.Value, 1)
	settlements.Lock()
	defer settlements.Unlock()
	if settlements.waiting == nil {
		settlements.waiting = map[int]chan []js.Value{}
	}
	settlements.next++
	settlements.waiting[settlements.next] = done
	return settlements.next, done
}

// await blocks the calling goroutine until promise settles; the Go runtime
// yields to the JavaScript event loop meanwhile. A rejection settles with an
// undefined value and its reason's message.
func await(promise js.Value) (js.Value, error) {
	id, done := wait()
	helpers.await.Invoke(promise, id, settle)
	results := <-done
	if len(results) > 1 {
		return js.Value{}, errors.New(results[1].String())
	}
	return argument(results), nil
}

func argument(args []js.Value) js.Value {
	if len(args) == 0 {
		return js.Undefined()
	}
	return args[0]
}

// call invokes a storage method, converting a synchronous throw into an error.
func (kv durableKV) call(method string, args ...any) (promise js.Value, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("worker: storage.%s: %v", method, recovered)
		}
	}()
	return kv.storage.Call(method, args...), nil
}

func (kv durableKV) Get(_ context.Context, keys []string) (map[string][]byte, error) {
	result := make(map[string][]byte, len(keys))
	for batch := range slices.Chunk(keys, maxBatch) {
		promise, err := kv.call("get", jsStrings(batch))
		if err != nil {
			return nil, err
		}
		found, err := await(promise)
		if err != nil {
			return nil, err
		}
		if err := entries(found, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (kv durableKV) List(_ context.Context, prefix string) (map[string][]byte, error) {
	options := jsObject.New()
	options.Set("prefix", prefix)
	promise, err := kv.call("list", options)
	if err != nil {
		return nil, err
	}
	found, err := await(promise)
	if err != nil {
		return nil, err
	}
	result := map[string][]byte{}
	return result, entries(found, result)
}

// Write issues every put and delete before awaiting any, so a Durable
// Object coalesces them into one atomic write.
func (kv durableKV) Write(_ context.Context, put map[string][]byte, del []string) error {
	var pending []js.Value
	for batch := range slices.Chunk(slices.Sorted(maps.Keys(put)), maxBatch) {
		values := jsObject.New()
		for _, key := range batch {
			values.Set(key, bytesToJS(put[key]))
		}
		promise, err := kv.call("put", values)
		if err != nil {
			return err
		}
		pending = append(pending, promise)
	}
	for batch := range slices.Chunk(del, maxBatch) {
		promise, err := kv.call("delete", jsStrings(batch))
		if err != nil {
			return err
		}
		pending = append(pending, promise)
	}
	var failure error
	for _, promise := range pending {
		if _, err := await(promise); err != nil && failure == nil {
			failure = err
		}
	}
	return failure
}

func jsStrings(values []string) js.Value {
	array := jsArray.New(len(values))
	for index, value := range values {
		array.SetIndex(index, value)
	}
	return array
}

func bytesToJS(data []byte) js.Value {
	array := jsUint8Array.New(len(data))
	js.CopyBytesToJS(array, data)
	return array
}

// entries copies a Map of key to Uint8Array (or ArrayBuffer) into result.
func entries(found js.Value, result map[string][]byte) error {
	pairs := jsArray.Call("from", found)
	for index := range pairs.Length() {
		pair := pairs.Index(index)
		value := pair.Index(1)
		if !value.InstanceOf(jsUint8Array) {
			if value.Type() != js.TypeObject || value.Get("byteLength").Type() != js.TypeNumber {
				return errors.New("worker: stored value " + pair.Index(0).String() + " is not binary")
			}
			value = jsUint8Array.New(value)
		}
		data := make([]byte, value.Length())
		js.CopyBytesToGo(data, value)
		result[pair.Index(0).String()] = data
	}
	return nil
}
