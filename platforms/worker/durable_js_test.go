//go:build js && wasm

package worker

import (
	"slices"
	"syscall/js"
	"testing"

	"github.com/OrdalieTech/orb/engine/harness"
	"github.com/OrdalieTech/orb/engine/harness/envtest"
)

// fakeStorage is a Map-backed stand-in for a Durable Object's ctx.storage:
// every call settles on a later macrotask, values are structured clones, and
// the per-call key limit is enforced.
func fakeStorage() js.Value {
	return js.Global().Get("Function").New(`
		const map = new Map();
		const later = value => new Promise(resolve => setTimeout(() => resolve(value), 0));
		const limit = count => { if (count > 128) throw new RangeError("more than 128 keys"); };
		return {
			map,
			get(keys) {
				limit(keys.length);
				const found = new Map();
				for (const key of keys) if (map.has(key)) found.set(key, structuredClone(map.get(key)));
				return later(found);
			},
			put(entries) {
				const keys = Object.keys(entries);
				limit(keys.length);
				return later().then(() => { for (const key of keys) map.set(key, structuredClone(entries[key])); });
			},
			delete(keys) {
				limit(keys.length);
				return later().then(() => keys.filter(key => map.delete(key)).length);
			},
			list({ prefix = "" } = {}) {
				const found = new Map();
				for (const key of [...map.keys()].sort()) if (key.startsWith(prefix)) found.set(key, structuredClone(map.get(key)));
				return later(found);
			},
		};
	`).Invoke()
}

func TestDurableKVBridge(t *testing.T) {
	ctx := t.Context()
	storage := fakeStorage()
	kv := NewDurableKV(storage)
	put := map[string][]byte{"a/1": []byte("one"), "a/2": {}, "b/1": {0, 255}}
	for index := range 300 {
		put["many/"+string(rune('a'+index%26))+string(rune('a'+index/26))] = []byte{byte(index)}
	}
	if err := kv.Write(ctx, put, nil); err != nil {
		t.Fatal(err)
	}
	if stored := storage.Get("map").Call("get", "b/1"); !stored.InstanceOf(js.Global().Get("Uint8Array")) || stored.Length() != 2 {
		t.Fatalf("stored value is %s, want a two-byte Uint8Array", stored.Call("toString").String())
	}
	found, err := kv.Get(ctx, []string{"a/1", "a/2", "missing"})
	if err != nil || len(found) != 2 || string(found["a/1"]) != "one" || len(found["a/2"]) != 0 {
		t.Fatalf("get = %v, %v", found, err)
	}
	listed, err := kv.List(ctx, "many/")
	if err != nil || len(listed) != 300 {
		t.Fatalf("list returned %d entries, %v", len(listed), err)
	}
	keys := make([]string, 0, len(listed))
	for key := range listed {
		keys = append(keys, key)
	}
	if err := kv.Write(ctx, map[string][]byte{"a/1": []byte("uno")}, keys); err != nil {
		t.Fatal(err)
	}
	if size := storage.Get("map").Get("size").Int(); size != 3 {
		t.Fatalf("storage holds %d keys after deleting 300, want 3", size)
	}
	if found, _ := kv.Get(ctx, []string{"a/1"}); string(found["a/1"]) != "uno" {
		t.Fatalf("rewritten value = %q", found["a/1"])
	}
	storage.Set("put", js.FuncOf(func(js.Value, []js.Value) any {
		return js.Global().Get("Promise").Call("reject", js.Global().Get("Error").New("quota exceeded"))
	}))
	if err := kv.Write(ctx, map[string][]byte{"x": nil}, nil); err == nil || err.Error() != "quota exceeded" {
		t.Fatalf("rejected put = %v", err)
	}
	storage.Set("list", js.Global().Get("Function").New(`throw new Error("thrown")`))
	if _, err := kv.List(ctx, ""); err == nil {
		t.Fatal("a synchronous throw was not reported")
	}
}

func TestDurableFileSystemConformance(t *testing.T) {
	envtest.TestFileSystem(t, func(t *testing.T) harness.FileSystem { return openFS(t, NewDurableKV(fakeStorage())) })
}

func TestDurableFileSystemSurvivesRestart(t *testing.T) {
	testSurvivesRestart(t, NewDurableKV(fakeStorage()))
}

func TestDurableStoreDocuments(t *testing.T) { testDocuments(t, NewDurableKV(fakeStorage())) }

func TestDurableInstanceResumesAfterRestart(t *testing.T) {
	storage := fakeStorage()
	testInstanceResumes(t, NewDurableKV(storage))
	keys := js.Global().Get("Array").Call("from", storage.Get("map").Call("keys"))
	var names []string
	for index := range keys.Length() {
		names = append(names, keys.Index(index).String())
	}
	if !slices.Contains(names, metaKey(filesNamespace, Workspace+"/notes/hello.txt")) || !slices.Contains(names, metaKey(documentsNamespace, currentSession)) {
		t.Fatalf("Durable Object storage keys = %v", names)
	}
}
