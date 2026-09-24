//go:build js && wasm

package peer

import (
	"encoding/json"
	"syscall/js"
	"testing"

	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/plugins/bridge"
)

// fakeDurable returns a Map-backed async stand-in for ctx.storage and two
// cross-wired fake Worker WebSockets delivering binary messages.
func fakeDurable() (storage, left, right js.Value) {
	fakes := js.Global().Get("Function").New(`
		const map = new Map();
		const later = value => new Promise(resolve => setTimeout(() => resolve(value), 0));
		const storage = {
			map,
			get: keys => later(new Map(keys.filter(key => map.has(key)).map(key => [key, structuredClone(map.get(key))]))),
			put: entries => later().then(() => Object.entries(entries).forEach(([key, value]) => map.set(key, structuredClone(value)))),
			delete: keys => later().then(() => keys.filter(key => map.delete(key)).length),
			list: ({ prefix = "" } = {}) => later(new Map([...map.keys()].sort().filter(key => key.startsWith(prefix)).map(key => [key, structuredClone(map.get(key))]))),
		};
		const a = new EventTarget(), b = new EventTarget();
		for (const [self, other] of [[a, b], [b, a]]) {
			self.send = data => {
				if (self.closed) throw new Error("socket closed");
				const copy = data.slice().buffer;
				setTimeout(() => other.dispatchEvent(Object.assign(new Event("message"), { data: copy })), 0);
			};
			self.close = () => {
				if (self.closed) return;
				self.closed = other.closed = true;
				setTimeout(() => { self.dispatchEvent(new Event("close")); other.dispatchEvent(new Event("close")); }, 0);
			};
		}
		return [storage, a, b];
	`).Invoke()
	return fakes.Index(0), fakes.Index(1), fakes.Index(2)
}

// TestPeerOverDurableObjectPorts runs Bridge's pinned TLS over the Worker
// WebSocket net.Conn, with the identity kept by the connect.Store adapter in
// fake Durable Object storage, across an object restart.
func TestPeerOverDurableObjectPorts(t *testing.T) {
	ctx := t.Context()
	storage, left, right := fakeDurable()
	kv := worker.NewDurableKV(storage)
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	instance, open := object(t, kv, provider)
	self, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	peerID := self.PeerID()
	instance.Dispose()
	if stored := storage.Get("map").Call("get", StateKey); !stored.InstanceOf(js.Global().Get("Uint8Array")) {
		t.Fatalf("Durable Object storage has no %s", StateKey)
	}

	restarted, reopen := object(t, kv, provider)
	defer restarted.Dispose()
	self, err = reopen(ctx)
	if err != nil || self.PeerID() != peerID {
		t.Fatalf("restarted peer = %v, %v; want %s", self, err, peerID)
	}
	laptop, err := bridge.Open(&memoryStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = laptop.Close() }()
	admin(t, self, "trust", map[string]string{"peer_id": laptop.PeerID()})

	go func() { _ = self.Serve(ctx, worker.NewSocketConn(left)) }()
	channel, _, err := laptop.Connect(ctx, worker.NewSocketConn(right), peerID, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = channel.Close() }()
	var catalog struct {
		Items []bridge.Instance `json:"items"`
	}
	if err = channel.Call(ctx, "instances.list", struct{}{}, &catalog); err != nil || len(catalog.Items) != 1 || catalog.Items[0].ID != self.InstanceID() || !catalog.Items[0].Available {
		raw, _ := json.Marshal(catalog)
		t.Fatalf("catalog over the Worker socket = %s, %v", raw, err)
	}
}
