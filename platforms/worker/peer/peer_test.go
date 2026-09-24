package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/connect"
	"github.com/OrdalieTech/orb/connect/protocol"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/plugins/bridge"
)

// mapKV stands in for Durable Object storage.
type mapKV struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (kv *mapKV) Get(_ context.Context, keys []string) (map[string][]byte, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	out := map[string][]byte{}
	for _, key := range keys {
		if value, ok := kv.data[key]; ok {
			out[key] = bytes.Clone(value)
		}
	}
	return out, nil
}

func (kv *mapKV) List(_ context.Context, prefix string) (map[string][]byte, error) {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	out := map[string][]byte{}
	for key, value := range kv.data {
		if strings.HasPrefix(key, prefix) {
			out[key] = bytes.Clone(value)
		}
	}
	return out, nil
}

func (kv *mapKV) Write(_ context.Context, put map[string][]byte, del []string) error {
	kv.mu.Lock()
	defer kv.mu.Unlock()
	for key, value := range put {
		kv.data[key] = bytes.Clone(value)
	}
	for _, key := range del {
		delete(kv.data, key)
	}
	return nil
}

type memoryStore struct{ data []byte }

func (s *memoryStore) Load() ([]byte, error) { return s.data, nil }
func (s *memoryStore) Save(b []byte) error   { s.data = bytes.Clone(b); return nil }

// object opens a Worker object with bridge_call enabled, its peer opened lazily.
func object(t *testing.T, kv worker.KV, provider *faux.Provider) (*worker.Instance, func(context.Context) (*Peer, error)) {
	t.Helper()
	var instance *worker.Instance
	var once sync.Once
	var self *Peer
	var openErr error
	open := func(context.Context) (*Peer, error) {
		once.Do(func() { self, openErr = Open(instance, "Durable Object/1") })
		return self, openErr
	}
	instance, err := worker.Open(t.Context(), worker.Options{KV: kv, Model: provider.GetModel(), StreamFn: provider.StreamSimple,
		Settings: []byte(`{"plugins":{"bridge-agent-calls":true}}`), Tools: AgentCalls(open)})
	if err != nil {
		t.Fatal(err)
	}
	return instance, open
}

// dial connects laptop to the object over an in-memory stream, as a laptop
// dials the object's wss:// locator.
func dial(t *testing.T, self *Peer, laptop *bridge.Bridge) *protocol.Conn {
	t.Helper()
	server, client := net.Pipe()
	go func() { _ = self.Serve(t.Context(), server) }()
	channel, _, err := laptop.Connect(t.Context(), client, self.PeerID(), false)
	if err != nil {
		t.Fatal(err)
	}
	return channel
}

func admin(t *testing.T, self *Peer, method string, params any) json.RawMessage {
	t.Helper()
	raw, err := self.Admin(t.Context(), method, connect.JSON(params), "wss://orb.test/agents/a/bridge")
	if err != nil {
		t.Fatalf("admin %s: %v", method, err)
	}
	return raw
}

// TestPeerPairsAndCallsBothWays pairs a laptop Bridge with the object, then
// the laptop lists, prompts and reads the object's Orb, and the object's
// agent calls the laptop's instance through bridge_call under its own grant.
func TestPeerPairsAndCallsBothWays(t *testing.T) {
	ctx := t.Context()
	kv := &mapKV{data: map[string][]byte{}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	instance, open := object(t, kv, provider)
	defer instance.Dispose()
	self, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	laptop, err := bridge.Open(&memoryStore{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = laptop.Close() }()

	var invitation bridge.Invitation
	if err = json.Unmarshal(admin(t, self, "invite", struct{}{}), &invitation); err != nil || invitation.Locator != "wss://orb.test/agents/a/bridge" || invitation.PeerID != self.PeerID() {
		t.Fatalf("invitation = %+v, %v", invitation, err)
	}
	channel := dial(t, self, laptop)
	var claimed bridge.Invitation
	if err = channel.Call(ctx, "pair.claim", map[string]string{"invitation_id": invitation.ID, "token": invitation.Token}, &claimed); err != nil || claimed.Claimant != laptop.PeerID() {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	admin(t, self, "approve", map[string]string{"invitation_id": invitation.ID, "claimant": laptop.PeerID()})

	// Laptop → object: list, inspect, prompt, read back.
	var catalog struct {
		Items []bridge.Instance `json:"items"`
	}
	if err = channel.Call(ctx, "instances.list", struct{}{}, &catalog); err != nil || len(catalog.Items) != 1 || catalog.Items[0].ID != self.InstanceID() || !catalog.Items[0].Available || catalog.Items[0].Alias != "durable-object-1" {
		t.Fatalf("catalog = %+v, %v", catalog, err)
	}
	var described struct {
		Generation string `json:"registration_generation"`
		Target     struct {
			SessionID string `json:"session_id"`
			Revision  string `json:"session_revision"`
		} `json:"target"`
	}
	call := connect.Call{InstanceID: self.InstanceID(), Service: protocol.Service, Method: "inspect", Args: json.RawMessage(`{}`)}
	if err = channel.Call(ctx, "instances.call", call, &described); err != nil || described.Target.SessionID == "" {
		t.Fatalf("inspect = %+v, %v", described, err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello laptop")})
	call = connect.Call{InstanceID: self.InstanceID(), Service: protocol.Service, Method: "prompt", SessionID: described.Target.SessionID,
		Expected: connect.Expected{Generation: described.Generation, Revision: described.Target.Revision}, OperationID: protocol.NewID(), Args: json.RawMessage(`{"text":"hi from the laptop"}`)}
	var receipt connect.Receipt
	if err = channel.Call(ctx, "instances.call", call, &receipt); err != nil || receipt.Status != "accepted" {
		t.Fatalf("prompt receipt = %+v, %v", receipt, err)
	}
	for deadline := time.Now().Add(10 * time.Second); receipt.Status != "succeeded"; {
		if time.Now().After(deadline) || receipt.Status == "failed" {
			t.Fatalf("prompt operation = %+v", receipt)
		}
		time.Sleep(10 * time.Millisecond)
		if err = channel.Call(ctx, "operations.get", map[string]string{"instance_id": self.InstanceID(), "operation_id": call.OperationID}, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	var snapshot json.RawMessage
	if err = channel.Call(ctx, "events.subscribe", map[string]string{"instance_id": self.InstanceID()}, &snapshot); err != nil ||
		!strings.Contains(string(snapshot), "hi from the laptop") || !strings.Contains(string(snapshot), "hello laptop") {
		t.Fatalf("conversation read back = %s, %v", snapshot, err)
	}

	// Object → laptop: bridge_call under the object's own instance grant.
	var received []connect.Request
	var mu sync.Mutex
	registration, credential, err := laptop.Enroll("laptop", laptop.PersonalGroup())
	if err != nil {
		t.Fatal(err)
	}
	endpoint := connect.NewLocal(func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		var request connect.Request
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		mu.Lock()
		received = append(received, request)
		mu.Unlock()
		return connect.JSON(map[string]string{"answered_by": "laptop"}), nil
	})
	if _, err = laptop.Attach(registration.ID, credential, protocol.NewID(), endpoint); err != nil {
		t.Fatal(err)
	}
	subject := connect.Principal{PeerID: self.PeerID(), Subject: connect.Subject{Kind: "instance", InstanceID: self.InstanceID()}}
	if err = laptop.AddGrant(bridge.Grant{Principal: subject, Instances: []string{registration.ID}, Permissions: []string{"instance.inspect"}}); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"peer_id": laptop.PeerID(), "call": map[string]any{"instance_id": registration.ID, "service": protocol.Service, "method": "inspect", "args": map[string]any{}}}
	// Without the object's own destination grant the call is refused on the
	// object; with it, the laptop answers.
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("bridge_call", arguments, faux.ToolCallOptions{ID: "denied"})),
		faux.AssistantMessage("refused"),
		faux.AssistantMessage(faux.ToolCall("bridge_call", arguments, faux.ToolCallOptions{ID: "granted"})),
		faux.AssistantMessage("called the laptop"),
	})
	if err = instance.Session().Prompt(ctx, "Inspect the laptop."); err != nil {
		t.Fatal(err)
	}
	admin(t, self, "grant", bridge.Grant{Principal: subject, Destination: laptop.PeerID(), Instances: []string{registration.ID}, Permissions: []string{"instance.inspect"}})
	if err = instance.Session().Prompt(ctx, "Inspect the laptop again."); err != nil {
		t.Fatal(err)
	}
	results := map[string]string{}
	for _, entry := range instance.Session().Manager().GetEntries() {
		var message struct {
			Role    string `json:"role"`
			ID      string `json:"toolCallId"`
			IsError bool   `json:"isError"`
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		}
		if entry.Type == "message" && json.Unmarshal(entry.Message, &message) == nil && message.Role == "toolResult" && len(message.Content) > 0 {
			results[message.ID] = message.Content[0].Text
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 || received[0].Principal != subject || received[0].Call.Method != "inspect" ||
		!strings.Contains(results["granted"], "laptop") || !strings.Contains(results["denied"], "unauthorized") {
		t.Fatalf("laptop received %+v; tool results %v", received, results)
	}
	_ = channel.Close()
}

// TestPeerIdentitySurvivesRestart reopens the object from the same storage:
// same peer ID, same instance, and a paired laptop still authenticates.
func TestPeerIdentitySurvivesRestart(t *testing.T) {
	ctx := t.Context()
	kv := &mapKV{data: map[string][]byte{}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	first, open := object(t, kv, provider)
	self, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := kv.data[StateKey]; !ok {
		t.Fatalf("no %s after opening the peer", StateKey)
	}
	laptop, _ := bridge.Open(&memoryStore{}, true)
	defer func() { _ = laptop.Close() }()
	admin(t, self, "trust", map[string]string{"peer_id": laptop.PeerID()})
	admin(t, self, "trust", map[string]string{"peer_id": laptop.PeerID()})
	var status struct {
		Grants []bridge.Grant `json:"grants"`
	}
	if json.Unmarshal(admin(t, self, "status", struct{}{}), &status) != nil || len(status.Grants) != 1 {
		t.Fatalf("trust is not idempotent: %+v", status.Grants)
	}
	peerID, instanceID := self.PeerID(), self.InstanceID()
	first.Dispose()

	second, reopen := object(t, kv, provider)
	defer second.Dispose()
	again, err := reopen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.PeerID() != peerID || again.InstanceID() != instanceID {
		t.Fatalf("identity changed: %s/%s, want %s/%s", again.PeerID(), again.InstanceID(), peerID, instanceID)
	}
	channel := dial(t, again, laptop)
	defer func() { _ = channel.Close() }()
	var catalog struct {
		Items []bridge.Instance `json:"items"`
	}
	if err = channel.Call(ctx, "instances.list", struct{}{}, &catalog); err != nil || len(catalog.Items) != 1 || catalog.Items[0].Generation != "2" {
		t.Fatalf("catalog after restart = %+v, %v", catalog, err)
	}
	if _, err = again.Admin(ctx, "join", nil, ""); err == nil {
		t.Fatal("join should be refused on a Worker object")
	}
	if _, err = again.Admin(ctx, "invite", nil, "https://not-a-stream"); err == nil {
		t.Fatal("invite accepted a non-WebSocket locator")
	}
}
