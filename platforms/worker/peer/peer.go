// Package peer makes a Worker object a Mitosis peer (DECISIONS.md P11): a
// persisted identity in the object's Store, the object's own Orb attached as
// its orb.instance/1 service, pinned-TLS streams over the object's
// WebSockets, and the owner's admin surface. The object only accepts
// streams: it reaches another peer over the connection that peer opened.
package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/OrdalieTech/orb/agent"
	"github.com/OrdalieTech/orb/agent/config"
	"github.com/OrdalieTech/orb/agent/extensions"
	attach "github.com/OrdalieTech/orb/agent/mitosis"
	mitosistool "github.com/OrdalieTech/orb/agent/mitosis/tool"
	aiauth "github.com/OrdalieTech/orb/ai/auth"
	"github.com/OrdalieTech/orb/ai/auth/accounts"
	"github.com/OrdalieTech/orb/engine"
	orbhost "github.com/OrdalieTech/orb/host"
	"github.com/OrdalieTech/orb/internal/document"
	"github.com/OrdalieTech/orb/mitosis"
	"github.com/OrdalieTech/orb/mitosis/protocol"
	"github.com/OrdalieTech/orb/mitosis/relay"
	"github.com/OrdalieTech/orb/platforms/worker"
	"github.com/OrdalieTech/orb/plugins/memory"
	"github.com/OrdalieTech/orb/plugins/memory/filestore"
)

// Documents in the object's Store, beside settings.json and out of the file
// tools' reach.
const (
	StateDocument    = "/mitosis/state.json"
	instanceDocument = "/mitosis/instance.json"
	ledgerDocument   = "/mitosis/operations.json"
	joinDocument     = "/mitosis/join-group"
)

// StateKey is present once the object has a Mitosis identity; the shim serves
// the unauthenticated stream route only then.
var StateKey = worker.DocumentKey(StateDocument)

type identity struct {
	Version    int    `json:"version"`
	InstanceID string `json:"instance_id"`
	Credential string `json:"credential"`
}

type Peer struct {
	mitosis    *mitosis.Mitosis
	instanceID string
	attachment *attach.Attachment
	peers      *mitosis.Peers // set by Dial
	store      orbhost.Store
	joinWake   chan struct{}
}

// Memory is the memory plugin's journal in the object's Store, as the native agent directory keeps
// it, shared with the object's sync group.
func Memory(store orbhost.Store) memory.Store {
	return filestore.Journal{Document: store.Document(path.Join(worker.AgentDir, "memory", "memory.jsonl"))}
}

// Accounts is the named-account store in the object's Store over its auth.json, shared with the
// object's sync group.
func Accounts(store orbhost.Store) *accounts.Store {
	return accounts.NewStoreWithDocument(
		store.Document(path.Join(worker.AgentDir, "accounts.json")),
		aiauth.NewDocumentStore(store.Document(path.Join(worker.AgentDir, "auth.json"))).WithRemoved(store.Document(path.Join(worker.AgentDir, "auth-removed.json"))),
	)
}

// Open loads or creates the object's Mitosis identity, enrolls the object's
// Orb once under alias, and attaches it for this runtime's lifetime.
func Open(instance *worker.Instance, alias string) (*Peer, error) {
	store := instance.Host.Store
	b, err := mitosis.Open(store.Document(StateDocument), true)
	if err != nil {
		return nil, err
	}
	if alias != "" {
		b.SetName(alias)
	}
	creds := aiauth.NewDocumentStore(store.Document(path.Join(worker.AgentDir, "auth.json"))).WithRemoved(store.Document(path.Join(worker.AgentDir, "auth-removed.json")))
	b.SetClass("credentials", creds)
	b.SetClass("accounts", accounts.NewStoreWithDocument(store.Document(path.Join(worker.AgentDir, "accounts.json")), creds))
	b.SetClass("memory", memory.Shared{Store: Memory(store)})
	b.SetClass("settings", config.SharedSettings{Document: store.Document(path.Join(worker.AgentDir, "settings.json"))})
	if ws, ok := store.(*worker.Store); ok {
		ws.OnWrite = func(name string) {
			switch name {
			case path.Join(worker.AgentDir, "auth.json"),
				path.Join(worker.AgentDir, "auth-removed.json"),
				path.Join(worker.AgentDir, "accounts.json"),
				path.Join(worker.AgentDir, "memory", "memory.jsonl"),
				path.Join(worker.AgentDir, "settings.json"):
				b.Notify()
			}
		}
	}
	enrolled := store.Document(instanceDocument)
	raw, err := enrolled.Read(context.Background())
	if err != nil {
		return nil, err
	}
	var self identity
	if len(raw) == 0 {
		registration, credential, err := b.Enroll(Alias(alias))
		if err != nil {
			return nil, err
		}
		self = identity{1, registration.ID, credential}
		if err = document.Replace(context.Background(), enrolled, mitosis.JSON(self)); err != nil {
			return nil, err
		}
	} else if err = protocol.Decode(raw, &self); err != nil || self.Version != 1 || !protocol.ValidID(self.InstanceID) {
		return nil, errors.New("peer: corrupt instance registration")
	}
	attachment, err := attach.Attach(context.Background(), host{instance}, attach.Options{
		InstanceID: self.InstanceID, Store: store.Document(ledgerDocument), Authorize: b.Authorize,
	})
	if err != nil {
		return nil, err
	}
	generation, err := b.Attach(self.InstanceID, self.Credential, protocol.NewID(), attachment)
	if err == nil {
		err = attachment.SetGeneration(generation)
	}
	if err != nil {
		_ = attachment.Close()
		return nil, err
	}
	return &Peer{mitosis: b, instanceID: self.InstanceID, attachment: attachment, store: store, joinWake: make(chan struct{}, 1)}, nil
}

// Alias turns an object name into a Mitosis instance alias.
func Alias(name string) string {
	var out strings.Builder
	for _, c := range strings.ToLower(name) {
		if out.Len() == 48 {
			break
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			out.WriteRune(c)
		} else {
			out.WriteByte('-')
		}
	}
	if out.Len() == 0 {
		return "worker"
	}
	return out.String()
}

func (p *Peer) PeerID() string     { return p.mitosis.PeerID() }
func (p *Peer) InstanceID() string { return p.instanceID }

// Serve authenticates an inbound stream as the TLS server and keeps the
// channel until either side closes it.
func (p *Peer) Serve(ctx context.Context, stream net.Conn) error {
	channel, _, err := p.mitosis.Connect(ctx, stream, "", true)
	if err != nil {
		return err
	}
	select {
	case <-channel.Done():
	case <-ctx.Done():
		_ = channel.Close()
	}
	return nil
}

// Close ends the peer: its channels and its attachment.
func (p *Peer) Close() error { return p.mitosis.Close() }

// Follow runs a sync round each time the records change (a peer published here) and every 15 s,
// as a native device does, so a peer that only accepts streams keeps up with its group over the
// ones its peers opened, and a memory saved or a login refreshed here still reaches the group.
func (p *Peer) Follow(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-p.mitosis.Changes():
		}
		_, _ = p.mitosis.Sync(ctx, nil)
	}
}

// Dial makes the peer reach others itself, as a browser tab does over WebSocket: dial opens a
// stream to a peer's locator, and the peer keeps channels to the peers it knows, exchanges records
// with its scopes and runs its sync group's rounds, as a native Mitosis does. It can join an
// invitation, though no one can dial it. Call it once, before any Admin call, instead of Follow.
func (p *Peer) Dial(ctx context.Context, dial func(ctx context.Context, peer, locator string) (net.Conn, error)) {
	p.peers = mitosis.NewPeers(ctx, p.mitosis)
	p.peers.Dial = dial
	go p.peers.Reconcile()
	go p.peers.Greet()
	go p.followGroup(ctx)
}

// wantJoin records (or clears, when peer == "") the inviter whose sync group this dialling peer
// should follow once approved, persisted across reloads until sync join succeeds.
func (p *Peer) wantJoin(ctx context.Context, peer string) {
	if p.store == nil {
		return
	}
	_ = document.Replace(ctx, p.store.Document(joinDocument), []byte(peer))
	select {
	case p.joinWake <- struct{}{}:
	default:
	}
}

// followGroup retries joining the inviter's sync group until the inviter approves this peer and
// sync join succeeds, mirroring how orb mitosis join and mitosis/view follow the inviter's group.
func (p *Peer) followGroup(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		raw, _ := p.store.Document(joinDocument).Read(ctx)
		if inviter := strings.TrimSpace(string(raw)); inviter != "" {
			var group struct {
				GroupID string `json:"group_id"`
			}
			if status, err := p.mitosis.Admin(ctx, "sync", mitosis.JSON(struct{}{})); err == nil && json.Unmarshal(status, &group) == nil && group.GroupID != "" {
				p.wantJoin(ctx, "")
			} else if mitosis.Code(err) == "not_found" {
				if _, err := p.peers.Admin(ctx, "sync", mitosis.JSON(map[string]string{"join": inviter})); err == nil {
					p.wantJoin(ctx, "")
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-p.joinWake:
		case <-ticker.C:
		}
	}
}

// Call is the object's agent-initiated call: it needs this agent's own
// grant for the destination, and a channel the destination opened.
func (p *Peer) Call(ctx context.Context, peer string, call mitosis.AgentCall) (json.RawMessage, error) {
	subject, err := p.mitosis.Outbound(p.instanceID, peer, call.Method)
	if err != nil {
		return nil, err
	}
	var result json.RawMessage
	err = p.remote(ctx, peer, "agent.call", struct {
		mitosis.AgentCall
		Subject mitosis.Subject `json:"subject"`
	}{call, subject}, &result)
	return result, err
}

func (p *Peer) remote(ctx context.Context, peer, method string, params, result any) error {
	if p.peers != nil {
		raw, err := p.peers.Remote(ctx, peer, method, params)
		if err == nil && result != nil {
			err = json.Unmarshal(raw, result)
		}
		return err
	}
	channel := p.mitosis.Connection(peer)
	if channel == nil {
		return fmt.Errorf("peer %s has no open connection to this object: %w", peer, mitosis.Fail("unavailable"))
	}
	return channel.Call(ctx, method, params, result)
}

// Relay is a browser tab's HTTP: direct, except for an origin the browser refused (an API that
// sends no CORS headers), whose requests go through an online device of the tab's group from then
// on. open returns the tab's peer when a request needs it.
func Relay(direct http.RoundTripper, open func(context.Context) (*Peer, error)) http.RoundTripper {
	return &fallback{direct: direct, relayed: map[string]bool{}, relay: &relay.Transport{
		Devices: func(ctx context.Context) []string {
			p, err := open(ctx)
			if err != nil {
				return nil
			}
			raw, err := p.mitosis.Admin(ctx, "status", mitosis.JSON(struct{}{}))
			var status struct {
				Peers  []string          `json:"peers"`
				States map[string]string `json:"peer_states"`
			}
			if err != nil || json.Unmarshal(raw, &status) != nil {
				return nil
			}
			return slices.DeleteFunc(status.Peers, func(peer string) bool { return status.States[peer] != "connected" })
		},
		Call: func(ctx context.Context, device, method string, params, result any) error {
			p, err := open(ctx)
			if err != nil {
				return err
			}
			return p.remote(ctx, device, method, params, result)
		},
	}}
}

type fallback struct {
	direct, relay http.RoundTripper
	mu            sync.Mutex
	relayed       map[string]bool // origins the browser refused
}

func (f *fallback) RoundTrip(request *http.Request) (*http.Response, error) {
	origin := request.URL.Scheme + "://" + request.URL.Host
	f.mu.Lock()
	relayed := f.relayed[origin]
	f.mu.Unlock()
	if relayed {
		return f.relay.RoundTrip(request)
	}
	var body []byte
	if request.Body != nil {
		var err error
		body, err = io.ReadAll(request.Body)
		_ = request.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	again := func() *http.Request {
		r := request.Clone(request.Context())
		if request.Body != nil {
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		return r
	}
	direct := again()
	// A POST the browser sends without asking first (a form, as a token refresh is) reaches the
	// server even when the browser then hides the answer, which would spend a refresh token. One
	// more header makes the browser ask first, so a refusal sends nothing.
	if request.Method == http.MethodPost && !preflighted(request.Header) {
		direct.Header.Set("X-Orb", "1")
	}
	response, err := f.direct.RoundTrip(direct)
	if err == nil || request.Context().Err() != nil || !strings.Contains(err.Error(), "fetch() failed: TypeError") {
		return response, err
	}
	f.mu.Lock()
	f.relayed[origin] = true
	f.mu.Unlock()
	return f.relay.RoundTrip(again())
}

// preflighted reports whether a browser asks the server before sending a request with these
// headers: one is outside the CORS-safelisted ones.
func preflighted(header http.Header) bool {
	for name, values := range header {
		switch name {
		case "Accept", "Accept-Language", "Content-Language":
			continue
		case "Content-Type":
			if t, _, _ := mime.ParseMediaType(strings.Join(values, ",")); t == "application/x-www-form-urlencoded" || t == "multipart/form-data" || t == "text/plain" {
				continue
			}
		}
		return true
	}
	return false
}

// FullGrant is the invitation grant `orb mitosis pair invite` proposes: full
// control of every current and future instance.
func FullGrant(peer string) mitosis.Grant {
	return mitosis.ConversationGrant(peer)
}

// Admin mirrors the owner methods of `orb mitosis`. invite advertises locator,
// the object's public stream URL. An object that cannot dial offers no join;
// publish and the service lifecycle are never offered; self reports the
// object's identity.
func (p *Peer) Admin(ctx context.Context, method string, params json.RawMessage, locator string) (json.RawMessage, error) {
	empty := len(params) == 0 || string(params) == "{}" || string(params) == "null"
	switch method {
	case "self":
		return mitosis.JSON(map[string]string{"peer_id": p.PeerID(), "instance_id": p.instanceID, "locator": locator}), nil
	case "invite":
		if !strings.HasPrefix(locator, "wss://") && !strings.HasPrefix(locator, "ws://") {
			return nil, errors.New("peer: an invitation needs the object's ws:// or wss:// URL")
		}
		if empty {
			params = mitosis.JSON(map[string]any{"grants": []mitosis.Grant{FullGrant("")}})
		}
		raw, err := p.mitosis.Admin(ctx, method, params)
		if err != nil {
			return nil, err
		}
		var invitation mitosis.Invitation
		if err = json.Unmarshal(raw, &invitation); err != nil {
			return nil, err
		}
		invitation.Locator = locator
		return mitosis.JSON(invitation), nil
	case "trust":
		var request struct {
			PeerID string `json:"peer_id"`
		}
		if err := protocol.Decode(params, &request); err != nil {
			return nil, err
		}
		grant := FullGrant(request.PeerID)
		var status struct {
			Grants []mitosis.Grant `json:"grants"`
		}
		raw, err := p.mitosis.Admin(ctx, "status", mitosis.JSON(struct{}{}))
		if err == nil {
			err = json.Unmarshal(raw, &status)
		}
		if err != nil {
			return nil, err
		}
		for _, old := range status.Grants {
			if old.Principal == grant.Principal && old.Destination == "" && slices.Equal(old.Permissions, grant.Permissions) {
				if p.peers != nil {
					if _, syncErr := p.mitosis.Admin(ctx, "sync", mitosis.JSON(struct{}{})); mitosis.Code(syncErr) == "not_found" {
						p.wantJoin(ctx, request.PeerID)
					}
				}
				return mitosis.JSON(struct{}{}), nil
			}
		}
		if p.peers != nil {
			// Trusting the device it joined keeps its locator, so the peer dials it again, and
			// schedules following its sync group once the inviter approves.
			_, err = p.peers.Admin(ctx, "grant", mitosis.JSON(grant))
			if err == nil {
				if _, syncErr := p.mitosis.Admin(ctx, "sync", mitosis.JSON(struct{}{})); mitosis.Code(syncErr) == "not_found" {
					p.wantJoin(ctx, request.PeerID)
				}
			}
			return mitosis.JSON(struct{}{}), err
		}
		return mitosis.JSON(struct{}{}), p.mitosis.AddGrant(grant)
	case "remote":
		var request struct {
			PeerID string          `json:"peer_id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := protocol.Decode(params, &request); err != nil {
			return nil, err
		}
		var result json.RawMessage
		return result, p.remote(ctx, request.PeerID, request.Method, request.Params, &result)
	case "join", "sync", "block":
		if p.peers != nil {
			res, err := p.peers.Admin(ctx, method, params)
			if method == "sync" && err == nil {
				var q struct {
					Join   string `json:"join"`
					Remove string `json:"remove"`
				}
				if json.Unmarshal(params, &q) == nil && (q.Join != "" || q.Remove == p.PeerID()) {
					p.wantJoin(ctx, "")
				}
			}
			return res, err
		}
		if method == "join" {
			return nil, fmt.Errorf("peer: join is not available on a Worker object; create the invitation here and join it from the other Orb")
		}
		return p.mitosis.Admin(ctx, method, params)
	case "publish", "stop", "start", "ssh":
		return nil, fmt.Errorf("peer: %s is not available here; create the invitation here and join it from the other Orb", method)
	default:
		return p.mitosis.Admin(ctx, method, params)
	}
}

// AgentCalls adds the mitosis_call tool when the object's settings enable the
// mitosis-agent-calls plugin, as on the native CLI. open returns the object's
// peer when the tool runs.
func AgentCalls(open func(context.Context) (*Peer, error)) worker.ToolsFunc {
	return func(settings *config.SettingsManager) []extensions.ToolDefinition {
		if settings == nil || !settings.GetPlugins()["mitosis-agent-calls"] {
			return nil
		}
		return []extensions.ToolDefinition{callTool(open)}
	}
}

// host adapts the object to the non-owning attachment boundary.
type host struct{ instance *worker.Instance }

func (h host) Session() *agent.AgentSession                  { return h.instance.Session() }
func (h host) EnableControl() (*agent.SessionControl, error) { return h.instance.EnableControl() }
func (h host) ObserveSessions(observe func(*agent.AgentSession)) func() {
	return h.instance.ObserveSessions(observe)
}

func (h host) NewSession(ctx context.Context, options *extensions.NewSessionOptions) (extensions.SessionReplacementResult, error) {
	parent := ""
	if options != nil {
		parent = options.ParentSession
	}
	cancelled, err := h.instance.NewSessionContext(ctx, parent)
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

func (h host) SwitchSession(ctx context.Context, path string, _ *agent.AgentSessionRuntimeSwitchOptions) (extensions.SessionReplacementResult, error) {
	cancelled, err := h.instance.SwitchSessionContext(ctx, path)
	return extensions.SessionReplacementResult{Cancelled: cancelled}, err
}

func (h host) Fork(context.Context, string, *extensions.ForkOptions) (agent.AgentSessionRuntimeForkResult, error) {
	return agent.AgentSessionRuntimeForkResult{}, errors.New("peer: the Worker host does not fork sessions")
}

func callTool(open func(context.Context) (*Peer, error)) extensions.ToolDefinition {
	tool, _ := mitosistool.NewTool(func(ctx context.Context, peer string, call mitosis.AgentCall) (json.RawMessage, error) {
		self, err := open(ctx)
		if err != nil {
			return nil, err
		}
		return self.Call(ctx, peer, call)
	})
	spec := tool.Spec()
	return extensions.ToolDefinition{Name: spec.Name, Label: spec.Label, Description: spec.Description, Parameters: spec.Parameters,
		Execute: func(ctx context.Context, id string, raw any, update engine.AgentToolUpdateCallback, _ extensions.Context) (engine.AgentToolResult, error) {
			return tool.Execute(ctx, id, raw, update)
		},
	}
}
