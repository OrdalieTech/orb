package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OrdalieTech/orb/ai"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/ai/providers/faux"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/platforms/worker"
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

// object opens a Worker object with mitosis_call enabled, its peer opened lazily.
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
		Settings: []byte(`{"plugins":{"mitosis-agent-calls":true}}`), Tools: AgentCalls(open)})
	if err != nil {
		t.Fatal(err)
	}
	return instance, open
}

// dial connects laptop to the object over an in-memory stream, as a laptop
// dials the object's wss:// locator.
func dial(t *testing.T, self *Peer, laptop *mitosis.Mitosis) *protocol.Conn {
	t.Helper()
	server, client := net.Pipe()
	go func() { _ = self.Serve(t.Context(), server) }()
	channel, _, err := laptop.Connect(t.Context(), client, self.PeerID(), false)
	if err != nil {
		t.Fatal(err)
	}
	// The object registers its side of the channel after the laptop's Connect returns: a call
	// that needs it (sync join) right away failed "unavailable" on a slow runner.
	for self.mitosis.Connection(laptop.PeerID()) == nil {
		time.Sleep(time.Millisecond)
	}
	return channel
}

func admin(t *testing.T, self *Peer, method string, params any) json.RawMessage {
	t.Helper()
	raw, err := self.Admin(t.Context(), method, mitosis.JSON(params), "wss://orb.test/agents/a/mitosis")
	if err != nil {
		t.Fatalf("admin %s: %v", method, err)
	}
	return raw
}

// TestPeerPairsAndCallsBothWays pairs a laptop Mitosis with the object, then
// the laptop lists, prompts and reads the object's Orb, and the object's
// agent calls the laptop's instance through mitosis_call under its own grant.
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
	laptop, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = laptop.Close() }()

	var invitation mitosis.Invitation
	if err = json.Unmarshal(admin(t, self, "invite", struct{}{}), &invitation); err != nil || invitation.Locator != "wss://orb.test/agents/a/mitosis" || invitation.PeerID != self.PeerID() {
		t.Fatalf("invitation = %+v, %v", invitation, err)
	}
	channel := dial(t, self, laptop)
	var claimed mitosis.Invitation
	if err = channel.Call(ctx, "pair.claim", map[string]string{"invitation_id": invitation.ID, "token": invitation.Token}, &claimed); err != nil || claimed.Claimant != laptop.PeerID() {
		t.Fatalf("claim = %+v, %v", claimed, err)
	}
	admin(t, self, "approve", map[string]string{"invitation_id": invitation.ID, "claimant": laptop.PeerID()})

	// Laptop → object: list, inspect, prompt, read back.
	var agent struct {
		Items []mitosis.Conversation `json:"items"`
	}
	if err = channel.Call(ctx, "agent.describe", struct{}{}, &agent); err != nil || len(agent.Items) != 1 || agent.Items[0].SessionID == "" {
		t.Fatalf("agent = %+v, %v", agent, err)
	}
	var described struct {
		Target struct {
			SessionID string `json:"session_id"`
		} `json:"target"`
	}
	if err = channel.Call(ctx, "agent.call", mitosis.AgentCall{Method: "inspect"}, &described); err != nil || described.Target.SessionID != agent.Items[0].SessionID {
		t.Fatalf("inspect = %+v, %v", described, err)
	}
	provider.SetResponses([]faux.ResponseStep{faux.AssistantMessage("hello laptop")})
	session := described.Target.SessionID
	call := mitosis.AgentCall{SessionID: session, Method: "prompt", OperationID: protocol.NewID(), Args: json.RawMessage(`{"text":"hi from the laptop"}`)}
	var receipt mitosis.Receipt
	if err = channel.Call(ctx, "agent.call", call, &receipt); err != nil || receipt.Status != "accepted" || receipt.SessionID != session {
		t.Fatalf("prompt receipt = %+v, %v", receipt, err)
	}
	for deadline := time.Now().Add(10 * time.Second); receipt.Status != "succeeded"; {
		if time.Now().After(deadline) || receipt.Status == "failed" {
			t.Fatalf("prompt operation = %+v", receipt)
		}
		time.Sleep(10 * time.Millisecond)
		if err = channel.Call(ctx, "operations.get", map[string]string{"session_id": session, "operation_id": call.OperationID}, &receipt); err != nil {
			t.Fatal(err)
		}
	}
	var snapshot json.RawMessage
	if err = channel.Call(ctx, "events.subscribe", map[string]string{"session_id": session}, &snapshot); err != nil ||
		!strings.Contains(string(snapshot), "hi from the laptop") || !strings.Contains(string(snapshot), "hello laptop") {
		t.Fatalf("conversation read back = %s, %v", snapshot, err)
	}

	// Object → laptop: mitosis_call under the object's own instance grant.
	var received []mitosis.Request
	var mu sync.Mutex
	registration, credential, err := laptop.Enroll("laptop")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := mitosis.NewLocal(func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == "instances.describe" {
			return json.RawMessage(`{"target":{"session_id":"laptop-thread"}}`), nil
		}
		var request mitosis.Request
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, err
		}
		mu.Lock()
		received = append(received, request)
		mu.Unlock()
		return mitosis.JSON(map[string]string{"answered_by": "laptop"}), nil
	})
	if _, err = laptop.Attach(registration.ID, credential, protocol.NewID(), endpoint); err != nil {
		t.Fatal(err)
	}
	subject := mitosis.Principal{PeerID: self.PeerID(), Subject: mitosis.Subject{Kind: "agent"}}
	if err = laptop.AddGrant(mitosis.Grant{Principal: subject, Permissions: []string{"agent.inspect"}}); err != nil {
		t.Fatal(err)
	}
	arguments := map[string]any{"peer_id": laptop.PeerID(), "call": map[string]any{"method": "inspect", "args": map[string]any{}}}
	// Without the object's own destination grant the call is refused on the
	// object; with it, the laptop answers.
	provider.SetResponses([]faux.ResponseStep{
		faux.AssistantMessage(faux.ToolCall("mitosis_call", arguments, faux.ToolCallOptions{ID: "denied"})),
		faux.AssistantMessage("refused"),
		faux.AssistantMessage(faux.ToolCall("mitosis_call", arguments, faux.ToolCallOptions{ID: "granted"})),
		faux.AssistantMessage("called the laptop"),
	})
	if err = instance.Session().Prompt(ctx, "Inspect the laptop."); err != nil {
		t.Fatal(err)
	}
	admin(t, self, "grant", mitosis.Grant{Principal: subject, Destination: laptop.PeerID(), Permissions: []string{"agent.inspect"}})
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
	if len(received) != 1 || received[0].Principal != subject || received[0].Call.Method != "inspect" || received[0].Call.SessionID != "laptop-thread" ||
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
	laptop, _ := mitosis.Open(&document.Memory{}, true)
	defer func() { _ = laptop.Close() }()
	admin(t, self, "trust", map[string]string{"peer_id": laptop.PeerID()})
	admin(t, self, "trust", map[string]string{"peer_id": laptop.PeerID()})
	var status struct {
		Grants []mitosis.Grant `json:"grants"`
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
	var described struct {
		Generation string `json:"registration_generation"`
	}
	if err = channel.Call(ctx, "agent.call", mitosis.AgentCall{Method: "inspect"}, &described); err != nil || described.Generation != "2" {
		t.Fatalf("the agent after restart = %+v, %v", described, err)
	}
	if _, err = again.Admin(ctx, "join", nil, ""); err == nil {
		t.Fatal("join should be refused on a Worker object")
	}
	if _, err = again.Admin(ctx, "invite", nil, "https://not-a-stream"); err == nil {
		t.Fatal("invite accepted a non-WebSocket locator")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// An origin the browser refuses is relayed, at once and from then on; a token refresh's form
// POST makes the browser ask first, so the refusal sends nothing; other failures stay failures.
func TestRelayTakesWhatTheBrowserRefuses(t *testing.T) {
	var direct, relayed []string
	f := &fallback{relayed: map[string]bool{},
		direct: roundTrip(func(r *http.Request) (*http.Response, error) {
			direct = append(direct, r.URL.Host+" "+r.Header.Get("X-Orb"))
			if r.URL.Host == "offline.example" {
				return nil, errors.New("net/http: fetch() failed: AbortError")
			}
			if r.URL.Host != "open.example" {
				return nil, errors.New("net/http: fetch() failed: TypeError: Failed to fetch")
			}
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}),
		relay: roundTrip(func(r *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(r.Body)
			relayed = append(relayed, r.URL.Host+" "+string(body))
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}),
	}
	send := func(host, contentType string) error {
		r, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://"+host+"/", strings.NewReader("grant"))
		r.Header.Set("Content-Type", contentType)
		_, err := f.RoundTrip(r)
		return err
	}
	if send("open.example", "application/json") != nil || send("auth.example", "application/x-www-form-urlencoded") != nil ||
		send("auth.example", "application/x-www-form-urlencoded") != nil || send("offline.example", "application/json") == nil {
		t.Fatal("a request failed or a failure was relayed")
	}
	if want := []string{"open.example ", "auth.example 1", "offline.example "}; !slices.Equal(direct, want) {
		t.Fatalf("direct %q, want %q", direct, want)
	}
	if want := []string{"auth.example grant", "auth.example grant"}; !slices.Equal(relayed, want) {
		t.Fatalf("relayed %q, want %q", relayed, want)
	}
}

// TestPeerSyncsNamedAccountsAndPublishesName proves a Worker/tab peer receives accounts stored
// only in accounts.json (such as a second Codex account or OpenCode Zen), sees their models in
// its registry without a restart, and publishes its own name instead of "".
func TestPeerSyncsNamedAccountsAndPublishesName(t *testing.T) {
	ctx := t.Context()
	kv := &mapKV{data: map[string][]byte{}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	instance, open := object(t, kv, provider)
	defer instance.Dispose()
	self, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	owner.SetName("MacBook")
	ownerAuth := aiauth.NewDocumentStore(&document.Memory{})
	ownerAccounts := accounts.NewStoreWithDocument(&document.Memory{}, ownerAuth)
	added, err := ownerAccounts.Add(ctx, "openai", "lea", aiauth.APIKeyCredential("sk-lea"))
	if err != nil {
		t.Fatal(err)
	}
	owner.SetClass("credentials", ownerAuth)
	owner.SetClass("accounts", ownerAccounts)

	// Pair and connect owner <-> peer.
	admin(t, self, "trust", map[string]string{"peer_id": owner.PeerID()})
	if err := owner.AddGrant(mitosis.FullGrant(self.PeerID())); err != nil {
		t.Fatal(err)
	}
	channel := dial(t, self, owner)
	defer func() { _ = channel.Close() }()

	// Peer reports its display name on ping.
	var ping struct {
		Name string `json:"name"`
	}
	if err := channel.Call(ctx, "mitosis.ping", struct{}{}, &ping); err != nil || ping.Name != "Durable Object/1" {
		t.Fatalf("ping = %+v, %v", ping, err)
	}

	// Owner adds peer to sync group; peer joins and exchanges records, then syncs.
	if _, err := owner.SyncAdmin(ctx, mitosis.JSON(map[string]string{"add": self.PeerID()}), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := self.Admin(ctx, "sync", mitosis.JSON(map[string]string{"join": owner.PeerID()}), ""); err != nil {
		t.Fatal(err)
	}
	// Exchange signed scope records both ways, as Reconcile does.
	var group struct {
		GroupID string `json:"group_id"`
	}
	raw, _ := owner.Sync(ctx, nil)
	_ = json.Unmarshal(raw, &group)
	ownerRecs, _ := owner.Contacts(self.PeerID(), group.GroupID)
	for _, r := range ownerRecs {
		_, _ = self.mitosis.MergeContact(owner.PeerID(), r)
	}
	if _, err := self.mitosis.Sync(ctx, nil); err != nil {
		t.Fatal(err)
	}
	peerRecs, _ := self.mitosis.Contacts(owner.PeerID(), group.GroupID)
	for _, r := range peerRecs {
		_, _ = owner.MergeContact(self.PeerID(), r)
	}
	var synced struct {
		Names map[string]string `json:"names"`
	}
	raw, err = owner.Sync(ctx, nil)
	if err != nil || json.Unmarshal(raw, &synced) != nil || synced.Names[self.PeerID()] != "Durable Object/1" {
		t.Fatalf("owner sync names = %+v, %v", synced.Names, err)
	}

	// Peer's accounts store holds "lea" and its session's ModelRegistry offers openai models.
	rows, err := Accounts(instance.Host.Store).Accounts(ctx)
	if err != nil || len(rows) != 1 || rows[0].ID != added.ID || rows[0].Name != "lea" || !rows[0].Active {
		t.Fatalf("peer accounts = %#v, %v", rows, err)
	}
	avail := instance.Session().AvailableModels()
	if !slices.ContainsFunc(avail, func(m ai.Model) bool { return m.Provider == "openai" }) {
		t.Fatalf("peer registry did not see synced openai account (got %d models)", len(avail))
	}
}

// TestDialPeerRetriesSyncJoinUntilApproved proves a dialling peer (browser tab) that pairs and
// trusts its inviter before approval automatically retries sync join in the background once
// approved, receiving credentials and named accounts with no further join call from the page.
func TestDialPeerRetriesSyncJoinUntilApproved(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	kv := &mapKV{data: map[string][]byte{}}
	provider := faux.New(faux.Options{TokenSize: faux.FixedTokenSize(1000)})
	instance, open := object(t, kv, provider)
	defer instance.Dispose()
	self, err := open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := mitosis.Open(&document.Memory{}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Close() }()
	owner.SetName("MacBook")
	ownerAuth := aiauth.NewDocumentStore(&document.Memory{})
	_, _ = ownerAuth.Modify(ctx, "openai", func(*aiauth.Credential) (*aiauth.Credential, error) {
		return aiauth.APIKeyCredential("sk-mac"), nil
	})
	ownerAccounts := accounts.NewStoreWithDocument(&document.Memory{}, ownerAuth)
	if _, err := ownerAccounts.Add(ctx, "openai", "lea", aiauth.APIKeyCredential("sk-lea")); err != nil {
		t.Fatal(err)
	}
	owner.SetClass("credentials", ownerAuth)
	owner.SetClass("accounts", ownerAccounts)

	self.Dial(ctx, func(_ context.Context, _, _ string) (net.Conn, error) {
		server, client := net.Pipe()
		go func() { _, _, _ = owner.Connect(ctx, server, "", true) }()
		return client, nil
	})

	inv, err := owner.Invite([]mitosis.Grant{mitosis.FullGrant("")})
	if err != nil {
		t.Fatal(err)
	}
	inv.Locator = "ws://127.0.0.1:8789"
	admin(t, self, "join", inv)
	admin(t, self, "trust", map[string]string{"peer_id": owner.PeerID()})

	// Before approval, sync.group rejects the tab, so it is not in any group yet.
	time.Sleep(200 * time.Millisecond)
	if _, err := self.Admin(ctx, "sync", mitosis.JSON(struct{}{}), ""); mitosis.Code(err) != "not_found" {
		t.Fatalf("expected not_found before approval, got %v", err)
	}

	// Owner approves and admits the tab; with no further join call, followGroup joins and syncs.
	if err := owner.Approve(inv.ID, self.PeerID()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.SyncAdmin(ctx, mitosis.JSON(map[string]string{"add": self.PeerID()}), nil); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := Accounts(instance.Host.Store).Accounts(ctx)
		if err == nil && slices.ContainsFunc(rows, func(a accounts.Account) bool { return a.Name == "lea" }) {
			// Verify pending join document was cleared once joined.
			if raw, _ := instance.Host.Store.Document(joinDocument).Read(ctx); strings.TrimSpace(string(raw)) != "" {
				t.Fatalf("joinDocument not cleared after sync join: %q", raw)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("dialling peer never joined inviter's sync group or received synced accounts after approval")
}
